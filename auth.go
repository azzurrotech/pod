package pod

// The exposed authentication helper.
//
// pod does NOT ship a built-in login. Instead it *exposes an auth helper*: the
// host application supplies identity and pod supplies the plumbing (request
// gate, optional session cookies, CSRF token issue/verify and a per-table,
// per-action policy hook) around every route, including uploads and shares.
//
// Typical wiring, where pod is mounted under a prefix:
//
//	auth := pod.NewAuth(pod.AuthOptions{
//	    // Resolve a request to an identity-free principal, or refuse it.
//	    Authenticate: func(r *http.Request) (pod.Principal, error) { … },
//	    // Optional: sessions + CSRF and a per-table, per-action policy.
//	    Sessions: true,
//	    CSRF:     true,
//	    Policy: func(p pod.Principal, table, action string) bool { … },
//	})
//	mux.Handle("/pod/", auth.Wrap(podHandler))
//
// With no helper wired (HandlerOptions.Auth == nil) the server is open, which
// is fine for local/trusted use and explicit everywhere else. The helper is
// deliberately transport-agnostic: implement Authenticate for your session
// store, SSO or token scheme and the same identity governs the HTML UI, the
// JSON/XML API, uploads and share links.

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"
)

// ErrUnauthorized is the sentinel returned by an Authenticate function that
// refuses a request. Wrap answers 401 with it (and 429 when ErrRateLimited is
// wrapped). Any other error is also treated as a 401 refusal.
var ErrUnauthorized = errors.New("pod: unauthorized")

// ErrRateLimited signals that a client exceeded a limit. When an Authenticate
// function returns it, Wrap answers 429.
var ErrRateLimited = errors.New("pod: rate limited")

// Principal is an identity-free caller: an opaque subject plus the scopes and
// roles (RBAC capabilities) needed to authorize a request. pod never stores
// who a subject is.
type Principal struct {
	Subject string
	Scopes  []string
	Roles   []string
	// Meta optionally carries extra, non-authoritative data for the host.
	Meta map[string]string
}

// HasScope reports whether the principal carries the given scope. The wildcard
// "*" grants every scope, and "action:*" grants every action of a namespace.
func (p Principal) HasScope(scope string) bool {
	if scope == "" {
		return true
	}
	for _, s := range p.Scopes {
		if s == "*" || s == scope {
			return true
		}
		if i := indexByte(s, ':'); i >= 0 && s[i+1:] == "*" && len(scope) > i+1 &&
			scope[:i+1] == s[:i+1] {
			return true
		}
	}
	return false
}

// HasRole reports whether the principal carries the given role (or "*").
func (p Principal) HasRole(role string) bool {
	for _, r := range p.Roles {
		if r == "*" || r == role {
			return true
		}
	}
	return false
}

// indexByte is a tiny strings.IndexByte wrapper kept local to avoid an import
// in the hot path; it returns -1 when not found.
func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// AuthOptions configures the auth helper. Only Authenticate is mandatory to
// gate requests; every other field is optional.
type AuthOptions struct {
	// Authenticate resolves a request to a principal, or returns an error to
	// refuse it (ErrUnauthorized / ErrRateLimited are recognised).
	Authenticate func(r *http.Request) (Principal, error)
	// Policy authorizes an action on a table. When nil, any authenticated
	// principal may act. action is one of "read", "write", "delete", "share".
	Policy func(p Principal, table, action string) bool
	// Sessions enables an in-memory session cookie so a host does not have to
	// re-authenticate every request. Sessions are process-local.
	Sessions bool
	// SessionTTL is the session lifetime (default 12h).
	SessionTTL time.Duration
	// CookieName overrides the session cookie name (default "pod_session").
	CookieName string
	// CSRF enables a double-submit CSRF check for unsafe methods.
	CSRF bool
	// OnError renders a refusal; when nil a plain status text is written.
	OnError func(w http.ResponseWriter, r *http.Request, status int, msg string)
}

// Auth is the auth helper. It is safe for concurrent use.
type Auth struct {
	opts AuthOptions

	mu       sync.Mutex
	sessions map[string]session
}

// session is one in-memory session record.
type session struct {
	principal Principal
	expires   time.Time
	csrf      string
}

// NewAuth builds an auth helper from options.
func NewAuth(o AuthOptions) *Auth {
	if o.SessionTTL <= 0 {
		o.SessionTTL = 12 * time.Hour
	}
	if o.CookieName == "" {
		o.CookieName = "pod_session"
	}
	return &Auth{opts: o, sessions: map[string]session{}}
}

type ctxKey int

const principalKey ctxKey = 0

// withPrincipal returns a context carrying the principal.
func withPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey, p)
}

// PrincipalFromContext returns the principal stored by Wrap, if any. It is the
// supported way for a host handler or pod route to read the caller's identity.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey).(Principal)
	return p, ok
}

// Principal returns the authenticated principal for a request that passed
// through Wrap.
func (a *Auth) Principal(r *http.Request) (Principal, bool) {
	return PrincipalFromContext(r.Context())
}

// Wrap gates a handler with the configured authentication. With no
// Authenticate function the helper is transparent (open server) so it can be
// mounted unconditionally.
func (a *Auth) Wrap(next http.Handler) http.Handler {
	if a == nil || a.opts.Authenticate == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := a.principalFor(w, r)
		if !ok {
			return // refusal already written
		}
		if a.opts.CSRF && a.sessionsEnabled() && isUnsafeMethod(r.Method) {
			if !a.checkCSRF(r) {
				a.refuse(w, r, http.StatusForbidden, "CSRF token missing or invalid")
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(withPrincipal(r.Context(), p)))
	})
}

// principalFor resolves the principal for a request, using the session cookie
// when sessions are enabled and falling back to Authenticate.
func (a *Auth) principalFor(w http.ResponseWriter, r *http.Request) (Principal, bool) {
	if a.sessionsEnabled() {
		if c, err := r.Cookie(a.opts.CookieName); err == nil {
			if sess, ok := a.lookupSession(c.Value); ok {
				return sess.principal, true
			}
		}
	}
	p, err := a.opts.Authenticate(r)
	if err != nil {
		if errors.Is(err, ErrRateLimited) {
			a.refuse(w, r, http.StatusTooManyRequests, err.Error())
		} else {
			a.refuse(w, r, http.StatusUnauthorized, err.Error())
		}
		return Principal{}, false
	}
	if a.sessionsEnabled() {
		a.startSession(w, p)
	}
	return p, true
}

// policyConfigured reports whether a per-table, per-action Policy is set.
func (a *Auth) policyConfigured() bool { return a != nil && a.opts.Policy != nil }

// Authorize evaluates the policy hook for one action on one table. With no
// policy configured it allows every principal.
func (a *Auth) Authorize(p Principal, table, action string) bool {
	if a == nil || a.opts.Policy == nil {
		return true
	}
	return a.opts.Policy(p, table, action)
}

// sessionsEnabled reports whether sessions are on and Authenticate is set.
func (a *Auth) sessionsEnabled() bool {
	return a != nil && a.opts.Sessions && a.opts.Authenticate != nil
}

// startSession issues (or refreshes) a session cookie for a principal.
func (a *Auth) startSession(w http.ResponseWriter, p Principal) {
	id, err := randomToken()
	if err != nil {
		return
	}
	csrf, err := randomToken()
	if err != nil {
		return
	}
	a.mu.Lock()
	a.sessions[id] = session{principal: p, expires: time.Now().Add(a.opts.SessionTTL), csrf: csrf}
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name:     a.opts.CookieName,
		Value:    id,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   false, // terminate TLS in front; set Secure at the edge
		Expires:  time.Now().Add(a.opts.SessionTTL),
	})
	// The CSRF value is delivered as a readable cookie for the double-submit
	// pattern; the client echoes it in the X-Pod-CSRF header.
	http.SetCookie(w, &http.Cookie{
		Name:     a.opts.CookieName + "_csrf",
		Value:    csrf,
		Path:     "/",
		SameSite: http.SameSiteLaxMode,
	})
}

// lookupSession returns a live session.
func (a *Auth) lookupSession(id string) (session, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.sessions[id]
	if !ok {
		return session{}, false
	}
	if time.Now().After(s.expires) {
		delete(a.sessions, id)
		return session{}, false
	}
	return s, true
}

// checkCSRF verifies the double-submit token against the session.
func (a *Auth) checkCSRF(r *http.Request) bool {
	c, err := r.Cookie(a.opts.CookieName)
	if err != nil {
		return false
	}
	sess, ok := a.lookupSession(c.Value)
	if !ok {
		return false
	}
	got := r.Header.Get("X-Pod-CSRF")
	if got == "" {
		got = r.FormValue("_csrf")
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(sess.csrf)) == 1
}

// CSRFFor returns the CSRF token for the caller's session, for embedding in a
// form or handing to a client. It returns "" when CSRF is off.
func (a *Auth) CSRFFor(r *http.Request) string {
	if a == nil || !a.opts.CSRF {
		return ""
	}
	c, err := r.Cookie(a.opts.CookieName)
	if err != nil {
		return ""
	}
	if sess, ok := a.lookupSession(c.Value); ok {
		return sess.csrf
	}
	return ""
}

// refuse writes an authentication/authorization failure.
func (a *Auth) refuse(w http.ResponseWriter, r *http.Request, status int, msg string) {
	if a.opts.OnError != nil {
		a.opts.OnError(w, r, status, msg)
		return
	}
	http.Error(w, msg, status)
}

// CleanupSessions drops expired sessions. It is safe to call periodically.
func (a *Auth) CleanupSessions() {
	if a == nil {
		return
	}
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	for id, s := range a.sessions {
		if now.After(s.expires) {
			delete(a.sessions, id)
		}
	}
}

// randomToken returns a base64url token with 32 bytes of entropy.
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		log.Printf("pod: auth: random token: %v", err)
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// isUnsafeMethod reports whether a method changes state and therefore needs
// CSRF protection.
func isUnsafeMethod(m string) bool {
	switch m {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

package pod

import (
	"strconv"
	"strings"
)

// Op is a comparison operator used by predicates.
type Op int

const (
	OpEq Op = iota
	OpNe
	OpGt
	OpGe
	OpLt
	OpLe
	OpLike
	OpNotLike
	OpIsNull
	OpIsNotNull
)

func (o Op) String() string {
	switch o {
	case OpEq:
		return "="
	case OpNe:
		return "!="
	case OpGt:
		return ">"
	case OpGe:
		return ">="
	case OpLt:
		return "<"
	case OpLe:
		return "<="
	case OpLike:
		return "LIKE"
	case OpNotLike:
		return "NOT LIKE"
	case OpIsNull:
		return "IS NULL"
	case OpIsNotNull:
		return "IS NOT NULL"
	}
	return "?"
}

// Predicate is a single field comparison. HasValue is false for unset / NULL
// comparisons; Null marks comparisons performed against an explicit NULL
// literal, which always evaluate to "unknown" (no match) in SQL semantics.
type Predicate struct {
	Field    string
	Op       Op
	Value    string
	HasValue bool
	Null     bool
}

// Cond is a boolean filter tree over records.
type Cond interface {
	Match(r *Record) bool
}

type condAll struct{}

func (condAll) Match(*Record) bool { return true }

// CondAll returns a condition that matches every record.
func CondAll() Cond { return condAll{} }

// PredCond wraps a single predicate.
type PredCond struct{ P Predicate }

func (c PredCond) Match(r *Record) bool {
	v, has := r.Get(c.P.Field)
	return c.P.Matches(v, has)
}

// AndCond matches when both sides match.
type AndCond struct{ L, R Cond }

func (c AndCond) Match(r *Record) bool { return c.L.Match(r) && c.R.Match(r) }

// OrCond matches when either side matches.
type OrCond struct{ L, R Cond }

func (c OrCond) Match(r *Record) bool { return c.L.Match(r) || c.R.Match(r) }

// Matches evaluates the predicate against a field value. has reports whether
// the field exists on the record at all.
func (p Predicate) Matches(v string, has bool) bool {
	switch p.Op {
	case OpEq:
		if p.Null {
			return false // field = NULL is never true
		}
		if !p.HasValue {
			return true // field = <empty> matches missing fields too
		}
		return has && v == p.Value
	case OpNe:
		if p.Null {
			return false
		}
		if !p.HasValue {
			return !has || v != ""
		}
		return !has || v != p.Value
	case OpIsNull:
		return !has
	case OpIsNotNull:
		return has
	case OpLike:
		if !p.HasValue || !has {
			return false
		}
		return likeMatch(p.Value, v)
	case OpNotLike:
		if !p.HasValue || !has {
			return true
		}
		return !likeMatch(p.Value, v)
	default:
		if !p.HasValue || !has {
			return false
		}
		return compareValues(v, p.Value, p.Op)
	}
}

// TopLevelEqs collects equality predicates reachable through the top-level
// AND chain of a condition. They are used to narrow a query with the index.
func TopLevelEqs(cond Cond) []Predicate {
	if cond == nil {
		return nil
	}
	var eqs []Predicate
	var walk func(c Cond)
	walk = func(c Cond) {
		switch t := c.(type) {
		case AndCond:
			walk(t.L)
			walk(t.R)
		case PredCond:
			if t.P.Op == OpEq && t.P.HasValue && !t.P.Null {
				eqs = append(eqs, t.P)
			}
		}
	}
	walk(cond)
	return eqs
}

// likeMatch implements SQL LIKE semantics for the subset of patterns using
// '%' (any run) and '_' (any single character). No backslash escapes.
func likeMatch(pattern, s string) bool {
	return likeMatchHelper(pattern, s)
}

func likeMatchHelper(pattern, s string) bool {
	// Dead-simple backtracking matcher.
	if pattern == "" {
		return s == ""
	}
	p := pattern[0]
	switch p {
	case '%':
		// Collapse consecutive % quickly.
		i := 0
		for i < len(pattern) && pattern[i] == '%' {
			i++
		}
		rest := pattern[i:]
		if rest == "" {
			return true
		}
		for j := 0; j <= len(s); j++ {
			if likeMatchHelper(rest, s[j:]) {
				return true
			}
		}
		return false
	case '_':
		return len(s) >= 1 && likeMatchHelper(pattern[1:], s[1:])
	default:
		return len(s) >= 1 && s[0] == p && likeMatchHelper(pattern[1:], s[1:])
	}
}

// compareValues compares two string values numerically when both parse as
// numbers, and lexically otherwise.
func compareValues(a, b string, op Op) bool {
	af, aerr := strconv.ParseFloat(strings.TrimSpace(a), 64)
	bf, berr := strconv.ParseFloat(strings.TrimSpace(b), 64)
	var cmp int
	if aerr == nil && berr == nil {
		switch {
		case af < bf:
			cmp = -1
		case af > bf:
			cmp = 1
		}
	} else {
		cmp = strings.Compare(a, b)
	}
	switch op {
	case OpGt:
		return cmp > 0
	case OpGe:
		return cmp >= 0
	case OpLt:
		return cmp < 0
	case OpLe:
		return cmp <= 0
	}
	return false
}

// sortValues compares two values for ORDER BY, numeric-aware.
func sortValues(a, b string) int {
	af, aerr := strconv.ParseFloat(strings.TrimSpace(a), 64)
	bf, berr := strconv.ParseFloat(strings.TrimSpace(b), 64)
	if aerr == nil && berr == nil {
		switch {
		case af < bf:
			return -1
		case af > bf:
			return 1
		default:
			return 0
		}
	}
	return strings.Compare(a, b)
}

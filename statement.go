package pod

import (
	"database/sql/driver"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// stmtKind enumerates the supported SQL statements.
type stmtKind int

const (
	stmtSelect stmtKind = iota
	stmtInsert
	stmtUpsert
	stmtUpdate
	stmtDelete
	stmtCreate
	stmtDrop
)

// valueExpr is a value in a statement: either a positional parameter (the
// database/sql '?' style) or a literal.
type valueExpr struct {
	IsParam bool
	Index   int
	Lit     string
	IsNull  bool
}

// ColumnSpec describes one column in a CREATE TABLE statement.
type ColumnSpec struct {
	Name    string
	Type    string
	Default *string
}

// Statement is a parsed SQL statement.
type Statement struct {
	Kind      stmtKind
	Table     string
	Columns   []string
	Types     []string
	Defaults  []*string
	Specs     []ColumnSpec
	Values    []valueExpr
	SelectAll bool
	Where     condNode
	OrderBy   string
	Desc      bool
	HasLimit  bool
	Limit     int
	Offset    int
	NumArgs   int
}

// condNode is a parsed condition tree that is bound to arguments at execution
// time (database/sql binds parameters per call).
type condNode interface {
	bind(args []string) (Cond, error)
}

type predNode struct {
	field string
	op    Op
	val   valueExpr
}

type andNode struct{ l, r condNode }

type orNode struct{ l, r condNode }

func (n *predNode) bind(args []string) (Cond, error) {
	if n.op == OpIsNull || n.op == OpIsNotNull {
		return PredCond{Predicate{Field: n.field, Op: n.op}}, nil
	}
	val, isNull, err := resolveValue(n.val, args)
	if err != nil {
		return nil, err
	}
	return PredCond{Predicate{Field: n.field, Op: n.op, Value: val, HasValue: !isNull, Null: isNull}}, nil
}

func (n *andNode) bind(args []string) (Cond, error) {
	l, err := n.l.bind(args)
	if err != nil {
		return nil, err
	}
	r, err := n.r.bind(args)
	if err != nil {
		return nil, err
	}
	return AndCond{L: l, R: r}, nil
}

func (n *orNode) bind(args []string) (Cond, error) {
	l, err := n.l.bind(args)
	if err != nil {
		return nil, err
	}
	r, err := n.r.bind(args)
	if err != nil {
		return nil, err
	}
	return OrCond{L: l, R: r}, nil
}

func resolveValue(v valueExpr, args []string) (string, bool, error) {
	if v.IsParam {
		if v.Index < 0 || v.Index >= len(args) {
			return "", false, fmt.Errorf("pod: missing argument for parameter %d", v.Index+1)
		}
		return args[v.Index], false, nil
	}
	if v.IsNull {
		return "", true, nil
	}
	return v.Lit, false, nil
}

// Parse parses a single SQL statement. Executing happens on the Store.
func Parse(sql string) (*Statement, error) {
	toks, err := tokenize(sql)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	st, err := p.parseStatement()
	if err != nil {
		return nil, err
	}
	if p.peek().kind != tokEOF {
		return nil, fmt.Errorf("pod: unexpected trailing input at offset %d", p.peek().pos)
	}
	st.NumArgs = p.narg
	return st, nil
}

// --- executor ---------------------------------------------------------------

// Execute runs a parsed statement against the store.
func (s *Store) Execute(st *Statement, args []driver.Value) (driver.Result, driver.Rows, error) {
	switch st.Kind {
	case stmtSelect:
		rows, err := s.execSelect(st, args)
		return nil, rows, err
	case stmtInsert, stmtUpsert:
		res, err := s.execInsert(st, args)
		return res, nil, err
	case stmtUpdate:
		res, err := s.execUpdate(st, args)
		return res, nil, err
	case stmtDelete:
		res, err := s.execDelete(st, args)
		return res, nil, err
	case stmtCreate:
		res, err := s.execCreate(st)
		return res, nil, err
	case stmtDrop:
		res, err := s.execDrop(st)
		return res, nil, err
	}
	return nil, nil, fmt.Errorf("pod: unknown statement kind")
}

func (s *Store) execSelect(st *Statement, args []driver.Value) (driver.Rows, error) {
	if err := checkArgs(st, args); err != nil {
		return nil, err
	}
	strs, err := argStrings(args)
	if err != nil {
		return nil, err
	}
	var cond Cond
	if st.Where != nil {
		cond, err = st.Where.bind(strs)
		if err != nil {
			return nil, err
		}
	}
	var opts *QueryOptions
	if st.OrderBy != "" || st.HasLimit || st.Offset > 0 {
		opts = &QueryOptions{OrderBy: st.OrderBy, Desc: st.Desc, Limit: st.Limit, Offset: st.Offset}
	}
	recs, err := s.Query(st.Table, cond, opts)
	if err != nil {
		return nil, err
	}
	return s.buildRows(st, recs), nil
}

func (s *Store) execInsert(st *Statement, args []driver.Value) (driver.Result, error) {
	if err := checkArgs(st, args); err != nil {
		return nil, err
	}
	if len(st.Columns) != len(st.Values) {
		return nil, fmt.Errorf("pod: column/value count mismatch")
	}
	strs, err := argStrings(args)
	if err != nil {
		return nil, err
	}
	values := make(map[string]string, len(st.Columns))
	for i, col := range st.Columns {
		v, _, err := resolveValue(st.Values[i], strs)
		if err != nil {
			return nil, err
		}
		values[col] = v
	}
	var rec *Record
	if st.Kind == stmtInsert {
		rec, err = s.Insert(st.Table, values)
	} else {
		rec, _, err = s.Upsert(st.Table, values["id"], values)
	}
	if err != nil {
		return nil, err
	}
	lastID, _ := strconv.ParseInt(rec.ID, 10, 64)
	return &Result{lastID: lastID, affected: 1}, nil
}

func (s *Store) execUpdate(st *Statement, args []driver.Value) (driver.Result, error) {
	if err := checkArgs(st, args); err != nil {
		return nil, err
	}
	if len(st.Columns) != len(st.Values) {
		return nil, fmt.Errorf("pod: column/value count mismatch")
	}
	strs, err := argStrings(args)
	if err != nil {
		return nil, err
	}
	setters := make(map[string]string, len(st.Columns))
	for i, col := range st.Columns {
		if col == "id" {
			return nil, fmt.Errorf("pod: cannot update the id column")
		}
		v, _, err := resolveValue(st.Values[i], strs)
		if err != nil {
			return nil, err
		}
		setters[col] = v
	}
	var cond Cond
	if st.Where != nil {
		cond, err = st.Where.bind(strs)
		if err != nil {
			return nil, err
		}
	}
	recs, err := s.Query(st.Table, cond, nil)
	if err != nil {
		return nil, err
	}
	affected := int64(0)
	for _, rec := range recs {
		if _, err := s.Update(st.Table, rec.ID, setters); err != nil {
			return nil, err
		}
		affected++
	}
	return &Result{affected: affected}, nil
}

func (s *Store) execDelete(st *Statement, args []driver.Value) (driver.Result, error) {
	if err := checkArgs(st, args); err != nil {
		return nil, err
	}
	strs, err := argStrings(args)
	if err != nil {
		return nil, err
	}
	var cond Cond
	if st.Where != nil {
		cond, err = st.Where.bind(strs)
		if err != nil {
			return nil, err
		}
	}
	recs, err := s.Query(st.Table, cond, nil)
	if err != nil {
		return nil, err
	}
	affected := int64(0)
	for _, rec := range recs {
		if err := s.Delete(st.Table, rec.ID); err != nil {
			return nil, err
		}
		affected++
	}
	return &Result{affected: affected}, nil
}

func (s *Store) execCreate(st *Statement) (driver.Result, error) {
	cols := make([]Column, 0, len(st.Specs))
	for _, sp := range st.Specs {
		typ := sp.Type
		if typ == "" {
			typ = "text"
		}
		cols = append(cols, Column{Name: sp.Name, Type: typ, Default: sp.Default})
	}
	if err := s.CreateTable(st.Table, cols); err != nil {
		return nil, err
	}
	return &Result{affected: 0}, nil
}

func (s *Store) execDrop(st *Statement) (driver.Result, error) {
	if err := s.DropTable(st.Table); err != nil {
		return nil, err
	}
	return &Result{affected: 0}, nil
}

// buildRows assembles database/sql rows for a SELECT.
func (s *Store) buildRows(st *Statement, recs []*Record) driver.Rows {
	var cols []string
	if st.SelectAll {
		cols = s.buildStarColumns(st.Table, recs)
	} else {
		cols = append([]string{}, st.Columns...)
	}
	return &Rows{cols: cols, recs: recs}
}

func (s *Store) buildStarColumns(table string, recs []*Record) []string {
	cols := []string{"id"}
	seen := map[string]bool{"id": true}
	var names []string
	if sc, err := s.Schema(table); err == nil && sc != nil {
		for _, c := range sc.Columns {
			if !seen[c.Name] {
				seen[c.Name] = true
				names = append(names, c.Name)
			}
		}
	}
	for _, r := range recs {
		for _, f := range r.Fields {
			if !seen[f.Name] {
				seen[f.Name] = true
				names = append(names, f.Name)
			}
		}
	}
	sort.Strings(names)
	return append(cols, names...)
}

func checkArgs(st *Statement, args []driver.Value) error {
	if len(args) != st.NumArgs {
		return fmt.Errorf("pod: expected %d arguments, got %d", st.NumArgs, len(args))
	}
	return nil
}

// argStrings converts driver values to strings for storage and comparison.
func argStrings(args []driver.Value) ([]string, error) {
	out := make([]string, len(args))
	for i, a := range args {
		if a == nil {
			continue
		}
		switch t := a.(type) {
		case []byte:
			out[i] = string(t)
		case string:
			out[i] = t
		case time.Time:
			out[i] = t.UTC().Format(timeFormat)
		case bool:
			if t {
				out[i] = "true"
			} else {
				out[i] = "false"
			}
		default:
			out[i] = fmt.Sprint(a)
		}
	}
	return out, nil
}

// --- tokenizer --------------------------------------------------------------

type tokenKind int

const (
	tokEOF tokenKind = iota
	tokIdent
	tokString
	tokNumber
	tokParam
	tokStar
	tokLParen
	tokRParen
	tokComma
	tokOp
)

type token struct {
	kind tokenKind
	text string
	pos  int
}

func tokenize(sql string) ([]token, error) {
	var toks []token
	i, n := 0, len(sql)
	for i < n {
		c := sql[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '\'' || c == '"':
			start := i
			quote := c
			i++
			var sb strings.Builder
			closed := false
			for i < n {
				if sql[i] == quote {
					if i+1 < n && sql[i+1] == quote {
						sb.WriteByte(quote)
						i += 2
						continue
					}
					i++
					closed = true
					break
				}
				sb.WriteByte(sql[i])
				i++
			}
			if !closed {
				return nil, fmt.Errorf("pod: unterminated string literal at offset %d", start)
			}
			toks = append(toks, token{tokString, sb.String(), start})
		case c == '?':
			toks = append(toks, token{tokParam, "?", i})
			i++
		case c == '*':
			toks = append(toks, token{tokStar, "*", i})
			i++
		case c == '(':
			toks = append(toks, token{tokLParen, "(", i})
			i++
		case c == ')':
			toks = append(toks, token{tokRParen, ")", i})
			i++
		case c == ',':
			toks = append(toks, token{tokComma, ",", i})
			i++
		case c == '=':
			toks = append(toks, token{tokOp, "=", i})
			i++
		case c == '!' && i+1 < n && sql[i+1] == '=':
			toks = append(toks, token{tokOp, "!=", i})
			i += 2
		case c == '<':
			if i+1 < n && sql[i+1] == '=' {
				toks = append(toks, token{tokOp, "<=", i})
				i += 2
			} else if i+1 < n && sql[i+1] == '>' {
				toks = append(toks, token{tokOp, "<>", i})
				i += 2
			} else {
				toks = append(toks, token{tokOp, "<", i})
				i++
			}
		case c == '>':
			if i+1 < n && sql[i+1] == '=' {
				toks = append(toks, token{tokOp, ">=", i})
				i += 2
			} else {
				toks = append(toks, token{tokOp, ">", i})
				i++
			}
		case isIdentStart(c):
			start := i
			for i < n && isIdentPart(sql[i]) {
				i++
			}
			toks = append(toks, token{tokIdent, sql[start:i], start})
		case c >= '0' && c <= '9' || (c == '-' && i+1 < n && sql[i+1] >= '0' && sql[i+1] <= '9'):
			start := i
			i++
			for i < n && (isDigit(sql[i]) || sql[i] == '.') {
				i++
			}
			toks = append(toks, token{tokNumber, sql[start:i], start})
		default:
			return nil, fmt.Errorf("pod: unexpected character %q at offset %d", string(c), i)
		}
	}
	return append(toks, token{tokEOF, "", n}), nil
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || isDigit(c)
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// --- parser -----------------------------------------------------------------

type parser struct {
	toks []token
	pos  int
	narg int
}

func (p *parser) peek() token { return p.toks[p.pos] }

func (p *parser) next() token {
	t := p.toks[p.pos]
	if t.kind != tokEOF {
		p.pos++
	}
	return t
}

func (p *parser) acceptIdent(upper string) bool {
	t := p.peek()
	if t.kind == tokIdent && strings.ToUpper(t.text) == upper {
		p.next()
		return true
	}
	return false
}

func (p *parser) expectIdent(upper string) error {
	if !p.acceptIdent(upper) {
		return fmt.Errorf("pod: expected %s at offset %d", upper, p.peek().pos)
	}
	return nil
}

func (p *parser) acceptComma() bool {
	if p.peek().kind == tokComma {
		p.next()
		return true
	}
	return false
}

func (p *parser) expect(text string) error {
	t := p.next()
	switch text {
	case "(":
		if t.kind == tokLParen {
			return nil
		}
	case ")":
		if t.kind == tokRParen {
			return nil
		}
	}
	return fmt.Errorf("pod: expected %q at offset %d", text, t.pos)
}

func (p *parser) ident() (string, error) {
	t := p.next()
	if t.kind != tokIdent && t.kind != tokString {
		return "", fmt.Errorf("pod: expected an identifier at offset %d", t.pos)
	}
	return t.text, nil
}

func (p *parser) parseStatement() (*Statement, error) {
	t := p.peek()
	if t.kind != tokIdent {
		return nil, fmt.Errorf("pod: expected a statement keyword at offset %d", t.pos)
	}
	switch strings.ToUpper(t.text) {
	case "CREATE":
		return p.parseCreate()
	case "DROP":
		return p.parseDrop()
	case "INSERT":
		return p.parseInsert(stmtInsert)
	case "UPSERT":
		return p.parseInsert(stmtUpsert)
	case "SELECT":
		return p.parseSelect()
	case "UPDATE":
		return p.parseUpdate()
	case "DELETE":
		return p.parseDelete()
	}
	return nil, fmt.Errorf("pod: unsupported statement %q", t.text)
}

func (p *parser) parseCreate() (*Statement, error) {
	if err := p.expectIdent("CREATE"); err != nil {
		return nil, err
	}
	if err := p.expectIdent("TABLE"); err != nil {
		return nil, fmt.Errorf("pod: CREATE must be followed by TABLE")
	}
	name, err := p.ident()
	if err != nil {
		return nil, err
	}
	st := &Statement{Kind: stmtCreate, Table: name}
	if err := p.expect("("); err != nil {
		return nil, err
	}
	for {
		sp, err := p.columnSpec()
		if err != nil {
			return nil, err
		}
		st.Specs = append(st.Specs, sp)
		if !p.acceptComma() {
			break
		}
	}
	if err := p.expect(")"); err != nil {
		return nil, err
	}
	return st, nil
}

func (p *parser) columnSpec() (ColumnSpec, error) {
	name, err := p.ident()
	if err != nil {
		return ColumnSpec{}, err
	}
	sp := ColumnSpec{Name: name, Type: "text"}
	if t := p.peek(); t.kind == tokIdent || t.kind == tokString {
		p.next()
		sp.Type = strings.ToLower(t.text)
	}
	if p.acceptIdent("DEFAULT") {
		v, err := p.defaultValue()
		if err != nil {
			return ColumnSpec{}, err
		}
		d := v.Lit
		sp.Default = &d
	}
	return sp, nil
}

func (p *parser) parseDrop() (*Statement, error) {
	if err := p.expectIdent("DROP"); err != nil {
		return nil, err
	}
	if err := p.expectIdent("TABLE"); err != nil {
		return nil, fmt.Errorf("pod: DROP must be followed by TABLE")
	}
	name, err := p.ident()
	if err != nil {
		return nil, err
	}
	return &Statement{Kind: stmtDrop, Table: name}, nil
}

func (p *parser) parseInsert(kind stmtKind) (*Statement, error) {
	kw := "INSERT"
	if kind == stmtUpsert {
		kw = "UPSERT"
	}
	if err := p.expectIdent(kw); err != nil {
		return nil, err
	}
	p.acceptIdent("INTO")
	name, err := p.ident()
	if err != nil {
		return nil, err
	}
	st := &Statement{Kind: kind, Table: name}
	if err := p.expect("("); err != nil {
		return nil, err
	}
	for {
		col, err := p.ident()
		if err != nil {
			return nil, err
		}
		st.Columns = append(st.Columns, col)
		if !p.acceptComma() {
			break
		}
	}
	if err := p.expect(")"); err != nil {
		return nil, err
	}
	if err := p.expectIdent("VALUES"); err != nil {
		return nil, fmt.Errorf("pod: expected VALUES")
	}
	if err := p.expect("("); err != nil {
		return nil, err
	}
	for {
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		st.Values = append(st.Values, v)
		if !p.acceptComma() {
			break
		}
	}
	if err := p.expect(")"); err != nil {
		return nil, err
	}
	return st, nil
}

func (p *parser) parseSelect() (*Statement, error) {
	if err := p.expectIdent("SELECT"); err != nil {
		return nil, err
	}
	st := &Statement{Kind: stmtSelect}
	if p.peek().kind == tokStar {
		p.next()
		st.SelectAll = true
	} else {
		for {
			col, err := p.ident()
			if err != nil {
				return nil, err
			}
			st.Columns = append(st.Columns, col)
			if !p.acceptComma() {
				break
			}
		}
	}
	if err := p.expectIdent("FROM"); err != nil {
		return nil, fmt.Errorf("pod: expected FROM")
	}
	name, err := p.ident()
	if err != nil {
		return nil, err
	}
	st.Table = name
	if p.acceptIdent("WHERE") {
		c, err := p.parseCond()
		if err != nil {
			return nil, err
		}
		st.Where = c
	}
	if p.acceptIdent("ORDER") {
		if err := p.expectIdent("BY"); err != nil {
			return nil, err
		}
		col, err := p.ident()
		if err != nil {
			return nil, err
		}
		st.OrderBy = col
		if p.acceptIdent("ASC") {
			st.Desc = false
		}
		if p.acceptIdent("DESC") {
			st.Desc = true
		}
	}
	if p.acceptIdent("LIMIT") {
		n, err := p.intLit()
		if err != nil {
			return nil, err
		}
		st.HasLimit = true
		st.Limit = n
	}
	if p.acceptIdent("OFFSET") {
		n, err := p.intLit()
		if err != nil {
			return nil, err
		}
		st.Offset = n
	}
	return st, nil
}

func (p *parser) parseUpdate() (*Statement, error) {
	if err := p.expectIdent("UPDATE"); err != nil {
		return nil, err
	}
	name, err := p.ident()
	if err != nil {
		return nil, err
	}
	st := &Statement{Kind: stmtUpdate, Table: name}
	if err := p.expectIdent("SET"); err != nil {
		return nil, fmt.Errorf("pod: expected SET")
	}
	for {
		col, err := p.ident()
		if err != nil {
			return nil, err
		}
		st.Columns = append(st.Columns, col)
		t := p.next()
		if t.kind != tokOp || t.text != "=" {
			return nil, fmt.Errorf("pod: expected = after column %q", col)
		}
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		st.Values = append(st.Values, v)
		if !p.acceptComma() {
			break
		}
	}
	if p.acceptIdent("WHERE") {
		c, err := p.parseCond()
		if err != nil {
			return nil, err
		}
		st.Where = c
	}
	return st, nil
}

func (p *parser) parseDelete() (*Statement, error) {
	if err := p.expectIdent("DELETE"); err != nil {
		return nil, err
	}
	if err := p.expectIdent("FROM"); err != nil {
		return nil, fmt.Errorf("pod: expected FROM")
	}
	name, err := p.ident()
	if err != nil {
		return nil, err
	}
	st := &Statement{Kind: stmtDelete, Table: name}
	if p.acceptIdent("WHERE") {
		c, err := p.parseCond()
		if err != nil {
			return nil, err
		}
		st.Where = c
	}
	return st, nil
}

// value parses a value expression (param, literal, or NULL).
func (p *parser) value() (valueExpr, error) {
	t := p.next()
	switch t.kind {
	case tokParam:
		idx := p.narg
		p.narg++
		return valueExpr{IsParam: true, Index: idx}, nil
	case tokString, tokNumber:
		return valueExpr{Lit: t.text}, nil
	case tokIdent:
		if strings.ToUpper(t.text) == "NULL" {
			return valueExpr{IsNull: true}, nil
		}
		return valueExpr{Lit: t.text}, nil
	}
	return valueExpr{}, fmt.Errorf("pod: expected a value at offset %d", t.pos)
}

// defaultValue parses a DEFAULT literal (parameters not allowed).
func (p *parser) defaultValue() (valueExpr, error) {
	t := p.next()
	switch t.kind {
	case tokString, tokNumber:
		return valueExpr{Lit: t.text}, nil
	case tokIdent:
		if strings.ToUpper(t.text) == "NULL" {
			return valueExpr{IsNull: true}, nil
		}
		return valueExpr{Lit: t.text}, nil
	case tokParam:
		return valueExpr{}, fmt.Errorf("pod: parameters are not allowed in DEFAULT")
	}
	return valueExpr{}, fmt.Errorf("pod: expected a default value at offset %d", t.pos)
}

func (p *parser) intLit() (int, error) {
	v, err := p.value()
	if err != nil {
		return 0, err
	}
	if v.IsParam || v.IsNull {
		return 0, fmt.Errorf("pod: expected a number")
	}
	n, err := strconv.Atoi(strings.TrimSpace(v.Lit))
	if err != nil {
		return 0, fmt.Errorf("pod: expected a number, got %q", v.Lit)
	}
	if n < 0 {
		return 0, fmt.Errorf("pod: negative numbers not allowed here")
	}
	return n, nil
}

func (p *parser) parseCond() (condNode, error) { return p.parseOr() }

func (p *parser) parseOr() (condNode, error) {
	l, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.acceptIdent("OR") {
		r, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		l = &orNode{l: l, r: r}
	}
	return l, nil
}

func (p *parser) parseAnd() (condNode, error) {
	l, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	for p.acceptIdent("AND") {
		r, err := p.parsePrimary()
		if err != nil {
			return nil, err
		}
		l = &andNode{l: l, r: r}
	}
	return l, nil
}

func (p *parser) parsePrimary() (condNode, error) {
	if p.peek().kind == tokLParen {
		p.next()
		c, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if err := p.expect(")"); err != nil {
			return nil, err
		}
		return c, nil
	}
	return p.parsePredicate()
}

func (p *parser) parsePredicate() (condNode, error) {
	field, err := p.ident()
	if err != nil {
		return nil, err
	}
	t := p.peek()
	if t.kind == tokIdent {
		switch strings.ToUpper(t.text) {
		case "IS":
			p.next()
			neg := false
			if p.acceptIdent("NOT") {
				neg = true
			}
			if err := p.expectIdent("NULL"); err != nil {
				return nil, fmt.Errorf("pod: expected NULL after IS")
			}
			if neg {
				return &predNode{field: field, op: OpIsNotNull}, nil
			}
			return &predNode{field: field, op: OpIsNull}, nil
		case "NOT":
			p.next()
			if err := p.expectIdent("LIKE"); err != nil {
				return nil, fmt.Errorf("pod: expected LIKE after NOT")
			}
			v, err := p.value()
			if err != nil {
				return nil, err
			}
			return &predNode{field: field, op: OpNotLike, val: v}, nil
		case "LIKE":
			p.next()
			v, err := p.value()
			if err != nil {
				return nil, err
			}
			return &predNode{field: field, op: OpLike, val: v}, nil
		}
	}
	if t.kind != tokOp {
		return nil, fmt.Errorf("pod: expected a comparison operator at offset %d", t.pos)
	}
	p.next()
	op, err := opFromString(t.text)
	if err != nil {
		return nil, err
	}
	v, err := p.value()
	if err != nil {
		return nil, err
	}
	return &predNode{field: field, op: op, val: v}, nil
}

func opFromString(s string) (Op, error) {
	switch s {
	case "=":
		return OpEq, nil
	case "!=", "<>":
		return OpNe, nil
	case ">":
		return OpGt, nil
	case ">=":
		return OpGe, nil
	case "<":
		return OpLt, nil
	case "<=":
		return OpLe, nil
	}
	return OpEq, fmt.Errorf("pod: unknown operator %q", s)
}

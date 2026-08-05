package workflow

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/orlandoburli/apiary/internal/model"
)

// EvalContext provides the data a split condition is evaluated against.
type EvalContext struct {
	Cell   model.SourceItem
	Memory map[string]string    // workflow-memory Step Data keys → rendered values
	Steps  map[string]StepState // step id → terminal state info
	// Event carries the PR event payload for an event-triggered instance
	// (kind, body, author, author_association, pr_number, pr_url). Nil for
	// item-triggered instances — event.* accessors then resolve as missing ("").
	Event map[string]string
}

// StepState exposes a completed step's outcome to expressions.
type StepState struct {
	State    string // passed | failed
	ExitCode int
	Output   string
}

// Expr is a parsed, reusable condition expression.
type Expr struct {
	root exprNode
	src  string
}

// ParseExpr parses a condition expression (used in split branches). It returns a
// reusable Expr or a parse error — call at config-load time to validate.
func ParseExpr(src string) (*Expr, error) {
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &exprParser{toks: toks}
	node, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.peek().kind != tEOF {
		return nil, fmt.Errorf("unexpected token %q in expression %q", p.peek().val, src)
	}
	return &Expr{root: node, src: src}, nil
}

// Eval evaluates the expression against ctx.
func (e *Expr) Eval(ctx EvalContext) (bool, error) {
	return e.root.eval(ctx)
}

// LintExpr statically parses a condition expression, accepting the optional
// ${{ }} wrapper kept by the v2 lowering pass. It is injected into
// config.LintExpr by the cli package so `apiary validate` rejects malformed
// expressions pre-flight instead of discovering them at runtime (#180).
func LintExpr(src string) error {
	_, err := ParseExpr(stripExprDelimiters(src))
	return err
}

// String returns the original source.
func (e *Expr) String() string { return e.src }

// ── lexer ───────────────────────────────────────────────────────────────────

type tokKind int

const (
	tEOF tokKind = iota
	tIdent
	tString
	tNumber
	tDot
	tLParen
	tRParen
	tEq
	tNeq
)

type token struct {
	kind tokKind
	val  string
}

func lex(s string) ([]token, error) {
	var toks []token
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '(':
			toks = append(toks, token{tLParen, "("})
			i++
		case c == ')':
			toks = append(toks, token{tRParen, ")"})
			i++
		case c == '.':
			toks = append(toks, token{tDot, "."})
			i++
		case c == '=':
			if i+1 < len(s) && s[i+1] == '=' {
				toks = append(toks, token{tEq, "=="})
				i += 2
			} else {
				return nil, fmt.Errorf("unexpected '=' (did you mean '=='?) at position %d", i)
			}
		case c == '!':
			if i+1 < len(s) && s[i+1] == '=' {
				toks = append(toks, token{tNeq, "!="})
				i += 2
			} else {
				return nil, fmt.Errorf("unsupported operator '!' at position %d — use 'not' (or '!=' for inequality)", i)
			}
		case c == '&':
			return nil, fmt.Errorf("unsupported operator '&&' at position %d — use 'and' (C-style operators are not supported)", i)
		case c == '|':
			return nil, fmt.Errorf("unsupported operator '||' at position %d — use 'or' (C-style operators are not supported)", i)
		case c == '"' || c == '\'':
			quote := c
			j := i + 1
			var b strings.Builder
			for j < len(s) && s[j] != quote {
				b.WriteByte(s[j])
				j++
			}
			if j >= len(s) {
				return nil, fmt.Errorf("unterminated string literal in expression")
			}
			toks = append(toks, token{tString, b.String()})
			i = j + 1
		case c >= '0' && c <= '9' || (c == '-' && i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '9'):
			j := i + 1
			for j < len(s) && (s[j] >= '0' && s[j] <= '9' || s[j] == '.') {
				j++
			}
			toks = append(toks, token{tNumber, s[i:j]})
			i = j
		case c == '-':
			return nil, fmt.Errorf("unexpected '-' at position %d — identifiers cannot contain '-' (hyphenated step ids cannot be referenced in expressions)", i)
		case isIdentStart(c):
			j := i + 1
			for j < len(s) && isIdentChar(s[j]) {
				j++
			}
			toks = append(toks, token{tIdent, s[i:j]})
			i = j
		default:
			return nil, fmt.Errorf("unexpected character %q at position %d", string(c), i)
		}
	}
	toks = append(toks, token{tEOF, ""})
	return toks, nil
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentChar(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

// ── parser ──────────────────────────────────────────────────────────────────

type exprParser struct {
	toks []token
	pos  int
}

func (p *exprParser) peek() token { return p.toks[p.pos] }
func (p *exprParser) next() token {
	t := p.toks[p.pos]
	if p.pos < len(p.toks)-1 {
		p.pos++
	}
	return t
}

func (p *exprParser) parseOr() (exprNode, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.isKeyword("or") {
		p.next()
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = orNode{left, right}
	}
	return left, nil
}

func (p *exprParser) parseAnd() (exprNode, error) {
	left, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for p.isKeyword("and") {
		p.next()
		right, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		left = andNode{left, right}
	}
	return left, nil
}

func (p *exprParser) parseNot() (exprNode, error) {
	if p.isKeyword("not") {
		p.next()
		x, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return notNode{x}, nil
	}
	return p.parseAtom()
}

func (p *exprParser) parseAtom() (exprNode, error) {
	if p.peek().kind == tLParen {
		p.next()
		inner, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if p.peek().kind != tRParen {
			return nil, fmt.Errorf("expected ')' in expression")
		}
		p.next()
		return inner, nil
	}
	return p.parseComparison()
}

func (p *exprParser) parseComparison() (exprNode, error) {
	// accessor: IDENT ("." IDENT)*
	if p.peek().kind != tIdent {
		return nil, fmt.Errorf("expected accessor, got %q", p.peek().val)
	}
	var path []string
	path = append(path, p.next().val)
	for p.peek().kind == tDot {
		p.next()
		if p.peek().kind != tIdent {
			return nil, fmt.Errorf("expected identifier after '.'")
		}
		path = append(path, p.next().val)
	}

	// operator
	var op string
	switch {
	case p.peek().kind == tEq:
		op = "=="
		p.next()
	case p.peek().kind == tNeq:
		op = "!="
		p.next()
	case p.isKeyword("contains"):
		op = "contains"
		p.next()
	case p.isKeyword("matches"):
		op = "matches"
		p.next()
	default:
		return nil, fmt.Errorf("expected operator (==, !=, contains, matches) after accessor %q, got %q",
			strings.Join(path, "."), p.peek().val)
	}

	// operand
	operandTok := p.peek()
	switch operandTok.kind {
	case tString:
		p.next()
		return cmpNode{path: path, op: op, operand: operandTok.val}, nil
	case tNumber:
		p.next()
		return cmpNode{path: path, op: op, operand: operandTok.val, isNumber: true}, nil
	default:
		return nil, fmt.Errorf("expected string or number operand, got %q", operandTok.val)
	}
}

func (p *exprParser) isKeyword(kw string) bool {
	t := p.peek()
	return t.kind == tIdent && t.val == kw
}

// ── AST + eval ──────────────────────────────────────────────────────────────

type exprNode interface {
	eval(EvalContext) (bool, error)
}

type orNode struct{ l, r exprNode }

func (n orNode) eval(ctx EvalContext) (bool, error) {
	lv, err := n.l.eval(ctx)
	if err != nil {
		return false, err
	}
	if lv {
		return true, nil
	}
	return n.r.eval(ctx)
}

type andNode struct{ l, r exprNode }

func (n andNode) eval(ctx EvalContext) (bool, error) {
	lv, err := n.l.eval(ctx)
	if err != nil {
		return false, err
	}
	if !lv {
		return false, nil
	}
	return n.r.eval(ctx)
}

type notNode struct{ x exprNode }

func (n notNode) eval(ctx EvalContext) (bool, error) {
	v, err := n.x.eval(ctx)
	if err != nil {
		return false, err
	}
	return !v, nil
}

type valueKind int

const (
	kString valueKind = iota
	kList
	kNumber
	kMissing
)

type resolvedValue struct {
	kind valueKind
	s    string
	list []string
	n    float64
}

type cmpNode struct {
	path     []string
	op       string
	operand  string
	isNumber bool
}

func (n cmpNode) eval(ctx EvalContext) (bool, error) {
	v, err := resolveAccessor(n.path, ctx)
	if err != nil {
		return false, err
	}

	switch n.op {
	case "matches":
		if v.kind == kList {
			return false, fmt.Errorf("'matches' is not supported on list %q", strings.Join(n.path, "."))
		}
		re, err := regexp.Compile(n.operand)
		if err != nil {
			return false, fmt.Errorf("invalid regex %q: %w", n.operand, err)
		}
		return re.MatchString(v.s), nil

	case "contains":
		switch v.kind {
		case kList:
			for _, item := range v.list {
				if item == n.operand {
					return true, nil
				}
			}
			return false, nil
		case kString:
			return strings.Contains(v.s, n.operand), nil
		default:
			return false, fmt.Errorf("'contains' is not supported on %q", strings.Join(n.path, "."))
		}

	case "==", "!=":
		eq, err := equals(v, n)
		if err != nil {
			return false, err
		}
		if n.op == "!=" {
			return !eq, nil
		}
		return eq, nil
	}
	return false, fmt.Errorf("unknown operator %q", n.op)
}

func equals(v resolvedValue, n cmpNode) (bool, error) {
	if v.kind == kList {
		return false, fmt.Errorf("'%s' is not supported on list %q (use 'contains')", n.op, strings.Join(n.path, "."))
	}
	if v.kind == kNumber && n.isNumber {
		want, err := strconv.ParseFloat(n.operand, 64)
		if err != nil {
			return false, fmt.Errorf("invalid number operand %q", n.operand)
		}
		return v.n == want, nil
	}
	// String comparison (covers kString and kMissing → "").
	return v.s == n.operand, nil
}

// resolveAccessor maps an accessor path to a value within the EvalContext.
func resolveAccessor(path []string, ctx EvalContext) (resolvedValue, error) {
	if len(path) == 0 {
		return resolvedValue{}, fmt.Errorf("empty accessor")
	}
	switch path[0] {
	case "cell":
		if len(path) != 2 {
			return resolvedValue{}, fmt.Errorf("invalid cell accessor %q", strings.Join(path, "."))
		}
		switch path[1] {
		case "labels":
			return resolvedValue{kind: kList, list: ctx.Cell.Labels}, nil
		case "priority":
			return resolvedValue{kind: kString, s: ctx.Cell.Priority}, nil
		case "type":
			return resolvedValue{kind: kString, s: ctx.Cell.Type}, nil
		case "title":
			return resolvedValue{kind: kString, s: ctx.Cell.Title}, nil
		case "source":
			return resolvedValue{kind: kString, s: ctx.Cell.SourceID}, nil
		case "state":
			return resolvedValue{kind: kString, s: ctx.Cell.State}, nil
		default:
			return resolvedValue{}, fmt.Errorf("unknown cell field %q", path[1])
		}
	case "event":
		if len(path) != 2 {
			return resolvedValue{}, fmt.Errorf("invalid event accessor %q", strings.Join(path, "."))
		}
		switch path[1] {
		case "kind", "body", "author", "author_association", "pr_number", "pr_url":
			v, ok := ctx.Event[path[1]]
			if !ok {
				return resolvedValue{kind: kMissing}, nil
			}
			return resolvedValue{kind: kString, s: v}, nil
		default:
			return resolvedValue{}, fmt.Errorf("unknown event field %q", path[1])
		}
	case "memory":
		if len(path) != 2 {
			return resolvedValue{}, fmt.Errorf("invalid memory accessor %q", strings.Join(path, "."))
		}
		v, ok := ctx.Memory[path[1]]
		if !ok {
			return resolvedValue{kind: kMissing}, nil
		}
		return resolvedValue{kind: kString, s: v}, nil
	case "steps":
		if len(path) != 3 {
			return resolvedValue{}, fmt.Errorf("invalid steps accessor %q (want steps.<id>.<field>)", strings.Join(path, "."))
		}
		st, ok := ctx.Steps[path[1]]
		if !ok {
			return resolvedValue{kind: kMissing}, nil
		}
		switch path[2] {
		case "state":
			return resolvedValue{kind: kString, s: st.State}, nil
		case "output":
			return resolvedValue{kind: kString, s: st.Output}, nil
		case "exit_code":
			return resolvedValue{kind: kNumber, n: float64(st.ExitCode)}, nil
		default:
			return resolvedValue{}, fmt.Errorf("unknown step field %q", path[2])
		}
	default:
		return resolvedValue{}, fmt.Errorf("unknown accessor root %q (want cell, memory, steps, or event)", path[0])
	}
}

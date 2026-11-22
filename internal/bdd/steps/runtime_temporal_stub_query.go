package steps

import (
	"fmt"
	"strings"
	"unicode/utf8"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
)

type workflowStatusName string

func (n workflowStatusName) executionStatus() enumspb.WorkflowExecutionStatus {
	switch n {
	case "Running":
		return enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING
	case "Completed":
		return enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED
	case "Failed":
		return enumspb.WORKFLOW_EXECUTION_STATUS_FAILED
	case "Canceled":
		return enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED
	case "Terminated":
		return enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED
	case "ContinuedAsNew":
		return enumspb.WORKFLOW_EXECUTION_STATUS_CONTINUED_AS_NEW
	case "TimedOut":
		return enumspb.WORKFLOW_EXECUTION_STATUS_TIMED_OUT
	default:
		return enumspb.WORKFLOW_EXECUTION_STATUS_UNSPECIFIED
	}
}

type listQueryFilter struct {
	workflowIDPrefix string
	statusNot        *workflowStatusName
	statusEq         *workflowStatusName
	statusIN         []workflowStatusName
	basamentoFlow    *string
}

type queryTokenKind int

const (
	tokEOF queryTokenKind = iota
	tokIdent
	tokString
	tokNEQ
	tokEQ
	tokLParen
	tokRParen
	tokComma
)

type queryToken struct {
	kind queryTokenKind
	text string
}

// lexQuery tokenizes a list query in one pass over runes.
func lexQuery(q string) ([]queryToken, error) {
	var out []queryToken
	for i := 0; i < len(q); {
		r, w := utf8.DecodeRuneInString(q[i:])
		if r == ' ' {
			i += w
			continue
		}
		switch r {
		case '"':
			val, ni, err := lexString(q, i)
			if err != nil {
				return nil, err
			}
			out = append(out, queryToken{kind: tokString, text: val})
			i = ni
		case '(':
			out = append(out, queryToken{kind: tokLParen})
			i += w
		case ')':
			out = append(out, queryToken{kind: tokRParen})
			i += w
		case ',':
			out = append(out, queryToken{kind: tokComma})
			i += w
		case '=':
			if i+1 < len(q) && q[i+1] == '=' {
				return nil, invalidChar('=')
			}
			out = append(out, queryToken{kind: tokEQ})
			i += w
		case '!':
			if i+1 >= len(q) || q[i+1] != '=' {
				return nil, invalidChar('!')
			}
			out = append(out, queryToken{kind: tokNEQ})
			i += 2
		default:
			if !isIdentStart(r) {
				return nil, invalidChar(r)
			}
			j := i + w
			for j < len(q) {
				r2, w2 := utf8.DecodeRuneInString(q[j:])
				if r2 == ' ' || r2 == '"' || r2 == '(' || r2 == ')' || r2 == ',' || r2 == '=' || r2 == '!' {
					break
				}
				if !isIdentPart(r2) {
					return nil, invalidChar(r2)
				}
				j += w2
			}
			out = append(out, queryToken{kind: tokIdent, text: q[i:j]})
			i = j
		}
	}
	out = append(out, queryToken{kind: tokEOF})
	return out, nil
}

func lexString(q string, start int) (string, int, error) {
	if q[start] != '"' {
		return "", start, invalidChar('"')
	}
	var b strings.Builder
	for i := start + 1; i < len(q); i++ {
		c := q[i]
		if c == '"' {
			return b.String(), i + 1, nil
		}
		if c == '\\' {
			return "", start, invalidChar('\\')
		}
		if c < 0x20 {
			return "", start, invalidChar(rune(c))
		}
		b.WriteByte(c)
	}
	return "", start, serviceerror.NewInvalidArgument("stub: unterminated string")
}

func isIdentStart(r rune) bool {
	return r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')
}

func isIdentPart(r rune) bool {
	return isIdentStart(r) || (r >= '0' && r <= '9')
}

func invalidChar(r rune) error {
	return serviceerror.NewInvalidArgument(fmt.Sprintf("stub: invalid query character %q", string(r)))
}

func invalidKeyword(got string) error {
	return serviceerror.NewInvalidArgument(fmt.Sprintf("stub: invalid query keyword %q", got))
}

func unexpectedToken(t queryToken) error {
	switch t.kind {
	case tokIdent:
		return serviceerror.NewInvalidArgument(fmt.Sprintf("stub: unexpected token %q", t.text))
	case tokString:
		return serviceerror.NewInvalidArgument("stub: unexpected quoted value")
	default:
		return serviceerror.NewInvalidArgument("stub: unexpected token")
	}
}

func parseWorkflowStatusName(s string) (workflowStatusName, error) {
	n := workflowStatusName(s)
	if n.executionStatus() == enumspb.WORKFLOW_EXECUTION_STATUS_UNSPECIFIED {
		return "", serviceerror.NewInvalidArgument(fmt.Sprintf("stub: unknown ExecutionStatus value %q", s))
	}
	return n, nil
}

// parseTemporalListQuery parses list queries the runtime emits.
func parseTemporalListQuery(q string, basamentoRegistered bool) (*listQueryFilter, error) {
	if strings.TrimSpace(q) == "" {
		return nil, serviceerror.NewInvalidArgument("stub: empty query")
	}
	toks, err := lexQuery(q)
	if err != nil {
		return nil, err
	}
	p := &queryParser{toks: toks}
	f, err := p.parsePrefix()
	if err != nil {
		return nil, err
	}
	for p.peek().kind != tokEOF {
		if err := p.wantIdent("AND"); err != nil {
			return nil, err
		}
		if p.peek().kind == tokEOF {
			return nil, serviceerror.NewInvalidArgument("stub: clause expected after AND")
		}
		if err := p.parseClause(f, basamentoRegistered); err != nil {
			return nil, err
		}
	}
	return f, nil
}

type queryParser struct {
	toks []queryToken
	i    int
}

func (p *queryParser) peek() queryToken { return p.toks[p.i] }
func (p *queryParser) next() queryToken {
	t := p.toks[p.i]
	if t.kind != tokEOF {
		p.i++
	}
	return t
}

func (p *queryParser) wantIdent(want string) error {
	t := p.peek()
	if t.kind != tokIdent {
		return unexpectedToken(t)
	}
	if t.text != want {
		if want == "AND" && t.text == "OR" {
			return serviceerror.NewInvalidArgument("stub: OR is not supported")
		}
		if t.text == "ORDER" {
			p.next()
			if p.peek().kind == tokIdent && p.peek().text == "BY" {
				return serviceerror.NewInvalidArgument("operation is not supported: 'ORDER BY' clause")
			}
		}
		return invalidKeyword(t.text)
	}
	p.next()
	return nil
}

func (p *queryParser) wantString() (string, error) {
	t := p.next()
	if t.kind != tokString {
		return "", unexpectedToken(t)
	}
	return t.text, nil
}

func (p *queryParser) parsePrefix() (*listQueryFilter, error) {
	if err := p.wantIdent("WorkflowId"); err != nil {
		if p.peek().kind == tokIdent && p.peek().text == "AND" {
			return nil, serviceerror.NewInvalidArgument("stub: prefix clause must be first")
		}
		return nil, err
	}
	if err := p.wantIdent("STARTS_WITH"); err != nil {
		return nil, err
	}
	prefix, err := p.wantString()
	if err != nil {
		return nil, err
	}
	if prefix == "" {
		return nil, serviceerror.NewInvalidArgument("stub: empty WorkflowId prefix")
	}
	return &listQueryFilter{workflowIDPrefix: prefix}, nil
}

func (p *queryParser) parseClause(f *listQueryFilter, basamentoRegistered bool) error {
	t := p.peek()
	if t.kind != tokIdent {
		return unexpectedToken(t)
	}
	switch t.text {
	case "ExecutionStatus":
		return p.parseStatusClause(f)
	case "BasamentoFlow":
		if f.basamentoFlow != nil {
			return serviceerror.NewInvalidArgument("stub: duplicate BasamentoFlow clause")
		}
		p.next()
		if p.peek().kind != tokEQ {
			return serviceerror.NewInvalidArgument("stub: expected = after BasamentoFlow")
		}
		p.next()
		if !basamentoRegistered {
			return serviceerror.NewInvalidArgument("stub: unknown attribute BasamentoFlow")
		}
		val, err := p.wantString()
		if err != nil {
			return err
		}
		f.basamentoFlow = &val
		return nil
	case "WorkflowId":
		return serviceerror.NewInvalidArgument("stub: duplicate WorkflowId clause")
	case "ORDER":
		p.next()
		if p.peek().kind == tokIdent && p.peek().text == "BY" {
			return serviceerror.NewInvalidArgument("operation is not supported: 'ORDER BY' clause")
		}
		return invalidKeyword("ORDER")
	default:
		return invalidKeyword(t.text)
	}
}

func (p *queryParser) parseStatusClause(f *listQueryFilter) error {
	p.next()
	t := p.peek()
	switch t.kind {
	case tokNEQ:
		if f.statusNot != nil {
			return serviceerror.NewInvalidArgument("stub: duplicate ExecutionStatus !=")
		}
		p.next()
		val, err := p.wantString()
		if err != nil {
			return err
		}
		st, err := parseWorkflowStatusName(val)
		if err != nil {
			return err
		}
		f.statusNot = &st
		return nil
	case tokEQ:
		if f.statusEq != nil {
			return serviceerror.NewInvalidArgument("stub: duplicate ExecutionStatus =")
		}
		p.next()
		val, err := p.wantString()
		if err != nil {
			return err
		}
		st, err := parseWorkflowStatusName(val)
		if err != nil {
			return err
		}
		f.statusEq = &st
		return nil
	case tokIdent:
		if t.text != "IN" {
			return invalidKeyword(t.text)
		}
		if f.statusIN != nil {
			return serviceerror.NewInvalidArgument("stub: duplicate ExecutionStatus IN")
		}
		p.next()
		if p.peek().kind != tokLParen {
			return serviceerror.NewInvalidArgument("stub: expected ( after IN")
		}
		p.next()
		var sts []workflowStatusName
		for {
			if p.peek().kind == tokRParen {
				if len(sts) == 0 {
					return serviceerror.NewInvalidArgument("stub: empty ExecutionStatus IN list")
				}
				p.next()
				f.statusIN = sts
				return nil
			}
			val, err := p.wantString()
			if err != nil {
				return err
			}
			st, err := parseWorkflowStatusName(val)
			if err != nil {
				return err
			}
			sts = append(sts, st)
			if p.peek().kind == tokComma {
				p.next()
				if p.peek().kind == tokRParen {
					return serviceerror.NewInvalidArgument("stub: trailing comma in IN list")
				}
				continue
			}
			if p.peek().kind != tokRParen {
				return serviceerror.NewInvalidArgument("stub: expected comma or ) in IN list")
			}
		}
	default:
		return serviceerror.NewInvalidArgument("stub: invalid ExecutionStatus clause")
	}
}

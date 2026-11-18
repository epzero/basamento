package expr

import (
	"fmt"
	"strings"

	"github.com/itchyny/gojq"
)

func scanBannedQuery(q *gojq.Query) (string, bool) {
	if q == nil {
		return "", false
	}
	if q.Left != nil {
		if n, ok := scanBannedQuery(q.Left); ok {
			return n, true
		}
	}
	if q.Right != nil {
		if n, ok := scanBannedQuery(q.Right); ok {
			return n, true
		}
	}
	for _, p := range q.Patterns {
		if n, ok := scanBannedPattern(p); ok {
			return n, true
		}
	}
	if q.Term != nil {
		return scanBannedTerm(q.Term)
	}
	return "", false
}

func scanBannedPattern(p *gojq.Pattern) (string, bool) {
	if p == nil {
		return "", false
	}
	if bannedNames[p.Name] {
		return strings.TrimPrefix(p.Name, "$"), true
	}
	for _, sub := range p.Array {
		if n, ok := scanBannedPattern(sub); ok {
			return n, true
		}
	}
	for _, o := range p.Object {
		if bannedNames[o.Key] {
			return strings.TrimPrefix(o.Key, "$"), true
		}
		if o.KeyString != nil {
			if n, ok := scanBannedString(o.KeyString); ok {
				return n, true
			}
		}
		if o.KeyQuery != nil {
			if n, ok := scanBannedQuery(o.KeyQuery); ok {
				return n, true
			}
		}
		if o.Val != nil {
			if n, ok := scanBannedPattern(o.Val); ok {
				return n, true
			}
		}
	}
	return "", false
}

func scanBannedTerm(t *gojq.Term) (string, bool) {
	if t == nil {
		return "", false
	}
	switch t.Type {
	case gojq.TermTypeFunc:
		if t.Func != nil {
			if bannedNames[t.Func.Name] {
				return strings.TrimPrefix(t.Func.Name, "$"), true
			}
			for _, arg := range t.Func.Args {
				if n, ok := scanBannedQuery(arg); ok {
					return n, true
				}
			}
		}
	case gojq.TermTypeObject:
		if t.Object != nil {
			for _, kv := range t.Object.KeyVals {
				if bannedNames[kv.Key] {
					return strings.TrimPrefix(kv.Key, "$"), true
				}
				if kv.KeyString != nil {
					if n, ok := scanBannedString(kv.KeyString); ok {
						return n, true
					}
				}
				if kv.KeyQuery != nil {
					if n, ok := scanBannedQuery(kv.KeyQuery); ok {
						return n, true
					}
				}
				if kv.Val != nil {
					if n, ok := scanBannedQuery(kv.Val); ok {
						return n, true
					}
				}
			}
		}
	case gojq.TermTypeArray:
		if t.Array != nil && t.Array.Query != nil {
			if n, ok := scanBannedQuery(t.Array.Query); ok {
				return n, true
			}
		}
	case gojq.TermTypeUnary:
		if t.Unary != nil && t.Unary.Term != nil {
			if n, ok := scanBannedTerm(t.Unary.Term); ok {
				return n, true
			}
		}
	case gojq.TermTypeString, gojq.TermTypeFormat:
		if t.Str != nil {
			if n, ok := scanBannedString(t.Str); ok {
				return n, true
			}
		}
	case gojq.TermTypeIf:
		if t.If != nil {
			if n, ok := scanBannedQuery(t.If.Cond); ok {
				return n, true
			}
			if n, ok := scanBannedQuery(t.If.Then); ok {
				return n, true
			}
			for _, elif := range t.If.Elif {
				if n, ok := scanBannedQuery(elif.Cond); ok {
					return n, true
				}
				if n, ok := scanBannedQuery(elif.Then); ok {
					return n, true
				}
			}
			if t.If.Else != nil {
				if n, ok := scanBannedQuery(t.If.Else); ok {
					return n, true
				}
			}
		}
	case gojq.TermTypeTry:
		if t.Try != nil {
			if n, ok := scanBannedQuery(t.Try.Body); ok {
				return n, true
			}
			if t.Try.Catch != nil {
				if n, ok := scanBannedQuery(t.Try.Catch); ok {
					return n, true
				}
			}
		}
	case gojq.TermTypeReduce:
		if t.Reduce != nil {
			if n, ok := scanBannedReduceForeach(t.Reduce.Query, t.Reduce.Patterns, t.Reduce.Start, t.Reduce.Update, nil); ok {
				return n, true
			}
		}
	case gojq.TermTypeForeach:
		if t.Foreach != nil {
			if n, ok := scanBannedReduceForeach(t.Foreach.Query, t.Foreach.Patterns, t.Foreach.Start, t.Foreach.Update, t.Foreach.Extract); ok {
				return n, true
			}
		}
	case gojq.TermTypeLabel:
		if t.Label != nil && t.Label.Body != nil {
			if n, ok := scanBannedQuery(t.Label.Body); ok {
				return n, true
			}
		}
	case gojq.TermTypeQuery:
		if t.Query != nil {
			if n, ok := scanBannedQuery(t.Query); ok {
				return n, true
			}
		}
	case gojq.TermTypeIndex:
		if t.Index != nil {
			if n, ok := scanBannedIndexFull(t.Index); ok {
				return n, true
			}
		}
	case gojq.TermTypeBreak:
		if bannedNames[t.Break] {
			return t.Break, true
		}
	case gojq.TermTypeIdentity, gojq.TermTypeRecurse, gojq.TermTypeNull,
		gojq.TermTypeTrue, gojq.TermTypeFalse, gojq.TermTypeNumber:
	default:
		return fmt.Sprintf("unsupported gojq term type %d", int(t.Type)), true
	}
	for _, s := range t.SuffixList {
		if n, ok := scanBannedSuffix(s); ok {
			return n, true
		}
	}
	return "", false
}

func scanBannedReduceForeach(query *gojq.Query, patterns []*gojq.Pattern, start, update, extract *gojq.Query) (string, bool) {
	if query != nil {
		if n, ok := scanBannedQuery(query); ok {
			return n, true
		}
	}
	for _, p := range patterns {
		if n, ok := scanBannedPattern(p); ok {
			return n, true
		}
	}
	if start != nil {
		if n, ok := scanBannedQuery(start); ok {
			return n, true
		}
	}
	if update != nil {
		if n, ok := scanBannedQuery(update); ok {
			return n, true
		}
	}
	if extract != nil {
		if n, ok := scanBannedQuery(extract); ok {
			return n, true
		}
	}
	return "", false
}

func scanBannedString(s *gojq.String) (string, bool) {
	if s == nil || s.Queries == nil {
		return "", false
	}
	for _, q := range s.Queries {
		if n, ok := scanBannedQuery(q); ok {
			return n, true
		}
	}
	return "", false
}

func scanBannedIndexFull(idx *gojq.Index) (string, bool) {
	if idx == nil {
		return "", false
	}
	if bannedNames[idx.Name] {
		return strings.TrimPrefix(idx.Name, "$"), true
	}
	if idx.Str != nil {
		if n, ok := scanBannedString(idx.Str); ok {
			return n, true
		}
	}
	if idx.Start != nil {
		if n, ok := scanBannedQuery(idx.Start); ok {
			return n, true
		}
	}
	if idx.End != nil {
		if n, ok := scanBannedQuery(idx.End); ok {
			return n, true
		}
	}
	return "", false
}

func scanBannedSuffix(s *gojq.Suffix) (string, bool) {
	if s == nil || s.Index == nil {
		return "", false
	}
	return scanBannedIndexFull(s.Index)
}
package prompt

import (
	"strings"
	"unicode/utf8"
)

// CodePointCount returns the number of Unicode code points in s.
func CodePointCount(s string) int {
	return utf8.RuneCountInString(s)
}

const (
	openDelim  = `<untrusted source="`
	closeDelim = `</untrusted>`
)

// WrapUntrusted neutralizes break-out sequences then wraps value for source name.
func WrapUntrusted(source, value string) string {
	inner := neutralizeBreakout(value)
	return openDelim + source + `">` + inner + closeDelim
}

func neutralizeBreakout(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		r, width := utf8.DecodeRuneInString(s[i:])
		if r != '<' {
			b.WriteString(s[i : i+width])
			i += width
			continue
		}
		if delimiterLikeAt(s, i) {
			b.WriteString("&lt;")
			i += width
			continue
		}
		b.WriteString(s[i : i+width])
		i += width
	}
	return b.String()
}

func delimiterLikeAt(s string, pos int) bool {
	if pos >= len(s) || s[pos] != '<' {
		return false
	}
	i := pos + 1
	i = skipASCIISpace(s, i)
	if i < len(s) && s[i] == '/' {
		i++
		i = skipASCIISpace(s, i)
	}
	return untrustedWordAt(s, i)
}

func skipASCIISpace(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i++
	}
	return i
}

func untrustedWordAt(s string, i int) bool {
	const word = "untrusted"
	if i+len(word) > len(s) {
		return false
	}
	for j := 0; j < len(word); j++ {
		if toLower(s[i+j]) != word[j] {
			return false
		}
	}
	if i+len(word) < len(s) {
		next := s[i+len(word)]
		if isNameChar(next) {
			return false
		}
	}
	return true
}

func isNameChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_' || b == '-'
}

func toLower(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b - 'A' + 'a'
	}
	return b
}

// CountUntrustedDelimiters counts opening and closing delimiters in rendered text
// (not neutralized sequences).
func CountUntrustedDelimiters(text string) (opens, closes int) {
	i := 0
	for i < len(text) {
		if strings.HasPrefix(text[i:], openDelim) {
			opens++
			i += len(openDelim)
			// skip to closing quote of source attribute
			if j := strings.IndexByte(text[i:], '"'); j >= 0 {
				i += j + 2 // quote and >
			} else {
				break
			}
			continue
		}
		if strings.HasPrefix(text[i:], closeDelim) {
			closes++
			i += len(closeDelim)
			continue
		}
		if strings.HasPrefix(text[i:], "&lt;") {
			i += 4
			continue
		}
		_, w := utf8.DecodeRuneInString(text[i:])
		i += w
	}
	return opens, closes
}

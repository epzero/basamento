package prompt

import (
	"strings"
	"unicode"
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

// WrapUntrusted neutralizes break-out sequences then wraps value.
// The source name is restricted to [A-Za-z0-9_.-]; other runes become '_'.
func WrapUntrusted(source, value string) string {
	source = sanitizeSource(source)
	inner := neutralizeBreakout(value)
	return openDelim + source + `">` + inner + closeDelim
}

func sanitizeSource(source string) string {
	if source == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(source))
	for _, r := range source {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '.' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
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
	i = skipUnicodeSpace(s, i)
	if i < len(s) && s[i] == '/' {
		i++
		i = skipUnicodeSpace(s, i)
	}
	if !untrustedWordAt(s, i) {
		return false
	}
	i += len("untrusted")
	i = skipUnicodeSpace(s, i)
	if i < len(s) && s[i] == '>' {
		return true
	}
	// Opening tag look-alike may continue with attributes or suffix without '>'.
	if i < len(s) && (s[i] == '/' || isNameChar(s[i])) {
		return true
	}
	return i >= len(s)
}

func skipUnicodeSpace(s string, i int) int {
	for i < len(s) {
		r, w := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && w == 1 {
			break
		}
		if unicode.IsSpace(r) || unicode.In(r, unicode.Cf) {
			i += w
			continue
		}
		break
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

func scanCanonicalOpen(text string, i int) (end int, ok bool) {
	if !strings.HasPrefix(text[i:], openDelim) {
		return 0, false
	}
	j := i + len(openDelim)
	quote := strings.IndexByte(text[j:], '"')
	if quote < 0 {
		return 0, false
	}
	if j+quote+1 >= len(text) || text[j+quote+1] != '>' {
		return 0, false
	}
	return j + quote + 2, true
}

func scanCanonicalClose(text string, i int) (end int, ok bool) {
	if !strings.HasPrefix(text[i:], closeDelim) {
		return 0, false
	}
	end = i + len(closeDelim)
	if end < len(text) && text[end] == '>' {
		return 0, false
	}
	return end, true
}

// CountUntrustedDelimiters counts canonical opening and closing delimiters.
func CountUntrustedDelimiters(text string) (opens, closes int) {
	i := 0
	for i < len(text) {
		if end, ok := scanCanonicalOpen(text, i); ok {
			opens++
			i = end
			continue
		}
		if end, ok := scanCanonicalClose(text, i); ok {
			closes++
			i = end
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

func hasUnpairedDelimiters(text string) bool {
	depth := 0
	pairs := 0
	i := 0
	for i < len(text) {
		if strings.HasPrefix(text[i:], "&lt;") {
			i += 4
			continue
		}
		if end, ok := scanCanonicalOpen(text, i); ok {
			if depth > 0 {
				return true
			}
			depth = 1
			i = end
			continue
		}
		if end, ok := scanCanonicalClose(text, i); ok {
			if depth == 0 {
				return true
			}
			depth = 0
			pairs++
			if pairs > 1 {
				return true
			}
			i = end
			continue
		}
		if delimiterLikeAt(text, i) {
			return true
		}
		_, w := utf8.DecodeRuneInString(text[i:])
		i += w
	}
	return depth != 0
}

package prompt

import (
	"strings"
	"unicode/utf8"
)

// DelimiterRecord is one assembler-written untrusted wrapper in a text buffer.
// Byte offsets name the opening delimiter from '<' through '>', and the closing
// '</untrusted>' through its last byte.
type DelimiterRecord struct {
	OpenStart, OpenEnd, CloseStart, CloseEnd int
}

func delimiterRecordInWrapped(wrapped string, base int) (DelimiterRecord, bool) {
	openEnd, ok := scanCanonicalOpen(wrapped, 0)
	if !ok {
		return DelimiterRecord{}, false
	}
	rest := wrapped[openEnd:]
	closeRel := strings.Index(rest, closeDelim)
	if closeRel < 0 {
		return DelimiterRecord{}, false
	}
	closeStart := openEnd + closeRel
	rec := DelimiterRecord{
		OpenStart:  base,
		OpenEnd:    base + openEnd,
		CloseStart: base + closeStart,
		CloseEnd:   base + closeStart + len(closeDelim),
	}
	return rec, true
}

func recordMatchesText(text string, r DelimiterRecord) bool {
	if r.OpenStart < 0 || r.OpenEnd > len(text) || r.CloseStart < r.OpenEnd || r.CloseEnd > len(text) {
		return false
	}
	end, ok := scanCanonicalOpen(text, r.OpenStart)
	if !ok || end != r.OpenEnd {
		return false
	}
	closeEnd, ok := scanCanonicalClose(text, r.CloseStart)
	if !ok || closeEnd != r.CloseEnd {
		return false
	}
	return true
}

func shiftDelimiterRecords(recs []DelimiterRecord, shift int) []DelimiterRecord {
	if len(recs) == 0 || shift == 0 {
		return recs
	}
	out := make([]DelimiterRecord, len(recs))
	for i, r := range recs {
		out[i] = DelimiterRecord{
			OpenStart:  r.OpenStart + shift,
			OpenEnd:    r.OpenEnd + shift,
			CloseStart: r.CloseStart + shift,
			CloseEnd:   r.CloseEnd + shift,
		}
	}
	return out
}

func hasLookAlikeIn(text string, from, to int) bool {
	if from > to {
		from, to = to, from
	}
	if from < 0 {
		from = 0
	}
	if to > len(text) {
		to = len(text)
	}
	i := from
	for i < to {
		if delimiterLikeAt(text, i) {
			return true
		}
		_, w := utf8.DecodeRuneInString(text[i:])
		i += w
	}
	return false
}

func textHasDelimiterIssue(text string) bool {
	i := 0
	for i < len(text) {
		if strings.HasPrefix(text[i:], "&lt;") {
			i += 4
			continue
		}
		if _, ok := scanCanonicalOpen(text, i); ok {
			return true
		}
		if _, ok := scanCanonicalClose(text, i); ok {
			return true
		}
		if delimiterLikeAt(text, i) {
			return true
		}
		_, w := utf8.DecodeRuneInString(text[i:])
		i += w
	}
	return false
}

func lintTextWithRecords(text string, records []DelimiterRecord) bool {
	for _, r := range records {
		if !recordMatchesText(text, r) {
			return true
		}
	}
	if len(records) == 0 {
		return textHasDelimiterIssue(text)
	}
	used := make([]bool, len(records))
	openAt := func(pos int) int {
		for i, r := range records {
			if r.OpenStart == pos {
				return i
			}
		}
		return -1
	}
	closeAt := func(pos int) int {
		for i, r := range records {
			if r.CloseStart == pos {
				return i
			}
		}
		return -1
	}
	depth := 0
	var active int
	i := 0
	for i < len(text) {
		if strings.HasPrefix(text[i:], "&lt;") {
			i += 4
			continue
		}
		if end, ok := scanCanonicalOpen(text, i); ok {
			ri := openAt(i)
			if ri < 0 || used[ri] {
				return true
			}
			if depth > 0 {
				return true
			}
			used[ri] = true
			active = ri
			depth = 1
			if hasLookAlikeIn(text, end, records[ri].CloseStart) {
				return true
			}
			i = end
			continue
		}
		if end, ok := scanCanonicalClose(text, i); ok {
			ri := closeAt(i)
			if ri < 0 || !used[ri] || ri != active {
				return true
			}
			depth = 0
			active = -1
			i = end
			continue
		}
		if delimiterLikeAt(text, i) {
			return true
		}
		_, w := utf8.DecodeRuneInString(text[i:])
		i += w
	}
	if depth != 0 {
		return true
	}
	for _, ok := range used {
		if !ok {
			return true
		}
	}
	return false
}

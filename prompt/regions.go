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

// countUnpairedDelimiters counts untrusted-content delimiters in text that are
// not at a recorded pair's spans. A record counts only when both of its halves
// are at their spans; otherwise the delimiters in the text are unpaired.
func countUnpairedDelimiters(text string, records []DelimiterRecord) int {
	openEnds := map[int]int{}
	closeEnds := map[int]int{}
	for _, r := range records {
		if !recordMatchesText(text, r) {
			continue
		}
		openEnds[r.OpenStart] = r.OpenEnd
		closeEnds[r.CloseStart] = r.CloseEnd
	}
	n := 0
	i := 0
	for i < len(text) {
		if strings.HasPrefix(text[i:], "&lt;") {
			i += 4
			continue
		}
		if end, ok := scanCanonicalOpen(text, i); ok {
			if recordedEnd, ok := openEnds[i]; ok && recordedEnd == end {
				i = end
				continue
			}
			n++
			i = end
			continue
		}
		if end, ok := scanCanonicalClose(text, i); ok {
			if recordedEnd, ok := closeEnds[i]; ok && recordedEnd == end {
				i = end
				continue
			}
			n++
			i = end
			continue
		}
		if delimiterLikeAt(text, i) {
			n++
			i++
			continue
		}
		_, w := utf8.DecodeRuneInString(text[i:])
		i += w
	}
	return n
}

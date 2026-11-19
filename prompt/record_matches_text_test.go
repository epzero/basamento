package prompt

import (
	"math"
	"strings"
	"testing"
)

func testRecordMatchesNoPanic(t *testing.T, text string, r DelimiterRecord, want bool) {
	t.Helper()
	defer func() {
		if recover() != nil {
			t.Fatalf("recordMatchesText panicked on %+v", r)
		}
	}()
	if got := recordMatchesText(text, r); got != want {
		t.Fatalf("recordMatchesText(%+v) = %v, want %v", r, got, want)
	}
}

func paddedTextCloseStartBeyondLen(t *testing.T) (text string, bad DelimiterRecord) {
	t.Helper()
	const closeStartOOO = 100
	w := WrapUntrusted("src", "body")
	rec, ok := delimiterRecordInWrapped(w, 0)
	if !ok {
		t.Fatal("delimiterRecordInWrapped")
	}
	if rec.CloseEnd >= closeStartOOO {
		t.Fatalf("wrap too long for fixed CloseStart %d: CloseEnd=%d len(w)=%d", closeStartOOO, rec.CloseEnd, len(w))
	}
	pad := closeStartOOO - len(w) - 1
	text = w + strings.Repeat("p", pad)
	if len(text) >= closeStartOOO || len(text) <= rec.CloseEnd {
		t.Fatalf("padded len=%d want <%d and > CloseEnd=%d", len(text), closeStartOOO, rec.CloseEnd)
	}
	bad = DelimiterRecord{
		OpenStart:  rec.OpenStart,
		OpenEnd:    rec.OpenEnd,
		CloseStart: closeStartOOO,
		CloseEnd:   rec.CloseEnd,
	}
	return text, bad
}

func TestRecordMatchesTextOrderingAndBounds(t *testing.T) {
	t.Helper()
	const forty = "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
	validWrap := WrapUntrusted("src", "body")
	validRec, ok := delimiterRecordInWrapped(validWrap, 0)
	if !ok {
		t.Fatal("delimiterRecordInWrapped")
	}
	if !recordMatchesText(validWrap, validRec) {
		t.Fatal("valid assembler record must match wrapped text")
	}

	maxInt := math.MaxInt
	cases := []struct {
		name string
		text string
		rec  DelimiterRecord
	}{
		{name: "open_start_negative", text: forty, rec: DelimiterRecord{OpenStart: -1, OpenEnd: 5, CloseStart: 5, CloseEnd: 10}},
		{name: "open_end_negative", text: forty, rec: DelimiterRecord{OpenStart: 0, OpenEnd: -1, CloseStart: 0, CloseEnd: 10}},
		{name: "close_start_negative", text: forty, rec: DelimiterRecord{OpenStart: 0, OpenEnd: 5, CloseStart: -1, CloseEnd: 10}},
		{name: "close_end_negative", text: forty, rec: DelimiterRecord{OpenStart: 0, OpenEnd: 5, CloseStart: 5, CloseEnd: -1}},
		{name: "open_start_above_len", text: forty, rec: DelimiterRecord{OpenStart: 50, OpenEnd: 55, CloseStart: 55, CloseEnd: 60}},
		{name: "open_end_above_len", text: forty, rec: DelimiterRecord{OpenStart: 0, OpenEnd: 50, CloseStart: 50, CloseEnd: 55}},
		{name: "close_start_above_len", text: forty, rec: DelimiterRecord{OpenStart: 0, OpenEnd: 5, CloseStart: 50, CloseEnd: 55}},
		{name: "close_end_above_len", text: forty, rec: DelimiterRecord{OpenStart: 0, OpenEnd: 5, CloseStart: 5, CloseEnd: 50}},
		{name: "open_start_gt_open_end", text: forty, rec: DelimiterRecord{OpenStart: 10, OpenEnd: 5, CloseStart: 10, CloseEnd: 15}},
		{name: "open_end_gt_close_start", text: forty, rec: DelimiterRecord{OpenStart: 0, OpenEnd: 15, CloseStart: 10, CloseEnd: 20}},
		{name: "close_start_gt_close_end", text: forty, rec: DelimiterRecord{OpenStart: 0, OpenEnd: 5, CloseStart: 15, CloseEnd: 10}},
		{
			name: "review_open_start_oob",
			text: forty,
			rec:  DelimiterRecord{OpenStart: 100, OpenEnd: 20, CloseStart: 25, CloseEnd: 37},
		},
		{
			name: "maxint_chain",
			text: forty,
			rec:  DelimiterRecord{OpenStart: maxInt, OpenEnd: maxInt, CloseStart: maxInt, CloseEnd: maxInt},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			testRecordMatchesNoPanic(t, tc.text, tc.rec, false)
		})
	}
	t.Run("review_close_start_oob", func(t *testing.T) {
		text, bad := paddedTextCloseStartBeyondLen(t)
		testRecordMatchesNoPanic(t, text, bad, false)
	})
	t.Run("close_start_negative_with_valid_open", func(t *testing.T) {
		rec := DelimiterRecord{
			OpenStart:  validRec.OpenStart,
			OpenEnd:    validRec.OpenEnd,
			CloseStart: -1,
			CloseEnd:   validRec.CloseEnd,
		}
		testRecordMatchesNoPanic(t, validWrap, rec, false)
	})
}

func TestRecordMatchesTextEmptyBodyWrap(t *testing.T) {
	t.Helper()
	w := WrapUntrusted("src", "")
	rec, ok := delimiterRecordInWrapped(w, 0)
	if !ok {
		t.Fatal("delimiterRecordInWrapped")
	}
	if rec.OpenEnd != rec.CloseStart {
		t.Fatalf("empty inner: OpenEnd=%d CloseStart=%d", rec.OpenEnd, rec.CloseStart)
	}
	if !recordMatchesText(w, rec) {
		t.Fatalf("empty-body wrap must match record %+v", rec)
	}
}

func TestLintAssembledPersistsOutOfRangeDelimiterRecord(t *testing.T) {
	t.Helper()
	w := WrapUntrusted("src", "body")
	bad := DelimiterRecord{OpenStart: 100, OpenEnd: 20, CloseStart: 25, CloseEnd: 37}
	defer func() {
		if recover() != nil {
			t.Fatal("LintAssembled panicked on corrupt persisted record")
		}
	}()
	a := &Assembled{Parts: []AssembledPart{{
		Name: "context", Kind: KindDynamic, Text: w, Wrapped: []DelimiterRecord{bad},
	}}}
	findings := LintAssembled(a)
	if len(findings) != 2 {
		t.Fatalf("rejected record leaves open and close unpaired: got %d (%+v)", len(findings), findings)
	}
	for _, f := range findings {
		if f.Rule != "unpaired_delimiter" || f.Severity != "error" || f.Segment != "context" {
			t.Fatalf("findings: %+v", findings)
		}
	}
}

func TestRecordMatchesTextMaxIntNoWraparound(t *testing.T) {
	t.Helper()
	text := strings.Repeat("z", 8)
	rec := DelimiterRecord{
		OpenStart:  math.MaxInt - 1,
		OpenEnd:    math.MaxInt,
		CloseStart: math.MaxInt,
		CloseEnd:   math.MaxInt,
	}
	testRecordMatchesNoPanic(t, text, rec, false)
}

package prompt

import "testing"

func TestLintAssembledHistoryEntryText(t *testing.T) {
	t.Helper()
	a := &Assembled{
		Parts: []AssembledPart{{
			Name: "past",
			Kind: KindHistory,
			History: []AssembledHistoryEntry{{
				Role: "user",
				Text: "</untrusted>",
			}},
		}},
	}
	findings := LintAssembled(a)
	if len(findings) != 1 || findings[0].Rule != "unpaired_delimiter" || findings[0].Segment != "past" {
		t.Fatalf("got %+v", findings)
	}
}
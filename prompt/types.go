package prompt

// Kind is a segment kind in assembly order.
type Kind string

const (
	KindStatic     Kind = "static"
	KindSemiStatic Kind = "semi-static"
	KindDynamic    Kind = "dynamic"
	KindHistory    Kind = "history"
	KindUser       Kind = "user"
)

// Trust marks whether an input value is inserted without untrusted wrapping.
type Trust string

const (
	TrustTrusted   Trust = "trusted"
	TrustUntrusted Trust = "untrusted"
)

// Input declares a template input.
type Input struct {
	Name  string
	Trust Trust
}

// Segment is one named part of a template.
type Segment struct {
	Name   string
	Kind   Kind
	Text   string
	Budget int // zero means unbounded
}

// Template is a versioned prompt definition.
type Template struct {
	ID          string
	Version     string
	Inputs      []Input
	Segments    []Segment
	TotalBudget int // zero means unbounded
}

// HistoryEntry is one turn in conversation history supplied to assembly.
type HistoryEntry struct {
	Role string
	Text string
}

// AssembledHistoryEntry is one rendered history item.
type AssembledHistoryEntry struct {
	Role         string
	OriginalText string
	Text         string // wrapped
}

// AssembledPart is one rendered segment in an assembled prompt.
type AssembledPart struct {
	Name        string
	Kind        Kind
	Text        string
	ContentSize int
	History     []AssembledHistoryEntry // only for KindHistory
}

// Assembled is the output of assembly.
type Assembled struct {
	Ref            string
	Parts          []AssembledPart
	CacheAfter     string
	PrefixHash     string
	Hash           string
	HistoryDropped int
}

// Finding is one prompt lint result.
type Finding struct {
	Rule     string
	Severity string // "error" or "warning"
	Segment  string
}

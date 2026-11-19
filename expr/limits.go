package expr

const (
	defaultCostLimit   = uint64(1_000_000)
	defaultStepLimit   = uint64(1_000_000)
	defaultOutputLimit = 1 << 20 // 1 MiB, the RFC 8785 size of a mapping's output
)

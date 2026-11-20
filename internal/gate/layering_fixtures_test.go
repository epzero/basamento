package gate

// layeringExcludedBuildTag is a constraint no target GOOS/GOARCH satisfies; go list
// leaves matching sources off active lists (IgnoredGoFiles) while the layering
// filesystem walk still parses and checks them — unlike //go:build windows, which
// would change go list attribution when GOOS=windows in child processes.
const layeringExcludedBuildTag = "layering_fixture_never"

func layeringExcludedGoFile(body string) string {
	return "//go:build " + layeringExcludedBuildTag + "\n\n" + body
}
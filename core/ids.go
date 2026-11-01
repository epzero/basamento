package core

import (
	"crypto/rand"
	"fmt"
	"io"
	"regexp"
	"sync"

	"github.com/oklog/ulid/v2"
)

// Ids issues entity identifiers of the form "<kind>_<26 Crockford base32 chars>".
//
// Identifiers are unique across process restarts and sort lexicographically by
// creation time (REQ-CORE-001, REQ-CORE-002). Production code obtains an Ids from
// the composition root; deterministic fakes live in core/coretest and are
// importable from tests only (enforced by `gate layering`).
type Ids interface {
	New(kind string) (string, error)
}

var kindPattern = regexp.MustCompile(`^[a-z][a-z0-9]{0,15}$`)

// ValidKind reports whether kind may prefix an identifier.
func ValidKind(kind string) bool { return kindPattern.MatchString(kind) }

// ULIDs is the production Ids implementation: ULIDs with cryptographic entropy,
// monotonic within one millisecond in one process.
type ULIDs struct {
	clock   Clock
	mu      sync.Mutex
	entropy *ulid.MonotonicEntropy
}

// NewULIDs returns a ULID generator. A nil entropy source uses crypto/rand.
func NewULIDs(clock Clock, entropy io.Reader) *ULIDs {
	if entropy == nil {
		entropy = rand.Reader
	}
	return &ULIDs{clock: clock, entropy: ulid.Monotonic(entropy, 0)}
}

// New returns a fresh identifier for kind.
func (g *ULIDs) New(kind string) (string, error) {
	if !ValidKind(kind) {
		return "", NewError(CodeInvalidArgument, "invalid id kind", map[string]any{"kind": kind})
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	id, err := ulid.New(ulid.Timestamp(g.clock.Now()), g.entropy)
	if err != nil {
		return "", fmt.Errorf("core: generate ulid: %w", err)
	}
	return kind + "_" + id.String(), nil
}

package gate

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// Agent roles. One session holds exactly one role.
const (
	RoleSpecAuthor   = "spec-author"
	RoleSpecReviewer = "spec-reviewer"
	RoleImplementer  = "implementer"
	RoleCodeReviewer = "code-reviewer"
	RoleDispatcher   = "dispatcher"
	RoleArbiter      = "arbiter" // settles what an author and a reviewer could not
)

var roles = map[string]bool{RoleSpecAuthor: true, RoleSpecReviewer: true, RoleImplementer: true, RoleCodeReviewer: true, RoleDispatcher: true, RoleArbiter: true}

var (
	agentID = regexp.MustCompile(`^([a-z0-9][a-z0-9-]{1,23}):([a-z-]+):([a-z0-9]{6})$`)
	humanID = regexp.MustCompile(`^human:([A-Za-z0-9][A-Za-z0-9-]{0,38})$`)
)

// Identity names who acted: an agent session or a person.
//
//	<harness>:<role>:<instance>   e.g. claude-code:implementer:k3f9a2
//	human:<github-login>          e.g. human:ana
//
// harness is the tool hosting the session (claude-code, cursor, codex, …),
// never a model name. instance is six random lowercase hex/alnum characters
// issued once per session by `gate agent new`.
type Identity struct {
	Harness  string
	Role     string
	Instance string // for people: the login
}

// Human reports whether the identity is a person.
func (i Identity) Human() bool { return i.Harness == "human" }

func (i Identity) String() string {
	if i.Human() {
		return "human:" + i.Instance
	}
	return i.Harness + ":" + i.Role + ":" + i.Instance
}

// ParseIdentity validates an identity string.
func ParseIdentity(s string) (Identity, error) {
	if m := humanID.FindStringSubmatch(s); m != nil {
		return Identity{Harness: "human", Instance: m[1]}, nil
	}
	m := agentID.FindStringSubmatch(s)
	if m == nil {
		return Identity{}, fmt.Errorf("%q is not <harness>:<role>:<6-char instance> or human:<login>", s)
	}
	if m[1] == "human" {
		return Identity{}, fmt.Errorf("%q: harness may not be \"human\"", s)
	}
	if !roles[m[2]] {
		return Identity{}, fmt.Errorf("%q: unknown role %q", s, m[2])
	}
	return Identity{Harness: m[1], Role: m[2], Instance: m[3]}, nil
}

// NewIdentity issues a fresh identity for a session.
func NewIdentity(harness, role string) (Identity, error) {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		return Identity{}, err
	}
	id := Identity{Harness: strings.ToLower(harness), Role: role, Instance: hex.EncodeToString(b)}
	if _, err := ParseIdentity(id.String()); err != nil {
		return Identity{}, err
	}
	return id, nil
}

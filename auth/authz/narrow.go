package authz

import "strings"

const sessionScopePrefix = "session:"

type narrowOutcome int

const (
	narrowNone narrowOutcome = iota
	narrowApplied
	narrowUnrecognized
)

// narrowResult records how scopes changed the permission set (REQ-AUTH-030).
type narrowResult struct {
	perms   PermissionSet
	outcome narrowOutcome
}

// narrowScopes applies principal scope narrowing (REQ-AUTH-018, REQ-AUTH-030).
func narrowScopes(perms PermissionSet, scopes []string, resourceSession string) narrowResult {
	if len(scopes) == 0 {
		return narrowResult{perms: perms.clone(), outcome: narrowNone}
	}
	ids := recognizedSessionScopes(scopes)
	if len(ids) == 0 {
		return narrowResult{perms: nil, outcome: narrowUnrecognized}
	}
	for _, id := range ids {
		if id == resourceSession {
			return narrowResult{perms: perms.clone(), outcome: narrowApplied}
		}
	}
	return narrowResult{perms: nil, outcome: narrowApplied}
}

func recognizedSessionScopes(scopes []string) []string {
	var out []string
	for _, s := range scopes {
		id, ok := strings.CutPrefix(s, sessionScopePrefix)
		if !ok || id == "" {
			continue
		}
		out = append(out, id)
	}
	return out
}

func denyReasonForScopes(scopes []string, beforeNarrow PermissionSet, action string, condReason string, nr narrowResult) string {
	if len(scopes) == 0 {
		if condReason != "" {
			return condReason
		}
		return ReasonNoBinding
	}
	if nr.outcome == narrowUnrecognized {
		return ReasonScopeNarrowed
	}
	if beforeNarrow.Has(action) {
		return ReasonScopeNarrowed
	}
	if condReason != "" {
		return condReason
	}
	return ReasonNoBinding
}

package authz

import "testing"

func TestNarrowScopesMatrix(t *testing.T) {
	member := rolePerms(RoleMember)
	viewer := rolePerms(RoleViewer)
	cases := []struct {
		name    string
		scopes  []string
		session string
		perms   PermissionSet
		action  string
		allow   bool
		reason  string
	}{
		{"nil scopes", nil, "s1", member, "run.cancel", true, ""},
		{"empty scopes", []string{}, "s1", member, "run.cancel", true, ""},
		{"match s1", []string{"session:s1"}, "s1", member, "run.cancel", true, ""},
		{"mismatch s2", []string{"session:s1"}, "s2", member, "run.cancel", false, ReasonScopeNarrowed},
		{"empty session", []string{"session:s1"}, "", member, "run.cancel", false, ReasonScopeNarrowed},
		{"union s2", []string{"session:s1", "session:s2"}, "s2", member, "run.cancel", true, ""},
		{"openid side s1", []string{"session:s1", "openid"}, "s1", member, "run.cancel", true, ""},
		{"openid side s3", []string{"session:s1", "openid"}, "s3", member, "run.cancel", false, ReasonScopeNarrowed},
		{"only openid", []string{"openid", "profile"}, "s1", member, "run.cancel", false, ReasonScopeNarrowed},
		{"session empty id", []string{"session:"}, "", member, "run.cancel", false, ReasonScopeNarrowed},
		{"wrong case", []string{"Session:s1"}, "s1", member, "run.cancel", false, ReasonScopeNarrowed},
		{"viewer cancel denied", []string{"session:s1"}, "s1", viewer, "run.cancel", false, ReasonNoBinding},
		{"viewer get ok", []string{"session:s1"}, "s1", viewer, "run.get", true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := tc.perms.clone()
			nr := narrowScopes(tc.perms, tc.scopes, tc.session)
			got := nr.perms.Has(tc.action)
			if got != tc.allow {
				t.Fatalf("allow %v want %v", got, tc.allow)
			}
			if !tc.allow && tc.reason != "" {
				r := denyReasonForScopes(tc.scopes, before, tc.action, "", nr)
				if r != tc.reason {
					t.Fatalf("reason %q want %q", r, tc.reason)
				}
			}
		})
	}
}

func TestNarrowUnrecognizedNoBindingReason(t *testing.T) {
	nr := narrowScopes(nil, []string{"openid"}, "s1")
	r := denyReasonForScopes([]string{"openid"}, nil, "run.get", "", nr)
	if r != ReasonScopeNarrowed {
		t.Fatalf("got %q", r)
	}
	nr2 := narrowScopes(nil, []string{"session:s1"}, "s1")
	r2 := denyReasonForScopes([]string{"session:s1"}, nil, "run.get", "", nr2)
	if r2 != ReasonNoBinding {
		t.Fatalf("got %q", r2)
	}
}

func TestWorstConditionReasonPriority(t *testing.T) {
	if got := worstConditionReason(ReasonConditionError, ReasonConditionCostExceed); got != ReasonConditionCostExceed {
		t.Fatalf("got %q", got)
	}
	if got := worstConditionReason(ReasonNoBinding, ReasonConditionError); got != ReasonConditionError {
		t.Fatalf("got %q", got)
	}
}

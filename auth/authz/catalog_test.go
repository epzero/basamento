package authz_test

import (
	"slices"
	"testing"

	"github.com/epzero/basamento/auth/authz"
)

func TestCatalogSize(t *testing.T) {
	actions := authz.Actions()
	if len(actions) != 29 {
		t.Fatalf("catalog has %d actions, want 29", len(actions))
	}
}

func TestRoleMembershipExact(t *testing.T) {
	wantViewer := []string{
		"run.list", "run.get", "invocation.get", "invocation.events",
		"stream.attach", "stream.reattach", "stream.detach",
		"browser.session.read", "pool.list", "worker.list", "binding.list",
	}
	wantMemberExtra := []string{
		"run.start", "run.cancel", "gate.resolve", "conversation.send",
		"invocation.start", "invocation.cancel",
		"browser.session.start", "browser.session.cancel",
		"pool.join", "pool.leave",
	}
	wantAdminExtra := []string{
		"binding.grant", "binding.revoke",
		"credential.names", "credential.put", "credential.delete", "credential.resolve",
		"worker.remove", "audit.read",
	}
	assertPermSet(t, authz.RoleViewer, wantViewer)
	member := append(slices.Clone(wantViewer), wantMemberExtra...)
	assertPermSet(t, authz.RoleMember, member)
	admin := append(slices.Clone(member), wantAdminExtra...)
	assertPermSet(t, authz.RoleAdmin, admin)
}

func assertPermSet(t *testing.T, role authz.Role, want []string) {
	t.Helper()
	got := role.Permissions()
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("%s permissions:\n got %v\nwant %v", role, got, want)
	}
}

func TestRoleSubsetChain(t *testing.T) {
	v := toSet(authz.RoleViewer.Permissions())
	m := toSet(authz.RoleMember.Permissions())
	a := toSet(authz.RoleAdmin.Permissions())
	if !subset(v, m) || !subset(m, a) || !subset(v, a) {
		t.Fatal("viewer ⊂ member ⊂ admin")
	}
}

func toSet(perms []string) map[string]struct{} {
	out := make(map[string]struct{}, len(perms))
	for _, p := range perms {
		out[p] = struct{}{}
	}
	return out
}

func subset(a, b map[string]struct{}) bool {
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}

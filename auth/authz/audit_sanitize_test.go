package authz_test

import (
	"strings"
	"testing"

	"github.com/epzero/basamento/auth/authn"
	"github.com/epzero/basamento/auth/authz"
	"github.com/epzero/basamento/core"
)

func TestAuditSanitizePrincipalInDenial(t *testing.T) {
	h := newHarness(t)
	tenant := "acme"
	ctx := tenantCtx(t, tenant)
	mustDeny(t, h, ctx, authn.Principal{ID: "evil\nid", Tenant: core.TenantID(tenant)}, "run.get", authz.Resource{Kind: "run", ID: "r1"})
	events, n := h.audit.snap()
	if n != 1 || events[0].Principal != "[invalid]" {
		t.Fatalf("event %+v", events[0])
	}
}

func TestAuditSanitizeResourceInDenial(t *testing.T) {
	cases := []struct {
		name    string
		id      string
		wantRes string
	}{
		{name: "clean", id: "r1", wantRes: "run:r1"},
		{name: "1mib", id: strings.Repeat("x", 1<<20), wantRes: "run:" + strings.Repeat("x", 256)},
		{name: "newline", id: "a\nb", wantRes: "run:[invalid]"},
		{name: "badutf8", id: string([]byte{0xff, 0xfe}), wantRes: "run:[invalid]"},
		{name: "256bytes", id: strings.Repeat("b", 256), wantRes: "run:" + strings.Repeat("b", 256)},
		{name: "257bytes", id: strings.Repeat("b", 257), wantRes: "run:" + strings.Repeat("b", 256)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			tenant := "acme"
			ctx := tenantCtx(t, tenant)
			mustDeny(t, h, ctx, principal("bob", tenant), "run.get", authz.Resource{Kind: "run", ID: tc.id})
			events, n := h.audit.snap()
			if n != 1 {
				t.Fatalf("audit calls %d", n)
			}
			if events[0].Resource != tc.wantRes || events[0].Principal != "bob" {
				t.Fatalf("event %+v", events[0])
			}
		})
	}
}

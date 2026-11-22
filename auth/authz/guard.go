package authz

import (
	"context"
	"errors"
	"fmt"

	"github.com/epzero/basamento/auth/authn"
	"github.com/epzero/basamento/core"
)

// Resolver builds the Resource for one RPC from its request payload.
type Resolver func(ctx context.Context, request any) (Resource, error)

// Declaration wires one RPC procedure to a catalog action and resource resolver.
type Declaration struct {
	Procedure string
	Action    string
	Resolve   Resolver
}

// Guard refuses unknown procedures at wiring time and authorizes declared ones.
type Guard struct {
	auth  *Authorizer
	byRPC map[string]Declaration
}

// NewGuard validates declarations and returns a Guard.
func NewGuard(auth *Authorizer, decls ...Declaration) (*Guard, error) {
	if auth == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "authorizer is required", nil)
	}
	by := make(map[string]Declaration, len(decls))
	for _, d := range decls {
		if d.Procedure == "" {
			return nil, core.NewError(core.CodeInvalidArgument, "procedure name is empty", nil)
		}
		if _, dup := by[d.Procedure]; dup {
			return nil, core.NewError(core.CodeInvalidArgument, fmt.Sprintf("procedure %q: declared twice", d.Procedure), nil)
		}
		if d.Action == "" {
			return nil, core.NewError(core.CodeInvalidArgument, fmt.Sprintf("procedure %q: action is empty", d.Procedure), nil)
		}
		if !catalogHas(d.Action) {
			return nil, core.NewError(core.CodeInvalidArgument, fmt.Sprintf("procedure %q: action is not in the catalog", d.Procedure), nil)
		}
		if d.Resolve == nil {
			return nil, core.NewError(core.CodeInvalidArgument, fmt.Sprintf("procedure %q: resolver is missing", d.Procedure), nil)
		}
		by[d.Procedure] = d
	}
	return &Guard{auth: auth, byRPC: by}, nil
}

// Authorize resolves the request and runs Check for a declared procedure.
// An undeclared procedure returns unsupported and never touches the store or audit.
func (g *Guard) Authorize(ctx context.Context, principal authn.Principal, procedure string, request any) error {
	d, ok := g.byRPC[procedure]
	if !ok {
		return core.NewError(core.CodeUnsupported, "procedure is not served", nil)
	}
	res, err := d.Resolve(ctx, request)
	if err != nil {
		var ce *core.Error
		if errors.As(err, &ce) {
			return err
		}
		return core.NewError(core.CodeInvalidArgument, "request is not valid", nil)
	}
	return g.auth.Check(ctx, principal, d.Action, res)
}

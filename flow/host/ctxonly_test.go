package host

import (
	"context"

	"github.com/epzero/basamento/runtime"
)

// ctxOnlyDC is a runtime.DurableContext of which only Context works: in-package tests of
// unexported host functions use it where nothing else is called; any other call panics
// on the nil embedded interface, which flags misuse.
type ctxOnlyDC struct{ runtime.DurableContext }

func (ctxOnlyDC) Context() context.Context { return context.Background() }

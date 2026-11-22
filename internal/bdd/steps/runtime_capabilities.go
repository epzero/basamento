package steps

import (
	"fmt"

	"github.com/epzero/basamento/runtime"
)

func checkRuntimeCapability(caps runtime.Caps, name, want string) error {
	values := map[string]bool{
		"durable":           caps.Durable,
		"sync_gate_updates": caps.SyncGateUpdates,
		"continue_as_new":   caps.ContinueAsNew,
		"polyglot_queues":   caps.PolyglotQueues,
	}
	got, ok := values[name]
	if !ok {
		return fmt.Errorf("unknown capability %q", name)
	}
	if fmt.Sprintf("%t", got) != want {
		return fmt.Errorf("capability %q is %t, want %s", name, got, want)
	}
	return nil
}

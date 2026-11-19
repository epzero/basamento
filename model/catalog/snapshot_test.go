package catalog

import (
	"testing"

	"github.com/epzero/basamento/core"
	"gopkg.in/yaml.v3"
)

func TestSnapshotLoaderRejectsNegativeRate(t *testing.T) {
	var prices map[string]Price
	raw := []byte(`sim-chat:
  rules:
    - rates:
        input: -1
        output: 0
`)
	if err := yaml.Unmarshal(raw, &prices); err != nil {
		t.Fatalf("yaml: %v", err)
	}
	for modelID, p := range prices {
		if err := validatePrice(modelID, p); core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatalf("validatePrice: code=%s err=%v", core.CodeOf(err), err)
		}
	}
}

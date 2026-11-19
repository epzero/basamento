package catalog

import (
	_ "embed"
	"fmt"

	"gopkg.in/yaml.v3"
)

// snapshotYAML is the price snapshot shipped with the binary. It is data, not
// code: a deployment or tenant override replaces an entry, and nothing fetches
// prices at run time. It ships empty until the dataset generation step adds
// entries, so a model without an override has no price in this layer.
//
//go:embed snapshot.yaml
var snapshotYAML []byte

func loadSnapshot() (map[string]Price, error) {
	var prices map[string]Price
	if err := yaml.Unmarshal(snapshotYAML, &prices); err != nil {
		return nil, internalError("price snapshot is invalid", fmt.Errorf("yaml: %w", err))
	}
	if prices == nil {
		prices = map[string]Price{}
	}
	return prices, nil
}

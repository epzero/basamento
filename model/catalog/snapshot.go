package catalog

import (
	"bytes"
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
	return parseSnapshot(snapshotYAML)
}

func parseSnapshot(data []byte) (map[string]Price, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var prices map[string]Price
	if err := dec.Decode(&prices); err != nil {
		return nil, internalError("price snapshot is invalid", fmt.Errorf("yaml: %w", err))
	}
	if prices == nil {
		prices = map[string]Price{}
	}
	for modelID, p := range prices {
		if err := validatePrice(modelID, p); err != nil {
			return nil, err
		}
	}
	return prices, nil
}

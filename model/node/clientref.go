package node

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"

	"github.com/epzero/basamento/core/schema"
)

// clientRefPrefix marks a client ref the model node derived, so one cannot be
// confused with a caller's own.
const clientRefPrefix = "mn_"

// ClientRef derives the stable client ref of one logical invocation from the
// run-scoped visit token, the node's path and the node's input
// (REQ-MODEL-004, docs/architecture/model-layer.md §4). It is a hash, never a
// literal concatenation, so the ref carries no tenant, model or input text.
//
// The visit token is what makes two visits of one node in a run distinct: a
// node reached again on a cycle, or another item of a map, gets its own token
// and therefore its own ref. A replayed run derives the same token from the
// same position and recreates the same ref, which is what lets a runtime
// resume a start instead of repeating it.
func ClientRef(visit, node string, input []byte) string {
	canonical := input
	if c, err := schema.Canonicalize(input); err == nil {
		canonical = c
	}
	h := sha256.New()
	writeField(h, []byte(visit))
	writeField(h, []byte(node))
	writeField(h, canonical)
	return clientRefPrefix + hex.EncodeToString(h.Sum(nil))
}

// writeField length-prefixes one field so two different splits of the same
// bytes never hash alike.
func writeField(h interface{ Write([]byte) (int, error) }, field []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(field)))
	_, _ = h.Write(size[:])
	_, _ = h.Write(field)
}

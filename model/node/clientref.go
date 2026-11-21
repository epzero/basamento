package node

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"

	"github.com/epzero/basamento/core/schema"
	"github.com/epzero/basamento/runtime"
)

// clientRefPrefix marks a client ref the model node derived, so one cannot be
// confused with a caller's own.
const clientRefPrefix = "mn_"

// saltToken is the key of the run-scoped salt the client ref is derived from.
const saltToken = "model-node-salt"

// ClientRef derives the stable client ref of one logical invocation from the
// run's salt, the run-scoped visit token, the node's path and the node's input
// (REQ-MODEL-004, docs/architecture/model-layer.md §4). It is a hash, never a
// literal concatenation, so the ref carries no tenant, model or input text.
//
// The salt is what keeps two runs apart: both start their first invocation
// with the same visit token, so without it they would share one provider
// invocation. The visit token is what makes two visits of one node in a run
// distinct: a node reached again on a cycle, or another item of a map, gets
// its own token and therefore its own ref. A replayed run derives the same
// salt and the same token from the same position and recreates the same ref,
// which is what lets a runtime resume a start instead of repeating it.
func ClientRef(salt []byte, visit, node string, input []byte) string {
	canonical := input
	if c, err := schema.Canonicalize(input); err == nil {
		canonical = c
	}
	h := sha256.New()
	writeField(h, salt)
	writeField(h, []byte(visit))
	writeField(h, []byte(node))
	writeField(h, canonical)
	return clientRefPrefix + hex.EncodeToString(h.Sum(nil))
}

// runSalt draws the per-run salt the client ref is derived from. The runtime
// seeds Rand from (run, key), so the salt is replay-stable and differs between
// runs even when both visit the same node with the same input.
func runSalt(dc runtime.DurableContext) []byte {
	var salt [16]byte
	r := dc.Rand(saltToken)
	binary.BigEndian.PutUint64(salt[:8], r.Uint64())
	binary.BigEndian.PutUint64(salt[8:], r.Uint64())
	return salt[:]
}

// writeField length-prefixes one field so two different splits of the same
// bytes never hash alike.
func writeField(h interface{ Write([]byte) (int, error) }, field []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(field)))
	_, _ = h.Write(size[:])
	_, _ = h.Write(field)
}

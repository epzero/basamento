package coretest

import (
	"io"
	"math/rand"
)

// SeededRand returns an io.Reader backed by a deterministic math/rand source.
func SeededRand(seed int64) io.Reader {
	return rand.New(rand.NewSource(seed))
}

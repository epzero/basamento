package bus

import (
	"encoding/binary"
	"errors"
)

var errFloorRecordShort = errors.New("short purge floor record")

func parseUint64(b []byte) (uint64, error) {
	if len(b) < 8 {
		return 0, errFloorRecordShort
	}
	return binary.BigEndian.Uint64(b), nil
}

// subscribePurged reports whether after lies below the purge floor (REQ-BUS-008).
func subscribePurged(after, floor uint64) error {
	if after > 0 && after < floor {
		return purged(floor)
	}
	return nil
}

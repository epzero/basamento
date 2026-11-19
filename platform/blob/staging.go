package blob

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform"
)

const stagePrefix = "stage_"

// maxUploadContentTypeLen keeps slot metadata within the read cap.
const maxUploadContentTypeLen = 128

var stagingKeyRE = regexp.MustCompile(`^stage_[A-Za-z0-9_-]{43}$`)

// ValidateStagingKey reports whether k matches the staging key grammar.
func ValidateStagingKey(k string) bool {
	return stagingKeyRE.MatchString(k)
}

func stageObjectPath(tid core.TenantID, stageKey string) (string, error) {
	if !ValidateStagingKey(stageKey) {
		return "", core.NewError(core.CodeInvalidArgument, "malformed staging key", nil)
	}
	return fmt.Sprintf("%s/stage/%s", tid, stageKey), nil
}

func slotMetaPath(tid core.TenantID, stageKey string) (string, error) {
	p, err := stageObjectPath(tid, stageKey)
	if err != nil {
		return "", err
	}
	return p + ".slot", nil
}

func commitRecordPath(tid core.TenantID, stageKey string) (string, error) {
	p, err := stageObjectPath(tid, stageKey)
	if err != nil {
		return "", err
	}
	return p + ".committed", nil
}

type slotMeta struct {
	Max         int64  `json:"max"`
	ContentType string `json:"content_type"`
	Expires     int64  `json:"expires"`
}

func (m slotMeta) expired(now time.Time) bool {
	return !now.Before(time.Unix(0, m.Expires))
}

func validateUploadOptions(o platform.UploadOptions, maxTTL time.Duration) error {
	if o.MaxSize <= 0 {
		return core.NewError(core.CodeInvalidArgument, "upload maximum size out of bounds", nil)
	}
	if o.TTL <= 0 || o.TTL > maxTTL {
		return core.NewError(core.CodeInvalidArgument, "upload lifetime out of bounds", nil)
	}
	if !validUploadContentType(o.ContentType) {
		return core.NewError(core.CodeInvalidArgument, "upload content type out of bounds", nil)
	}
	return nil
}

func validUploadContentType(ct string) bool {
	return len(ct) <= maxUploadContentTypeLen && validBoundContentType(ct)
}

func validBoundContentType(ct string) bool {
	if ct == "" || stringsContainsAny(ct, ",*;") {
		return false
	}
	parts := splitSlash(ct)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false
	}
	return true
}

func stringsContainsAny(s, chars string) bool {
	for i := 0; i < len(s); i++ {
		for j := 0; j < len(chars); j++ {
			if s[i] == chars[j] {
				return true
			}
		}
	}
	return false
}

func splitSlash(s string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	parts = append(parts, s[start:])
	return parts
}

func newStagingKey(rand io.Reader) (string, error) {
	var b [32]byte
	if _, err := io.ReadFull(rand, b[:]); err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding.EncodeToString(b[:])
	return stagePrefix + enc, nil
}

func encodeSlotMeta(m slotMeta) ([]byte, error) {
	return json.Marshal(m)
}

func decodeSlotMeta(b []byte) (slotMeta, error) {
	var m slotMeta
	if err := json.Unmarshal(b, &m); err != nil {
		return slotMeta{}, err
	}
	return m, nil
}

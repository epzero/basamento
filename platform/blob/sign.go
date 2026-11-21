package blob

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/epzero/basamento/core"
)

func (s *Store) signedProxied(tid core.TenantID, key string, ttl time.Duration) (string, error) {
	if s.baseURL == "" || len(s.signKey) == 0 {
		return "", core.NewError(core.CodeInternal, "embedded blob URL signing not configured", nil)
	}
	hex, err := HexOf(key)
	if err != nil {
		return "", err
	}
	exp := s.clock.Now().Add(ttl).Unix()
	sig := signRead(s.signKey, string(tid), hex, exp)
	base := strings.TrimRight(s.baseURL, "/")
	seg := url.PathEscape(string(tid))
	return fmt.Sprintf("%s/%s/%s?exp=%d&sig=%s", base, seg, hex, exp, sig), nil
}

// signRead computes the proxied read URL HMAC (REQ-BLOB-016).
func signRead(key []byte, tenant, hexdigest string, exp int64) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(tenant))
	mac.Write([]byte{0})
	mac.Write([]byte(hexdigest))
	mac.Write([]byte{0})
	mac.Write([]byte(strconv.FormatInt(exp, 10)))
	return hex.EncodeToString(mac.Sum(nil))
}

func signUpload(key []byte, tenant, stageKey string, exp, max int64, contentType string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("upload"))
	mac.Write([]byte{0})
	mac.Write([]byte(tenant))
	mac.Write([]byte{0})
	mac.Write([]byte(stageKey))
	mac.Write([]byte{0})
	mac.Write([]byte(strconv.FormatInt(exp, 10)))
	mac.Write([]byte{0})
	mac.Write([]byte(strconv.FormatInt(max, 10)))
	mac.Write([]byte{0})
	mac.Write([]byte(contentType))
	return hex.EncodeToString(mac.Sum(nil))
}

func verifyUpload(key []byte, tenant, stageKey, sig string, exp, max int64, contentType string, now time.Time) bool {
	want := signUpload(key, tenant, stageKey, exp, max, contentType)
	if len(sig) != len(want) {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(sig), []byte(want)) != 1 {
		return false
	}
	return now.Unix() < exp
}

func verifyRead(key []byte, tenant, hexdigest, sig string, exp int64, now time.Time) bool {
	if now.Unix() >= exp {
		return false
	}
	want := signRead(key, tenant, hexdigest, exp)
	if len(sig) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(sig), []byte(want)) == 1
}

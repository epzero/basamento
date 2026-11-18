package blob

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestOpenErrorRedactsSecrets(t *testing.T) {
	t.Parallel()
	secret := "SECRETEXAMPLE"
	key := "AKIAEXAMPLE"
	urls := []string{
		"s3://" + key + ":" + secret + "@bucket?region=us-east-1",
		"azblob://account.blob.core.windows.net/container?sig=fakesignaturevalue",
		"s3://bucket?region=us-east-1&endpoint=http://127.0.0.1:1",
	}
	for _, raw := range urls {
		_, _, err := Open(context.Background(), raw, Options{})
		if err == nil {
			continue
		}
		text := err.Error()
		if strings.Contains(text, secret) || strings.Contains(text, key+":") || strings.Contains(text, "fakesignaturevalue") {
			t.Fatalf("error leaks url material for %q: %s", raw, text)
		}
		detailed := fmt.Sprintf("%+v", err)
		if strings.Contains(detailed, secret) {
			t.Fatalf("+v leaks secret: %s", detailed)
		}
	}
}

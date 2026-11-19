package steps

import (
	"context"
	"fmt"
	"testing"

	"github.com/epzero/basamento/platform/secrets"
)

func TestLastDecodedNameTracksMostRecent(t *testing.T) {
	t.Parallel()
	ctx := context.WithValue(context.Background(), worldKey{}, &World{Secrets: newSecretsWorld()})
	sw := secretsW(ctx)
	sw.recordDecoded("first", secrets.Payload{
		Metadata: map[string][]byte{"encoding": []byte("wrong")},
		Data:     []byte("one"),
	})
	sw.recordDecoded("second", secrets.Payload{
		Metadata: map[string][]byte{"encoding": []byte("json/plain")},
		Data:     []byte("two"),
	})
	if got := lastDecodedName(ctx); got != "second" {
		t.Fatalf("lastDecodedName=%q want second", got)
	}
	if err := assertLastDecoded(ctx, "two"); err != nil {
		t.Fatalf("assertLastDecoded: %v", err)
	}
	if err := assertLastDecoded(ctx, "one"); err == nil {
		t.Fatal("assertLastDecoded should not accept stale first decode")
	}
	if err := assertDecodedMeta(ctx, "json/plain"); err != nil {
		t.Fatalf("assertDecodedMeta: %v", err)
	}
	sw.payloads["first"] = secrets.Payload{Data: []byte("one")}
	if err := assertDecodedEqualsPayload(ctx, "first"); err != nil {
		t.Fatalf("assertDecodedEqualsPayload by name: %v", err)
	}
}

func TestAssertNoValueRejectsPartialWithError(t *testing.T) {
	t.Parallel()
	ctx := context.WithValue(context.Background(), worldKey{}, &World{Secrets: newSecretsWorld()})
	sw := secretsW(ctx)
	sw.lastErr = fmt.Errorf("decrypt failed")
	sw.lastVal = []byte("partial")
	if err := assertNoValue(ctx); err == nil {
		t.Fatal("expected failure when value bytes accompany error")
	}
}

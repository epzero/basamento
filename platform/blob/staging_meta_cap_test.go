package blob

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform"
)

func TestUploadContentTypeLengthBound(t *testing.T) {
	t.Parallel()
	st, _ := openEmbeddedStore(t)
	defer func() { _ = st.Close() }()
	ctx := core.WithTenant(context.Background(), "acme")
	okCT := "application/" + strings.Repeat("a", 128-len("application/"))
	if len(okCT) != 128 {
		t.Fatalf("setup: len %d", len(okCT))
	}
	if _, err := st.StartUpload(ctx, platform.UploadOptions{MaxSize: 1, ContentType: okCT, TTL: time.Minute}); err != nil {
		t.Fatalf("128-byte type: %v", err)
	}
	longCT := okCT + "x"
	if _, err := st.StartUpload(ctx, platform.UploadOptions{MaxSize: 1, ContentType: longCT, TTL: time.Minute}); err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("129-byte type should be invalid_argument: %v", err)
	}
}

func TestSlotMetaFitsReadCap(t *testing.T) {
	t.Parallel()
	meta := slotMeta{
		Max:         1 << 50,
		ContentType: "application/" + strings.Repeat("z", maxUploadContentTypeLen-len("application/")),
		Expires:     time.Now().Add(7 * 24 * time.Hour).UnixNano(),
	}
	raw, err := encodeSlotMeta(meta)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > maxSlotMetaBytes {
		t.Fatalf("encoded meta %d bytes > %d", len(raw), maxSlotMetaBytes)
	}
}

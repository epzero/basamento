package steps

import (
	"context"
	"fmt"
	gcblob "gocloud.dev/blob"
	_ "gocloud.dev/blob/fileblob"
	_ "gocloud.dev/blob/s3blob"
	"io"
	"net/url"
)

func (w *blobWorld) conformBucket(ctx context.Context) (*gcblob.Bucket, error) {
	var err error
	w.conformOnce.Do(func() {
		raw := conformBucketURL(w.locURL)
		w.conformB, err = gcblob.OpenBucket(ctx, raw)
	})
	if err != nil {
		return nil, err
	}
	return w.conformB, nil
}

func conformBucketURL(raw string) string {
	u, parseErr := url.Parse(raw)
	if parseErr != nil || u.Scheme != "file" {
		return raw
	}
	q := u.Query()
	q.Set("no_tmp_dir", "true")
	u.RawQuery = q.Encode()
	return u.String()
}

func stagingObjectKey(tenant, stageKey string) string {
	return fmt.Sprintf("%s/stage/%s", tenant, stageKey)
}

func (w *blobWorld) stagingExists(ctx context.Context, tenant, stageKey string) (bool, error) {
	b, err := w.conformBucket(ctx)
	if err != nil {
		return false, err
	}
	return b.Exists(ctx, stagingObjectKey(tenant, stageKey))
}

func (w *blobWorld) readStagingObject(ctx context.Context, tenant, stageKey string) ([]byte, string, error) {
	b, err := w.conformBucket(ctx)
	if err != nil {
		return nil, "", err
	}
	key := stagingObjectKey(tenant, stageKey)
	r, err := b.NewReader(ctx, key, nil)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = r.Close() }()
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, "", err
	}
	attrs, err := b.Attributes(ctx, key)
	if err != nil {
		return data, "", err
	}
	return data, attrs.ContentType, nil
}

func (w *blobWorld) writeStagingObject(ctx context.Context, tenant, stageKey string, body []byte, contentType string) error {
	b, err := w.conformBucket(ctx)
	if err != nil {
		return err
	}
	return b.WriteAll(ctx, stagingObjectKey(tenant, stageKey), body, &gcblob.WriterOptions{ContentType: contentType})
}

func sweepStagingSlots(ctx context.Context) error {
	// Re-open runs sweepAtOpen at the current clock without advancing a full URL-lifetime
	// interval, which would expire slots the scenario still needs.
	return world(ctx).Blob.openAt(ctx, world(ctx).Core.clock, world(ctx).Blob.locURL)
}

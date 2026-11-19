package blob

import (
	"context"
	"errors"
	"io"
	"strings"

	"gocloud.dev/blob"
)

type gocloudBucket struct {
	b *blob.Bucket
}

func wrapBucket(b *blob.Bucket) bucketBackend {
	return &gocloudBucket{b: b}
}

func (g *gocloudBucket) Exists(ctx context.Context, key string) (bool, error) {
	return g.b.Exists(ctx, key)
}

func (g *gocloudBucket) Delete(ctx context.Context, key string) error {
	return g.b.Delete(ctx, key)
}

func (g *gocloudBucket) WriteAll(ctx context.Context, key string, p []byte, opts *blob.WriterOptions) error {
	return g.b.WriteAll(ctx, key, p, opts)
}

func (g *gocloudBucket) NewReader(ctx context.Context, key string, opts *blob.ReaderOptions) (*blob.Reader, error) {
	return g.b.NewReader(ctx, key, opts)
}

func (g *gocloudBucket) NewWriter(ctx context.Context, key string, opts *blob.WriterOptions) (*blob.Writer, error) {
	return g.b.NewWriter(ctx, key, opts)
}

func (g *gocloudBucket) Attributes(ctx context.Context, key string) (*blob.Attributes, error) {
	return g.b.Attributes(ctx, key)
}

func (g *gocloudBucket) SignedURL(ctx context.Context, key string, opts *blob.SignedURLOptions) (string, error) {
	return g.b.SignedURL(ctx, key, opts)
}

func (g *gocloudBucket) ListKeys(ctx context.Context, prefix string) ([]string, error) {
	iter := g.b.List(&blob.ListOptions{Prefix: prefix})
	var keys []string
	for {
		obj, err := iter.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, mapError(err)
		}
		keys = append(keys, obj.Key)
	}
	return keys, nil
}

func (g *gocloudBucket) ListTenantPrefixes(ctx context.Context) ([]string, error) {
	iter := g.b.List(&blob.ListOptions{Delimiter: "/"})
	var tenants []string
	for {
		obj, err := iter.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, mapError(err)
		}
		if obj.IsDir || strings.HasSuffix(obj.Key, "/") {
			tenants = append(tenants, strings.TrimSuffix(obj.Key, "/"))
		}
	}
	return tenants, nil
}

func (g *gocloudBucket) ListKeysCallback(ctx context.Context, prefix string, fn func(string) error) error {
	iter := g.b.List(&blob.ListOptions{Prefix: prefix})
	for {
		obj, err := iter.Next(ctx)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return mapError(err)
		}
		if err := fn(obj.Key); err != nil {
			return err
		}
	}
}

func (g *gocloudBucket) Close() error {
	return g.b.Close()
}

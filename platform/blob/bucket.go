package blob

import (
	"context"

	"gocloud.dev/blob"
)

// bucketBackend is the storage surface Store uses; tests may substitute fakes.
type bucketBackend interface {
	Exists(ctx context.Context, key string) (bool, error)
	Delete(ctx context.Context, key string) error
	WriteAll(ctx context.Context, key string, p []byte, opts *blob.WriterOptions) error
	NewReader(ctx context.Context, key string, opts *blob.ReaderOptions) (*blob.Reader, error)
	NewWriter(ctx context.Context, key string, opts *blob.WriterOptions) (*blob.Writer, error)
	Attributes(ctx context.Context, key string) (*blob.Attributes, error)
	SignedURL(ctx context.Context, key string, opts *blob.SignedURLOptions) (string, error)
	Close() error
}

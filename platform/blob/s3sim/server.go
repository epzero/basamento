package s3sim

import (
	"fmt"
	"net"
	"net/http/httptest"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
)

// Server is a gofakes3 endpoint with a bucket URL for gocloud s3blob.
type Server struct {
	URL      string
	Bucket   string
	Endpoint string
	srv      *httptest.Server
}

// Start runs an in-process S3-compatible server.
func Start() *Server {
	faker := gofakes3.New(s3mem.New(), gofakes3.WithAutoBucket(true))
	ts := httptest.NewServer(faker.Server())
	_, port, err := net.SplitHostPort(ts.Listener.Addr().String())
	if err != nil {
		ts.Close()
		panic(err)
	}
	endpoint := fmt.Sprintf("http://127.0.0.1:%s", port)
	bucket := "conformance"
	url := fmt.Sprintf("s3://%s?region=us-east-1&endpoint=%s&disable_https=true&use_path_style=true&awssdk=v2", bucket, endpoint)
	return &Server{URL: url, Bucket: bucket, Endpoint: endpoint, srv: ts}
}

// Close stops the server.
func (s *Server) Close() {
	if s != nil && s.srv != nil {
		s.srv.Close()
	}
}

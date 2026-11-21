package node

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/schema"
	"github.com/epzero/basamento/model"
	"github.com/epzero/basamento/platform/blob"
	"github.com/epzero/basamento/runtime"
)

// maxOutputBytes bounds one output file the node downloads before storing it.
// An output larger than this is refused rather than buffered.
const maxOutputBytes = 512 << 20

// ingestTimeout bounds one download. It is the node's own client, not the
// run's clock: the bytes are fetched inside a durable step.
const ingestTimeout = 10 * time.Minute

// Asset is one output file stored with the provenance that explains it
// (REQ-MODEL-008).
type Asset struct {
	// Ref points at the content-addressed bytes.
	Ref model.AssetRef `json:"ref"`
	// Provenance is the metadata stored alongside the bytes.
	Provenance Provenance `json:"provenance"`
	// ProvenanceBlobKey is the content key of the stored provenance JSON.
	ProvenanceBlobKey string `json:"provenance_blob_key"`
}

// Provenance is what a stored output records about its invocation: the model,
// the hash of the parameters, the seed and the settled cost.
type Provenance struct {
	Model      string `json:"model"`
	Op         string `json:"op"`
	ParamsHash string `json:"params_hash"`
	Seed       *int64 `json:"seed,omitempty"`
	CostMpts   int64  `json:"cost_mpts"`
	Media      string `json:"media,omitempty"`
	Mime       string `json:"mime,omitempty"`
}

// Fetcher downloads one output. It returns the bytes and their content type.
type Fetcher func(ctx context.Context, out model.Output) ([]byte, string, error)

// ParamHash is the provenance hash of a call's parameters: the SHA-256 of
// their canonical JSON. An absent parameter object hashes as an empty object.
func ParamHash(params json.RawMessage) string {
	canonical := []byte("{}")
	if len(bytes.TrimSpace(params)) > 0 {
		if c, err := schema.Canonicalize(params); err == nil {
			canonical = c
		} else {
			canonical = params
		}
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

// ingestOutputs stores every output file of a result in content-addressed blob
// storage and keeps its provenance next to it. It runs as one durable step, so
// a replayed run stores each file at most once.
func (h *Handler) ingestOutputs(dc runtime.DurableContext, cfg Config, req model.Request, result model.Result, cost int64, ref string) ([]Asset, error) {
	name := stepName(cfg, "ingest", ref)
	if err := h.register(name, h.ingestStep, stepDefaults()); err != nil {
		return nil, err
	}
	var out struct {
		Assets []Asset `json:"assets"`
	}
	in := ingestIn{
		Model:  cfg.Model,
		Op:     string(cfg.Op),
		Params: req.Params,
		Seed:   req.Seed,
		Cost:   cost,
		Output: result.Outputs,
	}
	if err := dc.Step(name, in, runtime.StepOptions{
		Heartbeat:      stepHeartbeat,
		IdempotencyKey: name,
	}).Get(&out); err != nil {
		return nil, err
	}
	return out.Assets, nil
}

type ingestIn struct {
	Model  string          `json:"model"`
	Op     string          `json:"op"`
	Params json.RawMessage `json:"params,omitempty"`
	Seed   *int64          `json:"seed,omitempty"`
	Cost   int64           `json:"cost_mpts"`
	Output []model.Output  `json:"outputs"`
}

func (h *Handler) ingestStep(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	if h.deps.Blobs == nil {
		return nil, core.NewError(core.CodeInternal, "model node: no blob store to ingest into", nil)
	}
	var in ingestIn
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, core.NewError(core.CodeInvalidArgument, "model node: invalid ingest step input", nil)
	}
	fetch := h.deps.Fetch
	if fetch == nil {
		fetch = defaultFetcher
	}
	provenance := Provenance{
		Model:      in.Model,
		Op:         in.Op,
		ParamsHash: ParamHash(in.Params),
		Seed:       in.Seed,
		CostMpts:   in.Cost,
	}
	assets := make([]Asset, 0, len(in.Output))
	for _, output := range in.Output {
		data, mime, err := fetch(ctx, output)
		if err != nil {
			// A provider URL is presigned: its path and query are a credential,
			// so the details carry a fixed cause and the host only.
			return nil, core.NewError(core.CodeVendor,
				"model node: cannot read an output",
				map[string]any{"media": output.Media, "host": outputHost(output), "cause": "fetch_failed"})
		}
		if len(data) > maxOutputBytes {
			return nil, core.NewError(core.CodeInvalidArgument,
				"model node: output is too large", map[string]any{"bytes": len(data)})
		}
		if mime == "" {
			mime = output.Mime
		}
		key := blob.ContentKeyOf(data)
		if err := h.deps.Blobs.Put(ctx, key, bytes.NewReader(data), mime); err != nil {
			return nil, err
		}
		p := provenance
		p.Media = output.Media
		p.Mime = mime
		meta, err := json.Marshal(p)
		if err != nil {
			return nil, core.NewError(core.CodeInternal, "model node: cannot encode provenance", nil)
		}
		metaKey := blob.ContentKeyOf(meta)
		if err := h.deps.Blobs.Put(ctx, metaKey, bytes.NewReader(meta), "application/json"); err != nil {
			return nil, err
		}
		assets = append(assets, Asset{
			Ref: model.AssetRef{
				BlobKey: key,
				SHA256:  hex.EncodeToString(sha256Sum(data)),
				Media:   output.Media,
				Mime:    mime,
			},
			Provenance:        p,
			ProvenanceBlobKey: metaKey,
		})
	}
	return json.Marshal(struct {
		Assets []Asset `json:"assets"`
	}{Assets: assets})
}

func sha256Sum(data []byte) []byte {
	sum := sha256.Sum256(data)
	return sum[:]
}

// defaultFetcher reads an output from its URL or, for a command-line provider,
// its path. A URL must be https and must not resolve to an address this
// process should not reach; redirects are refused.
func defaultFetcher(ctx context.Context, out model.Output) ([]byte, string, error) {
	return outputFetcher{lookupIP: net.DefaultResolver.LookupIPAddr}.fetch(ctx, out)
}

// outputFetcher is the production fetcher. Its resolver and transport are
// fields so tests can exercise the address checks without a network.
type outputFetcher struct {
	lookupIP  func(context.Context, string) ([]net.IPAddr, error)
	transport http.RoundTripper
}

// errRedirect is returned when an output URL redirects. Following one would
// let a vendor's URL reach an address the initial check approved only for the
// original host.
var errRedirect = errors.New("output fetch does not follow redirects")

func (f outputFetcher) fetch(ctx context.Context, out model.Output) ([]byte, string, error) {
	switch {
	case out.URL != "":
		return f.fetchURL(ctx, out.URL)
	case out.Path != "":
		data, err := readBounded(out.Path, maxOutputBytes)
		return data, out.Mime, err
	default:
		return nil, "", errors.New("output has no URL or path")
	}
}

func (f outputFetcher) fetchURL(ctx context.Context, raw string) ([]byte, string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, "", errors.New("output URL is not a valid URL")
	}
	if u.Scheme != "https" {
		return nil, "", fmt.Errorf("output URL scheme %q is not https", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return nil, "", errors.New("output URL has no host")
	}
	ips, err := f.lookupIP(ctx, host)
	if err != nil {
		return nil, "", fmt.Errorf("cannot resolve output host %q", host)
	}
	if len(ips) == 0 {
		return nil, "", fmt.Errorf("output host %q has no address", host)
	}
	for _, ip := range ips {
		if !publicAddress(ip.IP) {
			return nil, "", fmt.Errorf("output host %q resolves to a non-public address", host)
		}
	}
	client := &http.Client{
		Timeout:   ingestTimeout,
		Transport: f.transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errRedirect
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", errors.New("output URL is not a valid request")
	}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, errRedirect) {
			return nil, "", errRedirect
		}
		// The client's error embeds the full URL, including a presigned query;
		// keep it out of logs and error details.
		return nil, "", errors.New("output fetch failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxOutputBytes+1))
	if err != nil {
		return nil, "", errors.New("reading the output failed")
	}
	return data, resp.Header.Get("Content-Type"), nil
}

// readBounded reads at most limit+1 bytes, so the caller can tell an oversized
// file from one that fits without buffering the whole file.
func readBounded(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return io.ReadAll(io.LimitReader(file, limit+1))
}

// publicAddress reports whether ip is safe to fetch an output from: not
// loopback, private, link-local, unspecified or multicast.
func publicAddress(ip net.IP) bool {
	if ip == nil {
		return false
	}
	return !ip.IsLoopback() && !ip.IsPrivate() &&
		!ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() &&
		!ip.IsUnspecified() && !ip.IsMulticast()
}

// outputHost is the non-sensitive part of an output's source for error
// details: the host of its URL, without path, query or userinfo. A path
// output has no host.
func outputHost(out model.Output) string {
	if out.URL == "" {
		return ""
	}
	u, err := url.Parse(out.URL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

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
	"net/http"
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
	if err := h.register(name, h.ingestStep); err != nil {
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
			return nil, core.NewError(core.CodeVendor,
				"model node: cannot read an output", map[string]any{"media": output.Media, "cause": err.Error()})
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
// its path. It never follows a redirect to another scheme.
func defaultFetcher(ctx context.Context, out model.Output) ([]byte, string, error) {
	switch {
	case out.URL != "":
		client := &http.Client{Timeout: ingestTimeout}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, out.URL, nil)
		if err != nil {
			return nil, "", err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, "", err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, "", fmt.Errorf("status %d", resp.StatusCode)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxOutputBytes+1))
		if err != nil {
			return nil, "", err
		}
		return data, resp.Header.Get("Content-Type"), nil
	case out.Path != "":
		data, err := os.ReadFile(out.Path)
		return data, out.Mime, err
	default:
		return nil, "", errors.New("output has no URL or path")
	}
}

package launcher

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform"
)

// maxResponseBytes bounds the response a launcher reads into memory.
const maxResponseBytes = 1 << 20

// CredentialResolver resolves a cred:// reference at the moment of use, as the
// generic HTTP provider does. The value never leaves this process.
type CredentialResolver interface {
	Resolve(ctx context.Context, ref string) ([]byte, error)
}

// Auth says where the resolved credential goes. The zero value sends it in the
// Authorization header with the Bearer scheme.
type Auth struct {
	Header string
	Scheme string
}

// HTTPConfig configures the http launcher: one templated request per launch.
// URL, Headers and Body may contain {pool}, {template}, {count},
// {idempotency_key} and {tokens} (the join tokens separated by commas). An
// empty Body sends a JSON object with the same fields, so a relay on the other
// side never has to parse a bespoke format.
type HTTPConfig struct {
	URL     string
	Method  string
	Headers map[string]string
	Body    string
	// Credential is a cred:// reference. Empty sends no credential.
	Credential string
	// Credentials resolves Credential. Nil refuses a config that names one.
	Credentials CredentialResolver
	// Auth says where the resolved credential goes.
	Auth Auth
	// Client is the HTTP client. Nil uses http.DefaultClient.
	Client *http.Client
}

// HTTP starts workers by calling a templated HTTP endpoint. It reaches Modal,
// Fly Machines, RunPod, a function URL, a CI dispatch or a small relay the
// operator writes; the endpoint's own authentication stays in a credential.
type HTTP struct {
	cfg    HTTPConfig
	client *http.Client
}

var _ platform.Launcher = (*HTTP)(nil)

// NewHTTP builds the http launcher.
func NewHTTP(cfg HTTPConfig) (*HTTP, error) {
	if strings.TrimSpace(cfg.URL) == "" {
		return nil, core.NewError(core.CodeInvalidArgument, "http launcher has no url", nil)
	}
	if _, err := url.Parse(cfg.URL); err != nil {
		return nil, core.NewError(core.CodeInvalidArgument, "http launcher url is invalid", nil)
	}
	if cfg.Credential != "" {
		if !strings.HasPrefix(cfg.Credential, "cred://") {
			return nil, core.NewError(core.CodeInvalidArgument, "http launcher credential is not a cred:// reference", nil)
		}
		if cfg.Credentials == nil {
			return nil, core.NewError(core.CodeInvalidArgument,
				"http launcher has a credential reference but no resolver", nil)
		}
	}
	client := cfg.Client
	if client == nil {
		client = http.DefaultClient
	}
	return &HTTP{cfg: cfg, client: client}, nil
}

// Launch sends one request for the whole batch and expects a 2xx answer.
func (h *HTTP) Launch(ctx context.Context, req platform.LaunchRequest) (platform.LaunchResult, error) {
	if err := validate(req); err != nil {
		return platform.LaunchResult{}, err
	}
	method := strings.ToUpper(strings.TrimSpace(h.cfg.Method))
	body, err := h.body(req, method)
	if err != nil {
		return platform.LaunchResult{}, err
	}
	if method == "" {
		if body != nil {
			method = http.MethodPost
		} else {
			method = http.MethodGet
		}
	}
	u := expand(h.cfg.URL, req)
	httpReq, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return platform.LaunchResult{}, core.NewError(core.CodeInvalidArgument, "http launcher cannot build a request", nil)
	}
	if body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	for k, v := range h.cfg.Headers {
		httpReq.Header.Set(k, expand(v, req))
	}
	if h.cfg.Credential != "" {
		secret, err := h.cfg.Credentials.Resolve(ctx, h.cfg.Credential)
		if err != nil {
			return platform.LaunchResult{}, err
		}
		if len(secret) == 0 {
			return platform.LaunchResult{}, core.NewError(core.CodeUnauthenticated, "launcher credential is empty", nil)
		}
		header, value := "Authorization", "Bearer "+string(secret)
		if h.cfg.Auth.Header != "" {
			header = h.cfg.Auth.Header
		}
		if h.cfg.Auth.Scheme != "" {
			value = h.cfg.Auth.Scheme + " " + string(secret)
		} else if h.cfg.Auth.Header != "" {
			value = string(secret)
		}
		httpReq.Header.Set(header, value)
	}
	resp, err := h.client.Do(httpReq)
	if err != nil {
		return platform.LaunchResult{}, core.NewError(core.CodeUnavailable,
			"launcher endpoint is unreachable", map[string]any{"error": err.Error()})
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return platform.LaunchResult{}, core.NewError(core.CodeUnavailable,
			fmt.Sprintf("launcher endpoint answered %d", resp.StatusCode),
			map[string]any{"status": resp.StatusCode})
	}
	return platform.LaunchResult{Accepted: true}, nil
}

func (h *HTTP) body(req platform.LaunchRequest, method string) (io.Reader, error) {
	if strings.TrimSpace(h.cfg.Body) != "" {
		return bytes.NewReader([]byte(expand(h.cfg.Body, req))), nil
	}
	if method == http.MethodGet && h.cfg.Method != "" {
		return nil, nil
	}
	var payload struct {
		Pool           string            `json:"pool"`
		Template       string            `json:"template"`
		Count          int               `json:"count"`
		JoinTokens     []string          `json:"join_tokens"`
		IdempotencyKey string            `json:"idempotency_key"`
		Settings       map[string]string `json:"settings,omitempty"`
	}
	payload.Pool = req.Pool
	payload.Template = req.Template
	payload.Count = req.Count
	payload.JoinTokens = req.JoinTokens
	payload.IdempotencyKey = req.IdempotencyKey
	payload.Settings = req.Settings
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, core.NewError(core.CodeInternal, "launcher cannot encode a request body", nil)
	}
	return bytes.NewReader(b), nil
}

func expand(s string, req platform.LaunchRequest) string {
	return strings.NewReplacer(
		"{pool}", req.Pool,
		"{template}", req.Template,
		"{count}", strconv.Itoa(req.Count),
		"{idempotency_key}", req.IdempotencyKey,
		"{tokens}", strings.Join(req.JoinTokens, ","),
	).Replace(s)
}

func validate(req platform.LaunchRequest) error {
	if strings.TrimSpace(req.Pool) == "" || strings.TrimSpace(req.Template) == "" {
		return core.NewError(core.CodeInvalidArgument, "launch request needs a pool and a template", nil)
	}
	if req.Count <= 0 {
		return core.NewError(core.CodeInvalidArgument, "launch request needs a positive count", nil)
	}
	if len(req.JoinTokens) != req.Count {
		return core.NewError(core.CodeInvalidArgument,
			"launch request needs one join token per worker",
			map[string]any{"count": req.Count, "tokens": len(req.JoinTokens)})
	}
	if strings.TrimSpace(req.IdempotencyKey) == "" {
		return core.NewError(core.CodeInvalidArgument, "launch request needs an idempotency key", nil)
	}
	for _, t := range req.JoinTokens {
		if strings.TrimSpace(t) == "" {
			return core.NewError(core.CodeInvalidArgument, "launch request has an empty join token", nil)
		}
	}
	return nil
}

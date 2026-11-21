package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/expr"
	"github.com/epzero/basamento/model"
)

// maxResponseBytes bounds one vendor response the provider reads into memory.
const maxResponseBytes = 8 << 20

// DefaultPollInterval is how long the generic HTTP provider waits between
// status calls when an invocation is submitted with the poll protocol.
const DefaultPollInterval = 250 * time.Millisecond

// CredentialResolver resolves a cred:// reference at the moment of use. It is
// satisfied by the credential store; providers never cache or log the value.
type CredentialResolver interface {
	Resolve(ctx context.Context, ref string) ([]byte, error)
}

// AssetResolver turns an output asset reference into a URL the vendor can
// fetch, for example a short-lived signed URL. Without one, a manifest sees the
// reference as stored (blob key and hash).
type AssetResolver interface {
	URL(ctx context.Context, ref model.AssetRef) (string, error)
}

// HTTPOptions configures the generic HTTP provider.
type HTTPOptions struct {
	// Client is the HTTP client. Nil uses a client with no timeout: the
	// invocation's context bounds each request, and an invocation outlives the
	// caller that started it.
	Client *http.Client
	// Credentials resolves the manifest's credential reference. Nil refuses a
	// manifest that names a credential.
	Credentials CredentialResolver
	// Assets resolves an input asset to a URL the vendor can fetch. Nil passes
	// the stored reference through unchanged.
	Assets AssetResolver
	// PollInterval is the wait between status calls. A negative value polls
	// back to back; zero uses DefaultPollInterval. Tests set a negative value
	// so no clock or sleep is involved.
	PollInterval time.Duration
	// Clock supplies Now for the registry's retention window and After for the
	// poll and retry waits. It must be a timer clock (core.SystemClock and
	// coretest.FakeClock implement it). Required: the registry fails closed
	// without one.
	Clock core.Clock
}

// wakeClock is the clock port the HTTP provider uses for Now and timer
// wake-ups. core.SystemClock and coretest.FakeClock implement it.
type wakeClock interface {
	core.Clock
	After(time.Duration) <-chan time.Time
}

// HTTP is a provider driven by a manifest over an HTTP API: start, status,
// cancel and lookup endpoints, with gojq mappings from our request to the
// vendor's JSON and back (docs/architecture/model-layer.md §3). It implements
// model.Invoker.
type HTTP struct {
	manifest    *Manifest
	descriptors []model.Descriptor
	base        *url.URL
	client      *http.Client
	credentials CredentialResolver
	assets      AssetResolver
	poll        time.Duration
	clock       wakeClock

	start  *compiledCall
	status *compiledCall
	cancel *compiledCall
	lookup *compiledCall

	runs *model.Registry

	// startMu serializes starts so two calls with the same client ref cannot
	// both reach the vendor. Starts are short; the vendor work is asynchronous.
	startMu sync.Mutex
}

var _ model.Invoker = (*HTTP)(nil)

// NewHTTP builds the generic HTTP provider from a manifest.
func NewHTTP(m *Manifest, opts HTTPOptions) (*HTTP, error) {
	if m == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "http provider needs a manifest", nil)
	}
	if m.HTTP == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "manifest has no http driver", nil)
	}
	if err := m.Validate(); err != nil {
		return nil, core.NewError(core.CodeInvalidArgument, err.Error(), nil)
	}
	if m.HTTP.Credential != "" && opts.Credentials == nil {
		return nil, core.NewError(core.CodeInvalidArgument,
			"http provider has a credential reference but no resolver", map[string]any{"provider": m.Name})
	}
	if opts.Clock == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "http provider has no clock", nil)
	}
	clock, ok := opts.Clock.(wakeClock)
	if !ok {
		return nil, core.NewError(core.CodeInvalidArgument,
			"http provider clock cannot schedule timers", nil)
	}
	descs, err := m.Descriptors()
	if err != nil {
		return nil, core.NewError(core.CodeInvalidArgument, err.Error(), nil)
	}
	base, err := url.Parse(m.HTTP.BaseURL)
	if err != nil {
		return nil, core.NewError(core.CodeInvalidArgument, "http provider base_url is invalid", nil)
	}
	start, err := compileCall(m.HTTP.Start)
	if err != nil {
		return nil, err
	}
	runs, err := model.NewRegistry(m.Name, model.WithClock(opts.Clock))
	if err != nil {
		return nil, err
	}
	h := &HTTP{
		manifest:    m,
		descriptors: descs,
		base:        base,
		client:      opts.Client,
		credentials: opts.Credentials,
		assets:      opts.Assets,
		poll:        opts.PollInterval,
		clock:       clock,
		start:       start,
		runs:        runs,
	}
	if h.client == nil {
		h.client = &http.Client{}
	}
	if h.poll == 0 {
		h.poll = DefaultPollInterval
	}
	if h.status, err = compileOptional(m.HTTP.Status); err != nil {
		return nil, err
	}
	if h.cancel, err = compileOptional(m.HTTP.Cancel); err != nil {
		return nil, err
	}
	if h.lookup, err = compileOptional(m.HTTP.Lookup); err != nil {
		return nil, err
	}
	return h, nil
}

// Provider is the manifest's provider name.
func (h *HTTP) Provider() string { return h.manifest.Name }

// Describe lists the models the manifest serves.
func (h *HTTP) Describe(context.Context) ([]model.Descriptor, error) {
	return append([]model.Descriptor(nil), h.descriptors...), nil
}

// Start submits an invocation and returns at once. A repeated start with the
// same client ref returns the original handle without reaching the vendor.
func (h *HTTP) Start(ctx context.Context, r model.Request) (model.Handle, error) {
	if _, err := model.Select(h.descriptors, r.Model, r.Op); err != nil {
		return model.Handle{}, err
	}
	if r.ClientRef == "" {
		return model.Handle{}, core.NewError(core.CodeInvalidArgument, "request has no client ref", nil)
	}
	// The registry keys the run by the tenant of ctx and fails without one. A
	// start it cannot record must never reach the vendor: that paid call would
	// be orphaned, since the node treats the error as final. Begin itself runs
	// after the vendor accepts, because the registry cannot reopen an ended
	// ref, so a run begun first would strand every retry of the same start.
	if _, ok := core.TenantFrom(ctx); !ok {
		return model.Handle{}, core.NewError(core.CodeInvalidArgument, "tenant required", nil)
	}
	h.startMu.Lock()
	defer h.startMu.Unlock()

	if hd, ok := h.local(ctx, r.ClientRef); ok {
		return hd, nil
	}
	input, err := h.inputFor(ctx, r)
	if err != nil {
		return model.Handle{}, err
	}
	doc, err := h.do(ctx, h.start, input, "")
	if err != nil {
		return model.Handle{}, err
	}
	id, err := h.startID(doc)
	if err != nil {
		return model.Handle{}, err
	}
	run, _, err := h.runs.Begin(ctx, r.ClientRef)
	if err != nil {
		return model.Handle{}, err
	}
	run.SetID(id)
	h.deliver(run, id, doc, h.manifest.Protocol == model.ProtocolSingle)

	switch h.manifest.Protocol {
	case model.ProtocolPoll:
		go h.follow(run, id, r.ClientRef)
	case model.ProtocolWebhook:
		// The callback resolves the invocation through Resolve.
	}
	return h.handle(r.ClientRef, id), nil
}

// Events follows the invocation's normalized events with Seq > after.
func (h *HTTP) Events(ctx context.Context, hd model.Handle, after int64) (<-chan model.Event, error) {
	return h.runs.Events(ctx, hd, after)
}

// Result waits for the invocation to end.
func (h *HTTP) Result(ctx context.Context, hd model.Handle) (model.Result, error) {
	return h.runs.Result(ctx, hd)
}

// Cancel asks the vendor to cancel the invocation when the manifest has a
// cancel call, then ends it as cancelled. Cancelling an invocation that has
// already ended is a no-op.
func (h *HTTP) Cancel(ctx context.Context, hd model.Handle) error {
	if hd.ClientRef == "" {
		return core.NewError(core.CodeInvalidArgument, "handle has no client ref", nil)
	}
	id, err := h.vendorID(ctx, hd)
	if err != nil {
		return err
	}
	// No run of this tenant holds the ref: the invocation was never ours or is
	// already forgotten, so nothing is sent to the vendor.
	if h.cancel != nil && id != "" {
		input, err := inputFor(model.Request{ClientRef: hd.ClientRef}, id, "")
		if err != nil {
			return err
		}
		if _, err := h.do(ctx, h.cancel, input, id); err != nil && core.CodeOf(err) != core.CodeNotFound {
			return err
		}
	}
	return h.runs.Cancel(ctx, hd)
}

// Lookup resolves an ambiguous start. It answers from this process first, then
// from the manifest's lookup endpoint. A provider without a lookup endpoint
// returns core.CodeUnsupported, which tells the model node to open the
// ambiguous-start gate instead of starting again (REQ-MODEL-005).
func (h *HTTP) Lookup(ctx context.Context, clientRef string) (*model.Handle, error) {
	if hd, ok := h.local(ctx, clientRef); ok {
		return &hd, nil
	}
	if h.lookup == nil {
		return nil, core.NewError(core.CodeUnsupported,
			"the provider has no lookup endpoint", map[string]any{"provider": h.manifest.Name})
	}
	if h.manifest.Protocol == model.ProtocolSingle {
		// A single-reply vendor has no status to read, so a found id would
		// leave the result unknowable. Answer "cannot resolve" instead.
		return nil, core.NewError(core.CodeUnsupported,
			"a single-reply provider cannot resolve an ambiguous start", map[string]any{"provider": h.manifest.Name})
	}
	input, err := inputFor(model.Request{ClientRef: clientRef}, "", "")
	if err != nil {
		return nil, err
	}
	doc, err := h.do(ctx, h.lookup, input, "")
	if err != nil {
		if core.CodeOf(err) == core.CodeNotFound {
			return nil, nil
		}
		return nil, err
	}
	foundID, err := h.lookup.response.read(doc)
	if err != nil || foundID.id == "" {
		return nil, nil
	}
	run, _, err := h.runs.Begin(ctx, clientRef)
	if err != nil {
		return nil, err
	}
	run.SetID(foundID.id)
	if h.manifest.Protocol == model.ProtocolPoll {
		go h.follow(run, foundID.id, clientRef)
	}
	hd := h.handle(clientRef, foundID.id)
	return &hd, nil
}

// Resolve completes a webhook-protocol invocation from the vendor's callback
// payload. It maps the payload with the status call's response mapping when a
// status call exists, and with the start call's mapping otherwise.
func (h *HTTP) Resolve(ctx context.Context, clientRef string, payload []byte) error {
	found, err := h.runs.Lookup(ctx, clientRef)
	if err != nil {
		return err
	}
	if found == nil {
		return core.NewError(core.CodeNotFound, "no invocation for the webhook", map[string]any{"client_ref": clientRef})
	}
	run, _, err := h.runs.Begin(ctx, clientRef)
	if err != nil {
		return err
	}
	var doc any
	if len(bytes.TrimSpace(payload)) > 0 {
		if err := json.Unmarshal(payload, &doc); err != nil {
			return core.NewError(core.CodeInvalidArgument, "webhook payload is not JSON", nil)
		}
	}
	response := h.start.response
	if h.status != nil {
		response = h.status.response
	}
	mapped, err := response.read(doc)
	if err != nil {
		run.Fail(err)
		return err
	}
	if mapped.progress != nil {
		run.Publish(model.Event{Kind: model.EventProgress, Progress: *mapped.progress})
	}
	if !mapped.hasState {
		mapped.state = model.StateSucceeded
		mapped.hasState = true
	}
	h.finish(run, mapped)
	return nil
}

// inputFor builds the mapping document for a start, resolving input assets to
// URLs the vendor can fetch when an AssetResolver is configured.
func (h *HTTP) inputFor(ctx context.Context, r model.Request) (map[string]any, error) {
	in, err := inputFor(r, "", "")
	if err != nil {
		return nil, err
	}
	if h.assets == nil || len(r.Assets) == 0 {
		return in, nil
	}
	assets := make(map[string]any, len(r.Assets))
	for slot, refs := range r.Assets {
		list := make([]any, 0, len(refs))
		for _, ref := range refs {
			u, err := h.assets.URL(ctx, ref)
			if err != nil {
				return nil, err
			}
			v, err := jsonValue(ref)
			if err != nil {
				return nil, err
			}
			obj, ok := v.(map[string]any)
			if !ok {
				return nil, core.NewError(core.CodeInternal, "provider asset is not an object", nil)
			}
			obj["url"] = u
			list = append(list, obj)
		}
		assets[slot] = list
	}
	in["assets"] = assets
	return in, nil
}

// statusRetryLimit bounds the consecutive retryable status errors one poll
// loop tolerates. A transient 429, 408, 504 or 5xx must not fail a paid run
// while the vendor job continues; a persistent one still ends it.
const statusRetryLimit = 5

// maxStatusRetryWait caps one wait between status retries, so a vendor's
// Retry-After hint cannot park the poll loop for hours.
const maxStatusRetryWait = 30 * time.Second

// statusRetryBudget bounds the total time one burst of status retries may
// spend waiting. A vendor that keeps asking for more time fails the run
// instead of holding it open indefinitely.
const statusRetryBudget = 2 * time.Minute

// follow polls the status endpoint until the invocation ends.
func (h *HTTP) follow(run *model.Run, id, clientRef string) {
	if h.status == nil {
		run.Fail(core.NewError(core.CodeInvalidArgument, "provider has no status endpoint", nil))
		return
	}
	ctx := run.Context()
	lastProgress := -1.0
	retries := 0
	var retryStart time.Time
	for {
		input, err := inputFor(model.Request{ClientRef: clientRef}, id, "")
		if err != nil {
			run.Fail(err)
			return
		}
		doc, err := h.do(ctx, h.status, input, id)
		if err != nil {
			if !core.IsRetryable(err) || retries >= statusRetryLimit {
				run.Fail(err)
				return
			}
			now := h.clock.Now()
			if retries == 0 {
				retryStart = now
			}
			wait := h.statusRetryWait(err)
			if now.Add(wait).Sub(retryStart) > statusRetryBudget {
				run.Fail(err)
				return
			}
			retries++
			if !h.sleep(ctx, wait) {
				return
			}
			continue
		}
		retries = 0
		mapped, err := h.status.response.read(doc)
		if err != nil {
			run.Fail(err)
			return
		}
		if mapped.progress != nil && *mapped.progress != lastProgress {
			lastProgress = *mapped.progress
			run.Publish(model.Event{Kind: model.EventProgress, Progress: *mapped.progress})
		}
		if mapped.terminal() {
			h.finish(run, mapped)
			return
		}
		if !h.pause(ctx) {
			return
		}
	}
}

// pause waits one poll interval, or returns at once when polling back to back
// (a negative interval, which tests use so no clock is involved). It reports
// false when the invocation's context ended.
func (h *HTTP) pause(ctx context.Context) bool {
	return h.sleep(ctx, h.poll)
}

// sleep waits d on the provider's clock, or returns at once when d is not
// positive. It reports false when the invocation's context ended.
func (h *HTTP) sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		select {
		case <-ctx.Done():
			return false
		default:
			return true
		}
	}
	select {
	case <-h.clock.After(d):
		return true
	case <-ctx.Done():
		return false
	}
}

// statusRetryWait returns how long to wait after a retryable status error: the
// poll interval, or the vendor's Retry-After hint when it is longer, both
// capped so a hint cannot park the poll loop.
func (h *HTTP) statusRetryWait(err error) time.Duration {
	wait := h.poll
	var pe *core.Error
	if errors.As(err, &pe) && pe.RetryAfter > wait {
		wait = pe.RetryAfter
	}
	if wait > maxStatusRetryWait {
		wait = maxStatusRetryWait
	}
	if wait < 0 {
		wait = 0
	}
	return wait
}

// deliver reads a document that carries a final or current state and ends the
// run when it is terminal. A single-reply or webhook payload without a state
// mapping is a successful result.
func (h *HTTP) deliver(run *model.Run, id string, doc any, final bool) {
	mapped, err := h.start.response.read(doc)
	if err != nil {
		run.Fail(err)
		return
	}
	if mapped.progress != nil {
		run.Publish(model.Event{Kind: model.EventProgress, Progress: *mapped.progress})
	}
	if !mapped.hasState && final {
		mapped.state = model.StateSucceeded
		mapped.hasState = true
	}
	if mapped.terminal() {
		h.finish(run, mapped)
	}
}

func (h *HTTP) finish(run *model.Run, m *mapped) {
	switch m.state {
	case model.StateSucceeded:
		run.Succeed(m.result())
	case model.StateCancelled:
		run.Cancel()
	case model.StateFailed:
		run.Fail(m.failure())
	default:
		run.Succeed(m.result())
	}
}

func (h *HTTP) startID(doc any) (string, error) {
	return h.start.response.idOf(doc)
}

// local answers a start or lookup from this process's registry, within the
// tenant of ctx. The run carries the vendor id the start recorded, so the
// handle it yields addresses the vendor directly.
func (h *HTTP) local(ctx context.Context, clientRef string) (model.Handle, bool) {
	hd, err := h.runs.Lookup(ctx, clientRef)
	if err != nil || hd == nil {
		return model.Handle{}, false
	}
	return h.handle(hd.ClientRef, hd.ID), true
}

func (h *HTTP) handle(clientRef, id string) model.Handle {
	return model.Handle{Provider: h.manifest.Name, ID: id, ClientRef: clientRef}
}

// vendorID is the vendor id of a handle within the tenant of ctx: the id the
// run recorded when the vendor accepted the start, else the handle's own. An
// empty answer means no run of this tenant holds the client ref.
func (h *HTTP) vendorID(ctx context.Context, hd model.Handle) (string, error) {
	found, err := h.runs.Lookup(ctx, hd.ClientRef)
	if err != nil {
		return "", err
	}
	if found == nil {
		return "", nil
	}
	if found.ID != "" {
		return found.ID, nil
	}
	return hd.ID, nil
}

// do executes one manifest call and returns its decoded JSON document.
func (h *HTTP) do(ctx context.Context, c *compiledCall, input map[string]any, id string) (any, error) {
	req, err := h.request(ctx, c, input, id)
	if err != nil {
		return nil, err
	}
	resp, err := h.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, model.MapError(ctx.Err())
		}
		// The request may have reached the vendor. A retryable timeout lets
		// the model node look the invocation up before starting again.
		return nil, &core.Error{Code: core.CodeTimeout, Message: "provider request failed", Retryable: true, Cause: err}
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, &core.Error{Code: core.CodeTimeout, Message: "provider response could not be read", Retryable: true, Cause: err}
	}
	if len(body) > maxResponseBytes {
		return nil, core.NewError(core.CodeInvalidArgument, "provider response is too large", nil)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, statusError(resp)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, nil
	}
	var doc any
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, core.NewError(core.CodeVendor, "provider response is not JSON", nil)
	}
	return doc, nil
}

func (h *HTTP) request(ctx context.Context, c *compiledCall, input map[string]any, id string) (*http.Request, error) {
	input["base_url"] = h.manifest.HTTP.BaseURL
	body, query, err := c.mapInput(input)
	if err != nil {
		return nil, err
	}
	path := strings.ReplaceAll(c.call.Path, "{id}", url.PathEscape(id))
	u := *h.base
	u.Path = strings.TrimRight(u.Path, "/") + "/" + strings.TrimLeft(path, "/")
	if len(query) > 0 {
		q := u.Query()
		for k, v := range query {
			q.Set(k, v)
		}
		u.RawQuery = q.Encode()
	}
	method := c.call.Method
	if method == "" {
		if body != nil {
			method = http.MethodPost
		} else {
			method = http.MethodGet
		}
	}
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, core.NewError(core.CodeInternal, "provider cannot encode a request body", nil)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return nil, core.NewError(core.CodeInvalidArgument, "provider cannot build a request", nil)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range c.call.Headers {
		req.Header.Set(k, v)
	}
	if h.manifest.HTTP.Credential != "" {
		secret, err := h.credentials.Resolve(ctx, h.manifest.HTTP.Credential)
		if err != nil {
			return nil, err
		}
		if len(secret) == 0 {
			return nil, core.NewError(core.CodeUnauthenticated, "provider credential is empty", nil)
		}
		header, value := "Authorization", "Bearer "+string(secret)
		if h.manifest.HTTP.Auth != nil {
			if h.manifest.HTTP.Auth.Header != "" {
				header = h.manifest.HTTP.Auth.Header
			}
			if h.manifest.HTTP.Auth.Scheme == "" {
				value = string(secret)
			} else {
				value = h.manifest.HTTP.Auth.Scheme + " " + string(secret)
			}
		}
		req.Header.Set(header, value)
	}
	return req, nil
}

// compiledCall is a manifest call with its mappings parsed once.
type compiledCall struct {
	call     Call
	body     *expr.Mapping
	query    map[string]*expr.Mapping
	response *compiledResponse
}

type compiledResponse struct {
	Response
	id       *expr.Mapping
	state    *expr.Mapping
	progress *expr.Mapping
	outputs  *expr.Mapping
	usage    *expr.Mapping
	cost     *expr.Mapping
	failure  *expr.Mapping
	code     *expr.Mapping
}

func compileCall(c Call) (*compiledCall, error) {
	cc := &compiledCall{call: c, query: map[string]*expr.Mapping{}}
	var err error
	if c.Body != "" {
		if cc.body, err = compileMapping(c.Body); err != nil {
			return nil, core.NewError(core.CodeInvalidArgument, "manifest call body mapping is invalid", nil)
		}
	}
	for name, src := range c.Query {
		m, err := compileMapping(src)
		if err != nil {
			return nil, core.NewError(core.CodeInvalidArgument, "manifest query mapping is invalid", map[string]any{"query": name})
		}
		cc.query[name] = m
	}
	r, err := compileResponse(c.Response)
	if err != nil {
		return nil, err
	}
	cc.response = r
	return cc, nil
}

func compileOptional(c *Call) (*compiledCall, error) {
	if c == nil {
		return nil, nil
	}
	return compileCall(*c)
}

func compileResponse(r Response) (*compiledResponse, error) {
	cr := &compiledResponse{Response: r}
	type pair struct {
		src string
		dst **expr.Mapping
	}
	pairs := []pair{
		{r.ID, &cr.id}, {r.State, &cr.state}, {r.Progress, &cr.progress},
		{r.Outputs, &cr.outputs}, {r.Usage, &cr.usage}, {r.Cost, &cr.cost},
		{r.Error, &cr.failure}, {r.Code, &cr.code},
	}
	for _, p := range pairs {
		if p.src == "" {
			continue
		}
		m, err := compileMapping(p.src)
		if err != nil {
			return nil, core.NewError(core.CodeInvalidArgument, "manifest response mapping is invalid", nil)
		}
		*p.dst = m
	}
	return cr, nil
}

func (c *compiledCall) mapInput(input map[string]any) (any, map[string]string, error) {
	var body any
	var err error
	if c.body != nil {
		body, err = evalMapping(c.body, input)
		if err != nil {
			return nil, nil, err
		}
	}
	query := make(map[string]string, len(c.query))
	for name, m := range c.query {
		v, err := evalMapping(m, input)
		if err != nil {
			return nil, nil, err
		}
		query[name] = toString(v)
	}
	return body, query, nil
}

// mapped is a vendor answer read into the contract's fields.
type mapped struct {
	id       string
	state    model.State
	hasState bool
	progress *float64
	outputs  []model.Output
	usage    *model.Usage
	cost     int64
	message  string
	code     core.Code
	retry    bool
}

func (c *compiledResponse) read(doc any) (*mapped, error) {
	m := &mapped{}
	id, err := c.idOf(doc)
	if err != nil {
		return nil, err
	}
	m.id = id
	if c.state != nil {
		raw, err := evalString(c.state, doc)
		if err != nil {
			return nil, err
		}
		m.hasState = true
		state, err := c.stateOf(raw)
		if err != nil {
			return nil, err
		}
		m.state = state
	}
	if c.progress != nil {
		v, err := evalMapping(c.progress, doc)
		if err != nil {
			return nil, err
		}
		if p, ok := toFloat(v); ok {
			if p < 0 {
				p = 0
			}
			if p > 1 {
				p = 1
			}
			m.progress = &p
		}
	}
	if c.outputs != nil {
		v, err := evalMapping(c.outputs, doc)
		if err != nil {
			return nil, err
		}
		if v != nil {
			b, err := json.Marshal(v)
			if err != nil {
				return nil, core.NewError(core.CodeVendor, "provider outputs are not readable", nil)
			}
			if err := json.Unmarshal(b, &m.outputs); err != nil {
				return nil, core.NewError(core.CodeVendor, "provider outputs do not match the output shape", nil)
			}
		}
	}
	if c.usage != nil {
		v, err := evalMapping(c.usage, doc)
		if err != nil {
			return nil, err
		}
		if v != nil {
			b, err := json.Marshal(v)
			if err != nil {
				return nil, core.NewError(core.CodeVendor, "provider usage is not readable", nil)
			}
			var u model.Usage
			if err := json.Unmarshal(b, &u); err != nil {
				return nil, core.NewError(core.CodeVendor, "provider usage does not match the usage shape", nil)
			}
			m.usage = &u
		}
	}
	if c.cost != nil {
		v, err := evalMapping(c.cost, doc)
		if err != nil {
			return nil, err
		}
		if n, ok := toInt(v); ok {
			m.cost = n
		}
	}
	if c.failure != nil {
		msg, err := evalString(c.failure, doc)
		if err != nil {
			return nil, err
		}
		m.message = msg
	}
	if c.code != nil {
		code, err := evalString(c.code, doc)
		if err != nil {
			return nil, err
		}
		m.code = core.Code(code)
	}
	m.retry = c.Retryable
	return m, nil
}

// idOf reads only the vendor id, so a start response is recorded even when
// its state mapping is incomplete: failing the start would orphan the call.
func (c *compiledResponse) idOf(doc any) (string, error) {
	if c.id != nil {
		return evalString(c.id, doc)
	}
	if obj, ok := doc.(map[string]any); ok {
		if v, ok := obj["id"]; ok {
			return toString(v), nil
		}
	}
	return "", nil
}

// stateOf maps a vendor state string. A state the manifest does not map is
// vendor_error, never running: a state_map gap must not leave a paid
// invocation running forever.
func (c *compiledResponse) stateOf(raw string) (model.State, error) {
	if c.StateMap != nil {
		if s, ok := c.StateMap[raw]; ok {
			return s, nil
		}
		return model.StateRunning, core.NewError(core.CodeVendor,
			"provider reported a state the manifest does not map",
			map[string]any{"state": raw})
	}
	switch model.State(raw) {
	case model.StateRunning, model.StateSucceeded, model.StateFailed, model.StateCancelled:
		return model.State(raw), nil
	default:
		return model.StateRunning, core.NewError(core.CodeVendor,
			"provider reported an unknown state", map[string]any{"state": raw})
	}
}

func (m *mapped) terminal() bool {
	return m.hasState && m.state != model.StateRunning
}

func (m *mapped) result() model.Result {
	return model.Result{
		State:    m.state,
		Outputs:  m.outputs,
		Usage:    m.usage,
		CostMpts: m.cost,
	}
}

func (m *mapped) failure() error {
	code := m.code
	if code == "" {
		code = core.CodeVendor
	}
	msg := m.message
	if msg == "" {
		msg = "provider invocation failed"
	}
	return &core.Error{Code: code, Message: msg, Retryable: m.retry}
}

func evalString(m *expr.Mapping, doc any) (string, error) {
	v, err := evalMapping(m, doc)
	if err != nil {
		return "", err
	}
	return toString(v), nil
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

func toInt(v any) (int64, bool) {
	switch x := v.(type) {
	case float64:
		return int64(x), true
	case int:
		return int64(x), true
	case int64:
		return x, true
	case json.Number:
		n, err := x.Int64()
		return n, err == nil
	default:
		return 0, false
	}
}

// statusError maps a non-2xx answer to the platform's error taxonomy. It never
// copies the vendor body, which may carry secrets.
func statusError(resp *http.Response) error {
	code := core.CodeVendor
	retryable := false
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		code = core.CodeUnauthenticated
	case http.StatusForbidden:
		code = core.CodePermissionDenied
	case http.StatusNotFound:
		code = core.CodeNotFound
	case http.StatusConflict:
		code = core.CodeConflict
	case http.StatusTooManyRequests:
		code, retryable = core.CodeRateLimited, true
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		code, retryable = core.CodeTimeout, true
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		code = core.CodeInvalidArgument
	default:
		if resp.StatusCode >= 500 {
			code, retryable = core.CodeUnavailable, true
		}
	}
	err := &core.Error{
		Code:      code,
		Message:   fmt.Sprintf("provider answered HTTP %d", resp.StatusCode),
		Retryable: retryable,
		Details:   map[string]any{"status": resp.StatusCode},
	}
	if v := resp.Header.Get("Retry-After"); v != "" {
		if seconds, convErr := strconv.Atoi(v); convErr == nil && seconds > 0 {
			err.RetryAfter = time.Duration(seconds) * time.Second
		}
	}
	return err
}

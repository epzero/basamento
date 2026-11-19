package providers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model"
)

// Manifest describes one provider: the models it serves and the driver that
// reaches the vendor. Generic HTTP API and command-line providers are data,
// not code, so a new vendor is a new manifest (docs/architecture/model-layer.md
// §3). Exactly one driver is set.
type Manifest struct {
	// Name is the provider name stamped on every descriptor and handle.
	Name string `yaml:"name"`
	// Locality is where the provider's models run. A manifest can only serve
	// models of one locality.
	Locality model.Locality `yaml:"locality"`
	// Protocol is how the vendor delivers results: single, poll or webhook for
	// HTTP; command for CLI. It must match the driver.
	Protocol model.Protocol `yaml:"protocol"`
	// Models are the descriptors this provider serves.
	Models []ModelSpec `yaml:"models"`
	// HTTP configures the generic HTTP API driver.
	HTTP *HTTPDriver `yaml:"http,omitempty"`
	// CLI configures the generic command-line driver.
	CLI *CLIDriver `yaml:"cli,omitempty"`
}

// ModelSpec is one model a manifest serves. It becomes a model.Descriptor.
type ModelSpec struct {
	// ID is the model id the catalog and flows use.
	ID string `yaml:"id"`
	// Caps are the capabilities the model declares.
	Caps []model.Capability `yaml:"caps"`
	// Params is the model's parameter JSON Schema, written as YAML.
	Params map[string]any `yaml:"params_schema,omitempty"`
	// Inputs are the model's input slots.
	Inputs []model.InputSlot `yaml:"inputs,omitempty"`
	// Price is the model's CEL price formula in milli-points. Empty uses the
	// catalog's rate rules and default token formula.
	Price string `yaml:"price,omitempty"`
	// Units is what one invocation costs on a worker; 0 reads as 1
	// (ADR-0012 §6). It has no meaning for a provider that is not a worker.
	Units int `yaml:"units,omitempty"`
	// Restart is "safe" when an interrupted invocation may be started again
	// on another worker (ADR-0012 §7); empty or "unsafe" is the default.
	Restart string `yaml:"restart,omitempty"`
}

// HTTP is the generic HTTP API driver. Every call is one HTTP request whose
// response is mapped with gojq into the invocation contract's fields.
type HTTPDriver struct {
	// BaseURL is the vendor's base URL, without a trailing slash.
	BaseURL string `yaml:"base_url"`
	// Credential is the cred:// reference resolved for every request. Empty
	// sends no credential.
	Credential string `yaml:"credential,omitempty"`
	// Auth says where the resolved credential goes. Defaults to the
	// Authorization header with the Bearer scheme.
	Auth *Auth `yaml:"auth,omitempty"`
	// Start submits an invocation.
	Start Call `yaml:"start"`
	// Status polls an invocation. Required for the poll protocol.
	Status *Call `yaml:"status,omitempty"`
	// Cancel asks the vendor to cancel an invocation. Optional.
	Cancel *Call `yaml:"cancel,omitempty"`
	// Lookup finds an invocation by client ref. Optional; without it an
	// ambiguous start cannot be resolved at the vendor.
	Lookup *Call `yaml:"lookup,omitempty"`
}

// Auth places a credential in a request header. The value is the scheme and
// the secret, separated by one space; an empty scheme sends the secret alone.
type Auth struct {
	Header string `yaml:"header,omitempty"`
	Scheme string `yaml:"scheme,omitempty"`
}

// Call describes one HTTP request and how its JSON response is read.
type Call struct {
	// Method is the HTTP method; default GET for a call without a body and
	// POST otherwise.
	Method string `yaml:"method,omitempty"`
	// Path is appended to the base URL. It may contain "{id}".
	Path string `yaml:"path"`
	// Body maps the invocation's input to the request body with gojq. Empty
	// sends the standard input object.
	Body string `yaml:"body,omitempty"`
	// Query maps each query parameter to a gojq expression over the input.
	Query map[string]string `yaml:"query,omitempty"`
	// Headers are literal request headers. Secrets never belong here; they
	// come from Credential.
	Headers map[string]string `yaml:"headers,omitempty"`
	// Response maps the vendor's JSON answer into the contract.
	Response Response `yaml:"response,omitempty"`
}

// Response maps a vendor's JSON answer with gojq. Every field is optional; the
// zero value reads nothing, which is right for a start call that only returns
// an id.
type Response struct {
	// ID is the vendor invocation id, default ".id".
	ID string `yaml:"id,omitempty"`
	// State is the vendor's state string.
	State string `yaml:"state,omitempty"`
	// StateMap maps a vendor state string to a model.State. Unlisted states
	// read as running.
	StateMap map[string]model.State `yaml:"state_map,omitempty"`
	// Progress is a 0..1 progress number.
	Progress string `yaml:"progress,omitempty"`
	// Outputs maps to an array of {url, path, media, mime}.
	Outputs string `yaml:"outputs,omitempty"`
	// Usage maps to model.Usage.
	Usage string `yaml:"usage,omitempty"`
	// Cost maps to the vendor's reported cost in milli-points.
	Cost string `yaml:"cost,omitempty"`
	// Error maps to the vendor's failure message.
	Error string `yaml:"error,omitempty"`
	// Code maps to a platform error code for a failed invocation. Default
	// "vendor_error".
	Code string `yaml:"code,omitempty"`
	// Retryable marks a failed invocation whose error may be retried.
	Retryable bool `yaml:"retryable,omitempty"`
}

// CLI is the generic command-line driver. It runs a command, parses progress
// from stdout and collects output files by glob. CLI providers are always
// machine-local.
type CLIDriver struct {
	// Command is the fixed argv prefix, for example ["ffmpeg"].
	Command []string `yaml:"command"`
	// Args is a gojq expression producing the array of arguments appended to
	// Command. It sees the invocation input plus workdir.
	Args string `yaml:"args,omitempty"`
	// Dir is the working directory. Output globs are relative to it.
	Dir string `yaml:"dir,omitempty"`
	// Env adds environment variables to the process. Values are literal.
	Env map[string]string `yaml:"env,omitempty"`
	// Outputs are file globs, relative to Dir, collected into the result.
	Outputs []string `yaml:"outputs"`
	// Progress is a regular expression with one number group, matched against
	// each stdout line. A match reports progress as the number over 100.
	Progress string `yaml:"progress,omitempty"`
}

// LoadManifest reads and validates one provider manifest from a file.
func LoadManifest(path string) (*Manifest, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, core.NewError(core.CodeNotFound,
			fmt.Sprintf("open provider manifest %q", path), map[string]any{"path": path})
	}
	defer func() { _ = f.Close() }()
	m, err := ReadManifest(f)
	if err != nil {
		return nil, core.NewError(core.CodeInvalidArgument,
			fmt.Sprintf("provider manifest %q: %v", path, err), map[string]any{"path": path})
	}
	return m, nil
}

// ReadManifest decodes and validates one provider manifest.
func ReadManifest(r io.Reader) (*Manifest, error) {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	var extra Manifest
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("manifest has more than one document")
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Validate checks that a manifest names its provider and models, picks one
// driver and that the driver can carry the declared protocol.
func (m *Manifest) Validate() error {
	if strings.TrimSpace(m.Name) == "" {
		return fmt.Errorf("manifest has no name")
	}
	switch m.Locality {
	case model.LocalityCloud, model.LocalityMachineLocal, model.LocalityWorker:
	default:
		return fmt.Errorf("unknown locality %q", m.Locality)
	}
	if (m.HTTP == nil) == (m.CLI == nil) {
		return fmt.Errorf("manifest %q must set exactly one of http and cli", m.Name)
	}
	if len(m.Models) == 0 {
		return fmt.Errorf("manifest %q has no models", m.Name)
	}
	for i, ms := range m.Models {
		if err := ms.validate(); err != nil {
			return fmt.Errorf("manifest %q model %d: %w", m.Name, i, err)
		}
		for j := range m.Models[:i] {
			if m.Models[j].ID == ms.ID {
				return fmt.Errorf("manifest %q declares model %q twice", m.Name, ms.ID)
			}
		}
	}
	if m.CLI != nil {
		if m.Locality != model.LocalityMachineLocal {
			return fmt.Errorf("manifest %q: a CLI provider is machine_local", m.Name)
		}
		if m.Protocol != model.ProtocolCommand {
			return fmt.Errorf("manifest %q: a CLI provider uses the command protocol", m.Name)
		}
		return m.CLI.validate()
	}
	switch m.Protocol {
	case model.ProtocolSingle:
	case model.ProtocolPoll:
		if m.HTTP.Status == nil {
			return fmt.Errorf("manifest %q: the poll protocol needs a status call", m.Name)
		}
		if m.HTTP.Status.Path == "" {
			return fmt.Errorf("manifest %q: the poll protocol needs a status path", m.Name)
		}
		if m.HTTP.Status.Response.State == "" {
			return fmt.Errorf("manifest %q: the poll protocol needs a state mapping", m.Name)
		}
	case model.ProtocolWebhook:
	default:
		return fmt.Errorf("manifest %q: the generic HTTP driver does not carry protocol %q", m.Name, m.Protocol)
	}
	return m.HTTP.validate()
}

func (m ModelSpec) validate() error {
	if strings.TrimSpace(m.ID) == "" {
		return fmt.Errorf("model has no id")
	}
	if len(m.Caps) == 0 {
		return fmt.Errorf("model %q has no capabilities", m.ID)
	}
	for _, c := range m.Caps {
		if strings.TrimSpace(string(c)) == "" {
			return fmt.Errorf("model %q has an empty capability", m.ID)
		}
	}
	if m.Units < 0 {
		return fmt.Errorf("model %q has negative units", m.ID)
	}
	switch m.Restart {
	case "", "unsafe", "safe":
	default:
		return fmt.Errorf("model %q has unknown restart %q (safe or unsafe)", m.ID, m.Restart)
	}
	return nil
}

func (h *HTTPDriver) validate() error {
	u, err := url.Parse(h.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("base_url %q is not an absolute http(s) URL", h.BaseURL)
	}
	if strings.TrimSpace(h.Start.Path) == "" {
		return fmt.Errorf("start call has no path")
	}
	if h.Credential != "" && !strings.HasPrefix(h.Credential, "cred://") {
		return fmt.Errorf("credential %q is not a cred:// reference", h.Credential)
	}
	for name, call := range map[string]*Call{
		"start": &h.Start, "cancel": h.Cancel, "lookup": h.Lookup,
	} {
		if call == nil {
			continue
		}
		if strings.TrimSpace(call.Path) == "" {
			return fmt.Errorf("%s call has no path", name)
		}
		if err := call.Response.validate(); err != nil {
			return fmt.Errorf("%s call: %w", name, err)
		}
	}
	if h.Status != nil {
		if err := h.Status.Response.validate(); err != nil {
			return fmt.Errorf("status call: %w", err)
		}
	}
	return nil
}

func (r Response) validate() error {
	for name, expr := range map[string]string{
		"id": r.ID, "state": r.State, "progress": r.Progress, "outputs": r.Outputs,
		"usage": r.Usage, "cost": r.Cost, "error": r.Error, "code": r.Code,
	} {
		if expr == "" {
			continue
		}
		if _, err := compileMapping(expr); err != nil {
			return fmt.Errorf("%s mapping: %w", name, err)
		}
	}
	return nil
}

func (c *CLIDriver) validate() error {
	if len(c.Command) == 0 {
		return fmt.Errorf("cli command is empty")
	}
	if len(c.Outputs) == 0 {
		return fmt.Errorf("cli has no output globs")
	}
	if c.Args != "" {
		if _, err := compileMapping(c.Args); err != nil {
			return fmt.Errorf("args mapping: %w", err)
		}
	}
	return nil
}

// Descriptors returns the catalog descriptors this manifest serves.
func (m *Manifest) Descriptors() ([]model.Descriptor, error) {
	out := make([]model.Descriptor, 0, len(m.Models))
	for _, ms := range m.Models {
		params, err := paramsSchema(ms.Params)
		if err != nil {
			return nil, fmt.Errorf("model %q: %w", ms.ID, err)
		}
		out = append(out, model.Descriptor{
			ID:       ms.ID,
			Provider: m.Name,
			Caps:     append([]model.Capability(nil), ms.Caps...),
			Params:   params,
			Inputs:   append([]model.InputSlot(nil), ms.Inputs...),
			Price:    ms.Price,
			Protocol: m.Protocol,
			Locality: m.Locality,
		})
	}
	return out, nil
}

func paramsSchema(params map[string]any) (json.RawMessage, error) {
	if len(params) == 0 {
		return json.RawMessage(`{"type":"object"}`), nil
	}
	b, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("params_schema is not JSON: %w", err)
	}
	return b, nil
}

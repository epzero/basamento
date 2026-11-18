package blob

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/epzero/basamento/core"
)

// URLHandler serves proxied blob read URLs for the embedded store.
type URLHandler struct {
	Store *Store
}

// ServeHTTP implements http.Handler.
func (h *URLHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if len(h.Store.signKey) == 0 {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	tenant, hex, ok := parseURLPath(r.URL.Path, h.Store.baseURL)
	if !ok || len(hex) != 64 {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	key := keyPrefix + hex
	sig := r.URL.Query().Get("sig")
	expStr := r.URL.Query().Get("exp")
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if sig == "" || err != nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if !verifyRead(h.Store.signKey, tenant, hex, sig, exp, h.Store.clock.Now()) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	ctx := core.WithTenant(r.Context(), core.TenantID(tenant))
	contentKey := key
	obj, err := h.Store.Get(ctx, contentKey)
	if err != nil {
		if core.CodeOf(err) == core.CodeNotFound {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	defer func() { _ = obj.Close() }()
	ctype, _ := h.Store.ContentType(ctx, core.TenantID(tenant), contentKey)
	serve := serveContentType(ctype, "")
	w.Header().Set("Content-Type", serve)
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = io.Copy(w, obj)
	}
}

func parseURLPath(p, base string) (tenant, hex string, ok bool) {
	p = strings.TrimPrefix(p, "/")
	parts := strings.Split(p, "/")
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// AlterURL applies test tampering to a proxied blob URL.
func AlterURL(raw, alteration, otherKey, otherTenant string, altSignKey []byte) (string, error) {
	u, err := parseProxied(raw)
	if err != nil {
		return "", err
	}
	switch alteration {
	case "its key replaced by the content key of \"world\"":
		hex, err := HexOf(otherKey)
		if err != nil {
			return "", err
		}
		u.hex = hex
	case "its expiry moved 1 hour later":
		u.exp += 3600
	case "its signature removed":
		u.sig = ""
	case "its signature replaced by one from another signing key":
		u.sig = signRead(altSignKey, u.tenant, u.hex, u.exp)
	case "its tenant replaced by \"globex\"":
		u.tenant = otherTenant
	default:
		return "", fmt.Errorf("unknown alteration %q", alteration)
	}
	return u.string(), nil
}

type proxiedURL struct {
	base             string
	tenant, hex, sig string
	exp              int64
}

func parseProxied(raw string) (*proxiedURL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("not a URL: %w", err)
	}
	pathPart := strings.TrimPrefix(u.Path, "/")
	pathPart = strings.TrimPrefix(pathPart, strings.Trim(path.Clean(HandlerRoute()), "/")+"/")
	tenant, hex, ok := parseURLPath(pathPart, "")
	if !ok {
		return nil, fmt.Errorf("bad path")
	}
	exp, _ := strconv.ParseInt(u.Query().Get("exp"), 10, 64)
	basePath := strings.TrimSuffix(u.Path, "/"+tenant+"/"+hex)
	return &proxiedURL{
		base:   fmt.Sprintf("%s://%s%s", u.Scheme, u.Host, basePath),
		tenant: tenant,
		hex:    hex,
		sig:    u.Query().Get("sig"),
		exp:    exp,
	}, nil
}

func (p *proxiedURL) string() string {
	key := keyPrefix + p.hex
	_ = key
	return fmt.Sprintf("%s/%s/%s?exp=%d&sig=%s", strings.TrimRight(p.base, "/"), p.tenant, p.hex, p.exp, p.sig)
}

// HandlerRoute returns the path prefix kit serve should mount.
func HandlerRoute() string { return "/blob" }

// Mount strips HandlerRoute from requests before ServeHTTP.
func (h *URLHandler) Mount() http.Handler {
	return http.StripPrefix(path.Clean(HandlerRoute()), h)
}

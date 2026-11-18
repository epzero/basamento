package blob

import (
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/epzero/basamento/core"
)

// URLHandler serves proxied blob read URLs for the embedded store.
type URLHandler struct {
	Store *Store
}

func setProxiedHeaders(w http.ResponseWriter) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

// ServeHTTP implements http.Handler.
func (h *URLHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setProxiedHeaders(w)
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
	if err := validateTenantSegment(tenant); err != nil {
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
	ctype, err := h.Store.ContentType(ctx, core.TenantID(tenant), contentKey)
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
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
	size, err := h.Store.contentLength(ctx, core.TenantID(tenant), contentKey)
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	serve := serveContentType(ctype, "")
	h.respondBody(w, r, obj, size, serve)
}

func (h *URLHandler) respondBody(w http.ResponseWriter, r *http.Request, body io.Reader, size int64, contentType string) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		if _, err := io.Copy(w, body); err != nil {
			panic(http.ErrAbortHandler)
		}
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

// HandlerRoute returns the path prefix kit serve should mount.
func HandlerRoute() string { return "/blob" }

// Mount strips HandlerRoute from requests before ServeHTTP.
func (h *URLHandler) Mount() http.Handler {
	return http.StripPrefix(path.Clean(HandlerRoute()), h)
}

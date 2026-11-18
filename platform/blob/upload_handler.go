package blob

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"gocloud.dev/blob"

	"github.com/epzero/basamento/core"
)

// UploadHandler serves proxied upload URLs for the embedded store (ADR-0013).
type UploadHandler struct {
	Store *Store
}

// UploadHandlerRoute is the path prefix for proxied upload URLs.
const UploadHandlerRoute = "/blob/upload"

func setUploadHeaders(w http.ResponseWriter) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

// ServeHTTP implements http.Handler.
func (h *UploadHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setUploadHeaders(w)
	if r.Method != http.MethodPut {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if len(h.Store.signKey) == 0 {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	tenant, stageKey, ok := parseUploadPath(r.URL.Path)
	if !ok {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if err := validateTenantSegment(tenant); err != nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	q := r.URL.Query()
	sig := q.Get("sig")
	expStr := q.Get("exp")
	maxStr := q.Get("max")
	ctEnc := q.Get("ct")
	if sig == "" || expStr == "" || maxStr == "" || ctEnc == "" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	max, err := strconv.ParseInt(maxStr, 10, 64)
	if err != nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	ct, err := url.QueryUnescape(ctEnc)
	if err != nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if !verifyUpload(h.Store.signKey, tenant, stageKey, sig, exp, max, ct, h.Store.clock.Now()) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	h.Store.maybeSweep(r.Context())
	tid := core.TenantID(tenant)
	meta, slotOK, err := h.Store.readSlot(r.Context(), tid, stageKey)
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if !slotOK || meta.expired(h.Store.clock.Now()) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if spent, err := h.Store.commitRecordExists(r.Context(), tid, stageKey); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	} else if spent {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if meta.Max != max || meta.ContentType != ct {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if cl := r.ContentLength; cl >= 0 && cl > max {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	reqCT := r.Header.Get("Content-Type")
	if reqCT == "" {
		reqCT = "application/octet-stream"
	}
	if reqCT != meta.ContentType {
		w.WriteHeader(http.StatusUnsupportedMediaType)
		return
	}
	stagePath, err := stageObjectPath(tid, stageKey)
	if err != nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if err := h.writeStaged(r.Context(), tid, stageKey, stagePath, meta.ContentType, max, r.Body); err != nil {
		if errors.Is(err, errBodyTooLarge) {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		if errors.Is(err, errSlotRefused) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

var (
	errBodyTooLarge = core.NewError(core.CodeInvalidArgument, "body too large", nil)
	errSlotRefused  = errors.New("staging slot refused")
)

// writeStaged reads the body without opening the staged object, then takes the
// per-slot lock and publishes only while the slot is still open (REQ-BLOB-025,
// REQ-BLOB-026). Bounded uploads buffer at most max+1 bytes in memory.
func (h *UploadHandler) writeStaged(ctx context.Context, tid core.TenantID, stageKey, stagePath, contentType string, max int64, body io.Reader) error {
	var buf bytes.Buffer
	limited := io.LimitReader(body, readLimit(max))
	n, err := io.Copy(&buf, limited)
	if err != nil {
		return mapError(err)
	}
	if n > max {
		return errBodyTooLarge
	}

	unlock := h.Store.stageLocks.lock(stageLockKey(tid, stageKey))
	defer unlock()

	meta, ok, err := h.Store.readSlot(ctx, tid, stageKey)
	if err != nil {
		return err
	}
	if !ok || meta.expired(h.Store.clock.Now()) || meta.ContentType != contentType || meta.Max != max {
		return errSlotRefused
	}
	spent, err := h.Store.commitRecordExists(ctx, tid, stageKey)
	if err != nil {
		return err
	}
	if spent {
		return errSlotRefused
	}
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	w, err := h.Store.bucket.NewWriter(wctx, stagePath, &blob.WriterOptions{ContentType: contentType})
	if err != nil {
		return mapError(err)
	}
	if _, err := io.Copy(w, &buf); err != nil {
		cancel()
		_ = w.Close()
		return mapError(err)
	}
	if err := w.Close(); err != nil {
		return mapError(err)
	}
	return nil
}

func parseUploadPath(p string) (tenant, stageKey string, ok bool) {
	p = strings.TrimPrefix(p, "/")
	parts := strings.Split(p, "/")
	if len(parts) != 3 || parts[0] != "upload" {
		return "", "", false
	}
	return parts[1], parts[2], ValidateStagingKey(parts[2])
}

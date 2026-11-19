package blob

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"gocloud.dev/blob"

	"github.com/epzero/basamento/core"
)

// UploadHandler serves proxied upload URLs for the embedded store (ADR-0013).
type UploadHandler struct {
	Store *Store
}

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
	ct := ctEnc
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

// writeStaged streams the body into a bucket writer without the per-slot lock.
// The object is not published until w.Close succeeds under the lock (REQ-BLOB-025,
// REQ-BLOB-026). Memory use is O(copy buffer), not O(max). A failed replay leaves
// any prior staged object in place; only a successful Close replaces it.
func (h *UploadHandler) writeStaged(ctx context.Context, tid core.TenantID, stageKey, stagePath, contentType string, max int64, body io.Reader) error {
	wctx, cancel := context.WithCancel(ctx)
	w, err := h.Store.bucket.NewWriter(wctx, stagePath, &blob.WriterOptions{ContentType: contentType})
	if err != nil {
		cancel()
		return mapError(err)
	}
	limited := io.LimitReader(body, readLimit(max))
	n, err := copyStagingBody(w, limited)
	if err != nil {
		abortStagingWriter(cancel, w)
		return mapError(err)
	}
	if n > max {
		abortStagingWriter(cancel, w)
		return errBodyTooLarge
	}

	unlock := h.Store.stageLocks.lock(stageLockKey(tid, stageKey))
	defer unlock()

	meta, ok, err := h.Store.readSlot(ctx, tid, stageKey)
	if err != nil {
		abortStagingWriter(cancel, w)
		return err
	}
	if !ok || meta.expired(h.Store.clock.Now()) || meta.ContentType != contentType || meta.Max != max {
		abortStagingWriter(cancel, w)
		return errSlotRefused
	}
	spent, err := h.Store.commitRecordExists(ctx, tid, stageKey)
	if err != nil {
		abortStagingWriter(cancel, w)
		return err
	}
	if spent {
		abortStagingWriter(cancel, w)
		return errSlotRefused
	}
	if err := w.Close(); err != nil {
		cancel()
		return mapError(err)
	}
	cancel()
	return nil
}

func abortStagingWriter(cancel context.CancelFunc, w *blob.Writer) {
	cancel()
	_ = w.Close()
}

const stagingCopyBuf = 32 * 1024

func copyStagingBody(w io.Writer, body io.Reader) (int64, error) {
	buf := make([]byte, stagingCopyBuf)
	var total int64
	for {
		nr, err := body.Read(buf)
		if nr > 0 {
			nw, werr := w.Write(buf[:nr])
			total += int64(nw)
			if werr != nil {
				return total, werr
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func parseUploadPath(p string) (tenant, stageKey string, ok bool) {
	p = strings.TrimPrefix(p, "/")
	parts := strings.Split(p, "/")
	if len(parts) != 3 || parts[0] != "upload" {
		return "", "", false
	}
	return parts[1], parts[2], ValidateStagingKey(parts[2])
}

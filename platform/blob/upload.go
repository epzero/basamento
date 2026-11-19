package blob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"gocloud.dev/blob"
	"gocloud.dev/gcerrors"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform"
)

const maxSlotMetaBytes = 4096

// StartUpload implements platform.Blob.
func (s *Store) StartUpload(ctx context.Context, o platform.UploadOptions) (platform.Upload, error) {
	tid, err := s.tenantOK(ctx)
	if err != nil {
		return platform.Upload{}, err
	}
	if err := validateUploadOptions(o, s.maxTTL); err != nil {
		return platform.Upload{}, err
	}
	s.maybeSweep(ctx)
	stageKey, err := newStagingKey(s.rand)
	if err != nil {
		return platform.Upload{}, core.NewError(core.CodeUnavailable, "upload unavailable", nil)
	}
	expires := s.clock.Now().Add(o.TTL)
	meta := slotMeta{Max: o.MaxSize, ContentType: o.ContentType, Expires: expires.UnixNano()}
	mp, err := slotMetaPath(tid, stageKey)
	if err != nil {
		return platform.Upload{}, err
	}
	raw, err := encodeSlotMeta(meta)
	if err != nil {
		return platform.Upload{}, core.NewError(core.CodeInternal, "upload unavailable", nil)
	}
	if err := s.bucket.WriteAll(ctx, mp, raw, nil); err != nil {
		return platform.Upload{}, mapError(err)
	}
	uploadURL, err := s.uploadURL(ctx, tid, stageKey, o, expires)
	if err != nil {
		_ = s.bucket.Delete(context.WithoutCancel(ctx), mp)
		return platform.Upload{}, err
	}
	return platform.Upload{URL: uploadURL, StagingKey: stageKey, Expires: expires}, nil
}

func (s *Store) uploadURL(ctx context.Context, tid core.TenantID, stageKey string, o platform.UploadOptions, expires time.Time) (string, error) {
	sp, err := stageObjectPath(tid, stageKey)
	if err != nil {
		return "", err
	}
	if s.embedded {
		if s.baseURL == "" || len(s.signKey) == 0 {
			return "", core.NewError(core.CodeInternal, "embedded blob URL signing not configured", nil)
		}
		return s.signedProxiedUpload(tid, stageKey, o, expires)
	}
	opts := &blob.SignedURLOptions{Method: http.MethodPut, Expiry: o.TTL, ContentType: o.ContentType}
	u, err := s.bucket.SignedURL(ctx, sp, opts)
	// When the driver cannot bind Content-Type on the presigned PUT, sign without it;
	// Commit still enforces the bound type (REQ-BLOB-034).
	if err != nil && o.ContentType != "" && gcerrors.Code(err) == gcerrors.Unimplemented {
		opts.ContentType = ""
		u, err = s.bucket.SignedURL(ctx, sp, opts)
	}
	if err != nil {
		return "", mapError(err)
	}
	return u, nil
}

func uploadURLExpUnix(expires time.Time) int64 {
	sec := expires.Unix()
	if expires.Nanosecond() > 0 {
		sec++
	}
	return sec
}

func (s *Store) signedProxiedUpload(tid core.TenantID, stageKey string, o platform.UploadOptions, expires time.Time) (string, error) {
	exp := uploadURLExpUnix(expires)
	sig := signUpload(s.signKey, string(tid), stageKey, exp, o.MaxSize, o.ContentType)
	base := strings.TrimRight(s.baseURL, "/")
	ct := url.QueryEscape(o.ContentType)
	seg := url.PathEscape(string(tid))
	return fmt.Sprintf("%s/upload/%s/%s?exp=%d&max=%d&ct=%s&sig=%s", base, seg, stageKey, exp, o.MaxSize, ct, sig), nil
}

// Commit implements platform.Blob. After the content blob is stored, it writes the
// commit record (which spends the slot) and then deletes the staged object. A failure
// to write the commit record returns unavailable and leaves the slot open so a retry
// can re-read the staged object and Put the same content again. Once the commit record
// is written, Commit succeeds with the content key even if deleting the staged object
// fails; the record prevents reuse and sweepSlot removes leftover staging keys at expiry.
func (s *Store) Commit(ctx context.Context, stagingKey string) (string, error) {
	tid, err := s.tenantOK(ctx)
	if err != nil {
		return "", err
	}
	if stagingKey == "" || !ValidateStagingKey(stagingKey) {
		return "", core.NewError(core.CodeInvalidArgument, "malformed staging key", nil)
	}
	s.maybeSweep(ctx)
	unlock := s.stageLocks.lock(stageLockKey(tid, stagingKey))
	defer unlock()

	meta, slotOK, err := s.readSlot(ctx, tid, stagingKey)
	if err != nil {
		return "", err
	}
	if !slotOK {
		return "", core.NewError(core.CodeNotFound, "staging key not found", nil)
	}
	if meta.expired(s.clock.Now()) {
		return "", core.NewError(core.CodeNotFound, "staging key not found", nil)
	}
	spent, err := s.commitRecordExists(ctx, tid, stagingKey)
	if err != nil {
		return "", err
	}
	if spent {
		return "", core.NewError(core.CodeNotFound, "staging key not found", nil)
	}
	stagePath, err := stageObjectPath(tid, stagingKey)
	if err != nil {
		return "", err
	}
	r, err := s.bucket.NewReader(ctx, stagePath, nil)
	if err != nil {
		if isNotFoundBucketErr(err) {
			return "", core.NewError(core.CodeNotFound, "staging key not found", nil)
		}
		return "", mapError(err)
	}
	defer func() { _ = r.Close() }()
	objCT := serveContentType(r.ContentType())
	if objCT != meta.ContentType {
		if err := s.spendFailedCommit(ctx, tid, stagingKey, stagePath); err != nil {
			return "", mapError(err)
		}
		return "", core.NewError(core.CodeInvalidArgument, "staged content type mismatch", nil)
	}
	h := sha256.New()
	limited := io.LimitReader(r, readLimit(meta.Max))
	n, err := io.Copy(h, limited)
	if err != nil {
		return "", mapCommitStoreErr(err)
	}
	if n > meta.Max {
		if err := s.spendFailedCommit(ctx, tid, stagingKey, stagePath); err != nil {
			return "", mapError(err)
		}
		return "", core.NewError(core.CodeInvalidArgument, "staged object over maximum size", nil)
	}
	r2, err := s.bucket.NewReader(ctx, stagePath, nil)
	if err != nil {
		return "", mapCommitStoreErr(err)
	}
	defer func() { _ = r2.Close() }()
	contentKey := keyPrefix + hex.EncodeToString(h.Sum(nil))
	if err := s.Put(ctx, contentKey, r2, meta.ContentType); err != nil {
		if ctx.Err() != nil {
			return "", err
		}
		if core.CodeOf(err) == core.CodeInvalidArgument || core.CodeOf(err) == core.CodeCancelled {
			return "", err
		}
		return "", mapError(err)
	}
	if err := s.finishSuccessfulCommit(ctx, tid, stagingKey, stagePath); err != nil {
		return "", err
	}
	return contentKey, nil
}

func mapCommitStoreErr(err error) error {
	if err == nil {
		return nil
	}
	if core.CodeOf(err) != "" {
		return err
	}
	return mapError(err)
}

func (s *Store) readSlot(ctx context.Context, tid core.TenantID, stageKey string) (slotMeta, bool, error) {
	mp, err := slotMetaPath(tid, stageKey)
	if err != nil {
		return slotMeta{}, false, err
	}
	rd, err := s.bucket.NewReader(ctx, mp, nil)
	if err != nil {
		if isNotFoundBucketErr(err) {
			return slotMeta{}, false, nil
		}
		return slotMeta{}, false, mapError(err)
	}
	defer func() { _ = rd.Close() }()
	lr := io.LimitReader(rd, maxSlotMetaBytes+1)
	b, err := io.ReadAll(lr)
	if err != nil {
		return slotMeta{}, false, mapError(err)
	}
	if int64(len(b)) > maxSlotMetaBytes {
		return slotMeta{}, false, core.NewError(core.CodeUnavailable, "corrupt staging slot", nil)
	}
	meta, err := decodeSlotMeta(b)
	if err != nil {
		return slotMeta{}, false, core.NewError(core.CodeUnavailable, "corrupt staging slot", nil)
	}
	return meta, true, nil
}

func (s *Store) commitRecordExists(ctx context.Context, tid core.TenantID, stageKey string) (bool, error) {
	cp, err := commitRecordPath(tid, stageKey)
	if err != nil {
		return false, err
	}
	ok, err := s.bucket.Exists(ctx, cp)
	if err != nil {
		return false, mapError(err)
	}
	return ok, nil
}

func (s *Store) spendFailedCommit(ctx context.Context, tid core.TenantID, stageKey, stagePath string) error {
	cp, err := commitRecordPath(tid, stageKey)
	if err != nil {
		return err
	}
	if err := s.bucket.Delete(context.WithoutCancel(ctx), stagePath); err != nil && !isNotFoundBucketErr(err) {
		return mapError(err)
	}
	if err := s.bucket.WriteAll(ctx, cp, nil, nil); err != nil {
		return mapError(err)
	}
	return nil
}

func (s *Store) finishSuccessfulCommit(ctx context.Context, tid core.TenantID, stageKey, stagePath string) error {
	cp, err := commitRecordPath(tid, stageKey)
	if err != nil {
		return mapError(err)
	}
	if err := s.bucket.WriteAll(ctx, cp, nil, nil); err != nil {
		return mapError(err)
	}
	// Best-effort: the slot is spent once the record exists; nothing reads the staged object again.
	// sweepSlot deletes the staged object, commit record and metadata when the slot expires.
	_ = s.bucket.Delete(ctx, stagePath)
	return nil
}

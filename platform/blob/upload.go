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

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform"
)

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
	now := s.clock.Now()
	expires := now.Add(o.TTL)
	meta := slotMeta{Max: o.MaxSize, ContentType: o.ContentType, Expires: expires.Unix()}
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
		_ = s.bucket.Delete(ctx, mp)
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
	if err != nil {
		opts.ContentType = ""
		u, err = s.bucket.SignedURL(ctx, sp, opts)
	}
	if err != nil {
		return "", mapError(err)
	}
	return u, nil
}

func (s *Store) signedProxiedUpload(tid core.TenantID, stageKey string, o platform.UploadOptions, expires time.Time) (string, error) {
	exp := expires.Unix()
	sig := signUpload(s.signKey, string(tid), stageKey, exp, o.MaxSize, o.ContentType)
	base := strings.TrimRight(s.baseURL, "/")
	ct := url.QueryEscape(o.ContentType)
	return fmt.Sprintf("%s/upload/%s/%s?exp=%d&max=%d&ct=%s&sig=%s", base, tid, stageKey, exp, o.MaxSize, ct, sig), nil
}

// Commit implements platform.Blob.
func (s *Store) Commit(ctx context.Context, stagingKey string) (string, error) {
	tid, err := s.tenantOK(ctx)
	if err != nil {
		return "", err
	}
	if stagingKey == "" || !ValidateStagingKey(stagingKey) {
		return "", core.NewError(core.CodeInvalidArgument, "malformed staging key", nil)
	}
	s.maybeSweep(ctx)
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
	if spent, err := s.commitRecordExists(ctx, tid, stagingKey); err != nil {
		return "", err
	} else if spent {
		return "", core.NewError(core.CodeNotFound, "staging key not found", nil)
	}
	stagePath, err := stageObjectPath(tid, stagingKey)
	if err != nil {
		return "", err
	}
	r, err := s.bucket.NewReader(ctx, stagePath, nil)
	if err != nil {
		if gcerr := mapError(err); gcerr != nil && core.CodeOf(gcerr) == core.CodeNotFound {
			return "", core.NewError(core.CodeNotFound, "staging key not found", nil)
		}
		return "", mapError(err)
	}
	defer func() { _ = r.Close() }()
	attrs, err := s.bucket.Attributes(ctx, stagePath)
	if err != nil {
		return "", mapError(err)
	}
	objCT := serveContentType("", attrs.ContentType)
	if objCT != meta.ContentType {
		_ = s.spendFailedCommit(ctx, tid, stagingKey, stagePath)
		return "", core.NewError(core.CodeInvalidArgument, "staged content type mismatch", nil)
	}
	h := sha256.New()
	limited := io.LimitReader(r, meta.Max+1)
	n, err := io.Copy(h, limited)
	if err != nil {
		return "", mapCommitStoreErr(err)
	}
	if n > meta.Max {
		_ = s.spendFailedCommit(ctx, tid, stagingKey, stagePath)
		return "", core.NewError(core.CodeInvalidArgument, "staged object over maximum size", nil)
	}
	// Re-read for Put after size check consumed the reader.
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
	ok, err := s.bucket.Exists(ctx, mp)
	if err != nil {
		return slotMeta{}, false, mapError(err)
	}
	if !ok {
		return slotMeta{}, false, nil
	}
	rd, err := s.bucket.NewReader(ctx, mp, nil)
	if err != nil {
		return slotMeta{}, false, mapError(err)
	}
	b, err := io.ReadAll(rd)
	_ = rd.Close()
	if err != nil {
		return slotMeta{}, false, mapError(err)
	}
	meta, err := decodeSlotMeta(b)
	if err != nil {
		return slotMeta{}, false, mapError(err)
	}
	return meta, true, nil
}

func (s *Store) commitRecordExists(ctx context.Context, tid core.TenantID, stageKey string) (bool, error) {
	cp, err := commitRecordPath(tid, stageKey)
	if err != nil {
		return false, err
	}
	return s.bucket.Exists(ctx, cp)
}

func (s *Store) spendFailedCommit(ctx context.Context, tid core.TenantID, stageKey, stagePath string) error {
	if err := s.bucket.Delete(ctx, stagePath); err != nil && core.CodeOf(mapError(err)) != core.CodeNotFound {
		return mapError(err)
	}
	cp, err := commitRecordPath(tid, stageKey)
	if err != nil {
		return err
	}
	return s.bucket.WriteAll(ctx, cp, nil, nil)
}

func (s *Store) finishSuccessfulCommit(ctx context.Context, tid core.TenantID, stageKey, stagePath string) error {
	cp, err := commitRecordPath(tid, stageKey)
	if err != nil {
		return mapError(err)
	}
	if err := s.bucket.WriteAll(ctx, cp, nil, nil); err != nil {
		return mapError(err)
	}
	if err := s.bucket.Delete(ctx, stagePath); err != nil {
		return mapError(err)
	}
	return nil
}

// WriteStagedObjectForTest writes a staged object bypassing the upload URL (conformance only).
func WriteStagedObjectForTest(ctx context.Context, s *Store, tid core.TenantID, stageKey string, body []byte, contentType string) error {
	if !ValidateStagingKey(stageKey) {
		return core.NewError(core.CodeInvalidArgument, "malformed staging key", nil)
	}
	path, err := stageObjectPath(tid, stageKey)
	if err != nil {
		return err
	}
	return s.bucket.WriteAll(ctx, path, body, &blob.WriterOptions{ContentType: contentType})
}

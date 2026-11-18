package blob

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/epzero/basamento/core"
)

// sweepRetryWait is how long to wait after a failed sweep before retrying.
const sweepRetryWait = time.Minute

func (s *Store) sweepAtOpen(ctx context.Context) {
	_ = s.runSweep(ctx)
}

func (s *Store) maybeSweep(ctx context.Context) {
	if !s.embedded {
		return
	}
	now := s.clock.Now()
	s.sweepMu.Lock()
	if !s.sweepRetryAt.IsZero() && now.Before(s.sweepRetryAt) {
		s.sweepMu.Unlock()
		return
	}
	if !s.lastSweep.IsZero() && now.Sub(s.lastSweep) < s.maxTTL {
		s.sweepMu.Unlock()
		return
	}
	s.sweepMu.Unlock()
	_ = s.runSweep(ctx)
}

func (s *Store) runSweep(ctx context.Context) error {
	err := s.sweepAllTenants(ctx, s.clock.Now())
	now := s.clock.Now()
	s.sweepMu.Lock()
	if err == nil {
		s.lastSweep = now
		s.sweepRetryAt = time.Time{}
	} else {
		s.sweepRetryAt = now.Add(sweepRetryWait)
	}
	s.sweepMu.Unlock()
	return err
}

// SweepExpiredStagingSlots deletes embedded staging slots past their expiry (REQ-BLOB-030).
func (s *Store) SweepExpiredStagingSlots(ctx context.Context) error {
	if !s.embedded {
		return nil
	}
	return s.runSweep(ctx)
}

func (s *Store) sweepAllTenants(ctx context.Context, now time.Time) error {
	tenants, err := s.bucket.ListTenantPrefixes(ctx)
	if err != nil {
		return mapError(err)
	}
	var joined []error
	seen := map[string]bool{}
	for _, tenant := range tenants {
		prefix := tenant + "/stage/"
		err := s.bucket.ListKeysCallback(ctx, prefix, func(k string) error {
			if !strings.HasSuffix(k, ".slot") {
				return nil
			}
			stageKey := strings.TrimSuffix(strings.TrimPrefix(k, prefix), ".slot")
			id := tenant + "/" + stageKey
			if seen[id] {
				return nil
			}
			seen[id] = true
			meta, ok, rerr := s.readSlot(ctx, core.TenantID(tenant), stageKey)
			if rerr != nil {
				return rerr
			}
			if !ok || !meta.expired(now) {
				return nil
			}
			if err := s.sweepSlot(ctx, core.TenantID(tenant), stageKey); err != nil {
				return err
			}
			return nil
		})
		if err != nil {
			joined = append(joined, err)
		}
	}
	return errors.Join(joined...)
}

func (s *Store) sweepSlot(ctx context.Context, tid core.TenantID, stageKey string) error {
	stagePath, err := stageObjectPath(tid, stageKey)
	if err != nil {
		return err
	}
	if err := s.bucket.Delete(ctx, stagePath); err != nil && !isNotFoundBucketErr(err) {
		return mapError(err)
	}
	cp, err := commitRecordPath(tid, stageKey)
	if err != nil {
		return err
	}
	if err := s.bucket.Delete(ctx, cp); err != nil && !isNotFoundBucketErr(err) {
		return mapError(err)
	}
	mp, err := slotMetaPath(tid, stageKey)
	if err != nil {
		return err
	}
	if err := s.bucket.Delete(ctx, mp); err != nil {
		return mapError(err)
	}
	return nil
}

func isNotFoundBucketErr(err error) bool {
	return err != nil && core.CodeOf(mapError(err)) == core.CodeNotFound
}

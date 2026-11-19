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
			tid := core.TenantID(tenant)
			if strings.HasSuffix(k, ".slot") {
				stageKey := strings.TrimSuffix(strings.TrimPrefix(k, prefix), ".slot")
				if !ValidateStagingKey(stageKey) {
					return nil
				}
				id := tenant + "/" + stageKey
				if seen[id] {
					return nil
				}
				seen[id] = true
				meta, ok, rerr := s.readSlot(ctx, tid, stageKey)
				if rerr != nil {
					joined = append(joined, rerr)
					return nil
				}
				if !ok || !meta.expired(now) {
					return nil
				}
				if err := s.sweepSlot(ctx, tid, stageKey); err != nil {
					joined = append(joined, err)
				}
				return nil
			}
			var stageKey string
			switch {
			case strings.HasSuffix(k, ".committed"):
				stageKey = strings.TrimSuffix(strings.TrimPrefix(k, prefix), ".committed")
			default:
				stageKey = strings.TrimPrefix(k, prefix)
			}
			if !ValidateStagingKey(stageKey) {
				return nil
			}
			mp, merr := slotMetaPath(tid, stageKey)
			if merr != nil {
				return nil
			}
			metaOK, err := s.bucket.Exists(ctx, mp)
			if err != nil {
				joined = append(joined, mapError(err))
				return nil
			}
			if metaOK {
				return nil
			}
			id := tenant + "/" + stageKey
			if seen[id] {
				return nil
			}
			seen[id] = true
			if err := s.sweepSlot(ctx, tid, stageKey); err != nil {
				joined = append(joined, err)
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
	now := s.clock.Now()
	unlock, ok := s.stageLocks.tryLock(stageLockKey(tid, stageKey))
	if !ok {
		return nil
	}
	defer unlock()
	meta, slotOK, err := s.readSlot(ctx, tid, stageKey)
	if err != nil {
		return err
	}
	if slotOK && !meta.expired(now) {
		return nil
	}
	if !slotOK {
		stagePath, err := stageObjectPath(tid, stageKey)
		if err != nil {
			return err
		}
		cp, err := commitRecordPath(tid, stageKey)
		if err != nil {
			return err
		}
		stageExists, err := s.bucket.Exists(ctx, stagePath)
		if err != nil {
			return mapError(err)
		}
		commitExists, err := s.bucket.Exists(ctx, cp)
		if err != nil {
			return mapError(err)
		}
		if !stageExists && !commitExists {
			return nil
		}
	}
	return s.deleteSlotContents(ctx, tid, stageKey)
}

func (s *Store) deleteSlotContents(ctx context.Context, tid core.TenantID, stageKey string) error {
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
	if err := s.bucket.Delete(ctx, mp); err != nil && !isNotFoundBucketErr(err) {
		return mapError(err)
	}
	return nil
}

func isNotFoundBucketErr(err error) bool {
	return err != nil && core.CodeOf(mapError(err)) == core.CodeNotFound
}

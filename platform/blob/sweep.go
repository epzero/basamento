package blob

import (
	"context"
	"strings"
	"time"

	"github.com/epzero/basamento/core"
)

func (s *Store) sweepAtOpen(ctx context.Context) {
	now := s.clock.Now()
	_ = s.sweepAllTenants(ctx, now)
	s.sweepMu.Lock()
	s.lastSweep = now
	s.sweepMu.Unlock()
}

func (s *Store) maybeSweep(ctx context.Context) {
	if !s.embedded {
		return
	}
	now := s.clock.Now()
	s.sweepMu.Lock()
	if !s.lastSweep.IsZero() && now.Sub(s.lastSweep) < s.maxTTL {
		s.sweepMu.Unlock()
		return
	}
	s.lastSweep = now
	s.sweepMu.Unlock()
	_ = s.sweepAllTenants(ctx, now)
}

// SweepExpiredStagingSlots deletes embedded staging slots past their expiry (REQ-BLOB-030).
func (s *Store) SweepExpiredStagingSlots(ctx context.Context) error {
	if !s.embedded {
		return nil
	}
	return s.sweepAllTenants(ctx, s.clock.Now())
}

func (s *Store) sweepAllTenants(ctx context.Context, now time.Time) error {
	keys, err := s.bucket.ListKeys(ctx, "")
	if err != nil {
		return mapError(err)
	}
	seen := map[string]bool{}
	for _, k := range keys {
		if !strings.HasSuffix(k, ".slot") {
			continue
		}
		parts := strings.Split(k, "/")
		if len(parts) < 3 || parts[1] != "stage" {
			continue
		}
		tid := core.TenantID(parts[0])
		stageKey := strings.TrimSuffix(parts[len(parts)-1], ".slot")
		if seen[string(tid)+"/"+stageKey] {
			continue
		}
		seen[string(tid)+"/"+stageKey] = true
		meta, ok, err := s.readSlot(ctx, tid, stageKey)
		if err != nil || !ok {
			continue
		}
		if !meta.expired(now) {
			continue
		}
		_ = s.sweepSlot(ctx, tid, stageKey)
	}
	return nil
}

func (s *Store) sweepSlot(ctx context.Context, tid core.TenantID, stageKey string) error {
	stagePath, err := stageObjectPath(tid, stageKey)
	if err != nil {
		return err
	}
	_ = s.bucket.Delete(ctx, stagePath)
	cp, err := commitRecordPath(tid, stageKey)
	if err != nil {
		return err
	}
	_ = s.bucket.Delete(ctx, cp)
	mp, err := slotMetaPath(tid, stageKey)
	if err != nil {
		return err
	}
	return s.bucket.Delete(ctx, mp)
}

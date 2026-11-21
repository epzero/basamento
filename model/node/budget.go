package node

import (
	"context"
	"fmt"
	"sync"

	"github.com/epzero/basamento/core"
)

// Budget is one run's spend account, in milli-points. The model node reserves
// a paid invocation's estimated cost before starting it and settles the
// reservation with the invocation's actual cost when it ends (REQ-MODEL-006).
//
// Implementations must be idempotent per key: the node may run its flow code
// again on replay and must not reserve or charge twice. The key is the
// invocation's client ref, so it names one logical invocation of one run.
type Budget interface {
	// Reserve holds amount under key. It fails with core.CodeBudgetExceeded
	// when the run's remaining budget cannot cover amount (REQ-MODEL-007).
	// Reserving the same key again is a no-op that keeps the first amount.
	Reserve(key string, amount int64) error
	// Settle closes key's reservation, charging actual milli-points and
	// releasing the rest. It is a no-op for a key with no open reservation;
	// the first settlement of a key wins.
	Settle(key string, actual int64) error
}

// budgetKey carries the run's budget through the run context. The composition
// root sets it on the context it starts the run with; the runtime hands that
// context to every step of the run.
type budgetKey struct{}

// WithBudget returns a context carrying b as the run's budget. A later call
// replaces the earlier one; the parent context is unchanged.
func WithBudget(ctx context.Context, b Budget) context.Context {
	return context.WithValue(ctx, budgetKey{}, b)
}

// BudgetFrom returns the budget carried by ctx, and whether there is one. A
// run without a budget cannot start a priced invocation: every priced call
// reserves the estimate before it starts (REQ-MODEL-006), so the node fails
// one before it reaches the provider. Only a model with no price runs
// unmetered.
func BudgetFrom(ctx context.Context) (Budget, bool) {
	if ctx == nil {
		return nil, false
	}
	b, ok := ctx.Value(budgetKey{}).(Budget)
	return b, ok && b != nil
}

// Memory is an in-process Budget for tests and the embedded profile. It is
// safe for concurrent use and records every reservation so a caller can read
// back what was held and what was charged per key. A settled charge keeps
// counting against the limit: settlement frees the reservation, not the
// money.
type Memory struct {
	mu      sync.Mutex
	limit   int64
	held    int64
	charged int64
	entries map[string]*entry
}

type entry struct {
	amount   int64
	settled  bool
	charged  int64
	released bool
}

// NewMemory returns a budget that allows spending up to limit milli-points.
// A negative limit is unlimited.
func NewMemory(limit int64) *Memory {
	return &Memory{limit: limit, entries: map[string]*entry{}}
}

// Limit is the budget's ceiling in milli-points; a negative limit is
// unlimited.
func (m *Memory) Limit() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.limit
}

// Reserve implements Budget.
func (m *Memory) Reserve(key string, amount int64) error {
	if key == "" {
		return core.NewError(core.CodeInvalidArgument, "reservation needs a key", nil)
	}
	if amount < 0 {
		return core.NewError(core.CodeInvalidArgument, "reservation amount is negative", map[string]any{"amount": amount})
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if e := m.entries[key]; e != nil {
		return nil
	}
	if m.limit >= 0 && m.held+amount > m.limit-m.charged {
		return core.NewError(core.CodeBudgetExceeded,
			fmt.Sprintf("reservation of %d milli-points exceeds the run's remaining budget", amount),
			map[string]any{
				"remaining": m.limit - m.charged - m.held,
				"estimate":  amount,
				"charged":   m.charged,
				"held":      m.held,
			})
	}
	m.entries[key] = &entry{amount: amount}
	m.held += amount
	return nil
}

// Settle implements Budget. The first settlement of a key wins, so a replayed
// flow does not charge twice.
func (m *Memory) Settle(key string, actual int64) error {
	if key == "" {
		return core.NewError(core.CodeInvalidArgument, "settlement needs a key", nil)
	}
	if actual < 0 {
		return core.NewError(core.CodeInvalidArgument, "settlement amount is negative", map[string]any{"amount": actual})
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.entries[key]
	if e == nil || e.settled {
		return nil
	}
	e.settled = true
	e.charged = actual
	e.released = actual < e.amount
	m.held -= e.amount
	m.charged += actual
	return nil
}

// Held reports the amount currently held under key, zero when nothing is held.
func (m *Memory) Held(key string) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e := m.entries[key]; e != nil && !e.settled {
		return e.amount
	}
	return 0
}

// Charged reports what was charged under key, zero before settlement.
func (m *Memory) Charged(key string) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e := m.entries[key]; e != nil && e.settled {
		return e.charged
	}
	return 0
}

// Reserved reports the amount that was reserved under key, settled or not.
func (m *Memory) Reserved(key string) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e := m.entries[key]; e != nil {
		return e.amount
	}
	return 0
}

// Settled reports whether key's reservation was closed.
func (m *Memory) Settled(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.entries[key]
	return e != nil && e.settled
}

// HeldTotal is the sum of every open reservation.
func (m *Memory) HeldTotal() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.held
}

// ChargedTotal is the sum of every settlement.
func (m *Memory) ChargedTotal() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.charged
}

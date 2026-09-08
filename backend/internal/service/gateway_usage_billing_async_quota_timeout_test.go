//go:build unit

package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"

	"github.com/stretchr/testify/require"
)

// capturingQuotaRepo records deadline/err of the context at IncrementUsageWithReset call time.
type capturingQuotaRepo struct {
	mu             sync.Mutex
	errAtCall      error
	deadlineAtCall time.Time
	hasDeadline    bool
	done           chan struct{}
}

func (c *capturingQuotaRepo) GetByUserPlatform(context.Context, int64, string) (*UserPlatformQuotaRecord, error) {
	return nil, nil
}
func (c *capturingQuotaRepo) BulkInsertInitial(context.Context, []UserPlatformQuotaRecord) error {
	return nil
}
func (c *capturingQuotaRepo) IncrementUsageWithReset(ctx context.Context, _ int64, _ string, _ float64, _ time.Time) error {
	c.mu.Lock()
	c.errAtCall = ctx.Err()
	c.deadlineAtCall, c.hasDeadline = ctx.Deadline()
	c.mu.Unlock()
	close(c.done)
	return nil
}
func (c *capturingQuotaRepo) ListByUser(context.Context, int64) ([]UserPlatformQuotaRecord, error) {
	return nil, nil
}
func (c *capturingQuotaRepo) UpsertForUser(context.Context, int64, []UserPlatformQuotaRecord) error {
	return nil
}
func (c *capturingQuotaRepo) ResetExpiredWindow(context.Context, int64, string, string, time.Time) error {
	return nil
}
func (c *capturingQuotaRepo) BatchSnapshotUsage(context.Context, []UserPlatformQuotaSnapshot, time.Time) error {
	return nil
}

func TestDetachedBillingContext_BoundsDeadline(t *testing.T) {
	t.Parallel()

	parent, cancel := context.WithCancel(context.Background())
	cancel() // client disconnected

	ctx, release := detachedBillingContext(parent)
	defer release()

	require.NoError(t, ctx.Err(), "detached billing ctx must not inherit parent cancel")
	deadline, ok := ctx.Deadline()
	require.True(t, ok, "detached billing ctx must have a deadline")
	remaining := time.Until(deadline)
	require.Greater(t, remaining, postUsageBillingTimeout-time.Second)
	require.LessOrEqual(t, remaining, postUsageBillingTimeout)
}

func TestFinalizePostUsageBilling_AsyncQuotaWriteUsesTimeout(t *testing.T) {
	t.Parallel()

	repo := &capturingQuotaRepo{done: make(chan struct{})}
	cfg := &config.Config{}
	deps := &billingDeps{
		billingCacheService:   &BillingCacheService{cfg: cfg}, // cache==nil → HasLimit true, Incr no-op
		deferredService:       &DeferredService{},
		userPlatformQuotaRepo: repo,
		cfg:                   nil, // flusher disabled → async DB path
	}
	p := &postUsageBillingParams{
		Cost:     &CostBreakdown{TotalCost: 1.0, ActualCost: 1.0},
		User:     &User{ID: 42},
		Account:  &Account{ID: 7},
		Platform: "anthropic",
	}

	parent, cancel := context.WithCancel(context.Background())
	cancel() // simulate client disconnect before async quota write

	finalizePostUsageBilling(parent, p, deps, &UsageBillingApplyResult{Applied: true})

	select {
	case <-repo.done:
	case <-time.After(2 * time.Second):
		t.Fatal("async IncrementUsageWithReset was not called")
	}

	repo.mu.Lock()
	errAtCall := repo.errAtCall
	deadline := repo.deadlineAtCall
	hasDeadline := repo.hasDeadline
	repo.mu.Unlock()

	require.NoError(t, errAtCall, "async quota ctx must survive client cancel")
	require.True(t, hasDeadline, "async quota ctx must be bounded by postUsageBillingTimeout")
	remaining := time.Until(deadline)
	require.Greater(t, remaining, postUsageBillingTimeout-time.Second)
	require.LessOrEqual(t, remaining, postUsageBillingTimeout)
}

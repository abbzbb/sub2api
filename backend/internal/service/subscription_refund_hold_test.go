//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
	"github.com/Wei-Shaw/sub2api/ent/usersubscription"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

// Database adapter only: all expiry decisions run through SubscriptionService.
type refundSubscriptionTestRepo struct {
	UserSubscriptionRepository
	db         *dbent.Client
	failExpiry bool
}

func (r *refundSubscriptionTestRepo) client(ctx context.Context) *dbent.Client {
	if tx := dbent.TxFromContext(ctx); tx != nil {
		return tx.Client()
	}
	return r.db
}

func (r *refundSubscriptionTestRepo) GetByID(ctx context.Context, id int64) (*UserSubscription, error) {
	sub, err := r.client(ctx).UserSubscription.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return refundTestSubscription(sub), nil
}

func (r *refundSubscriptionTestRepo) GetByIDForUpdate(ctx context.Context, id int64) (*UserSubscription, error) {
	sub, err := r.client(ctx).UserSubscription.Query().Unique(false).Where(usersubscription.IDEQ(id), func(q *sql.Selector) {
		if q.Dialect() == dialect.Postgres {
			q.ForUpdate()
		}
	}).Only(ctx)
	if err != nil {
		return nil, err
	}
	return refundTestSubscription(sub), nil
}

func refundTestSubscription(sub *dbent.UserSubscription) *UserSubscription {
	return &UserSubscription{ID: sub.ID, UserID: sub.UserID, GroupID: sub.GroupID,
		StartsAt: sub.StartsAt, ExpiresAt: sub.ExpiresAt, Status: sub.Status, Notes: psStringValue(sub.Notes)}
}

func (r *refundSubscriptionTestRepo) ExtendExpiry(ctx context.Context, id int64, expiry time.Time) error {
	if r.failExpiry {
		return errors.New("injected expiry failure")
	}
	return r.client(ctx).UserSubscription.UpdateOneID(id).SetExpiresAt(expiry).Exec(ctx)
}

func (r *refundSubscriptionTestRepo) UpdateStatus(ctx context.Context, id int64, status string) error {
	return r.client(ctx).UserSubscription.UpdateOneID(id).SetStatus(status).Exec(ctx)
}

func (r *refundSubscriptionTestRepo) Update(ctx context.Context, sub *UserSubscription) error {
	return r.client(ctx).UserSubscription.UpdateOneID(sub.ID).SetStartsAt(sub.StartsAt).
		SetExpiresAt(sub.ExpiresAt).SetStatus(sub.Status).SetNotes(sub.Notes).Exec(ctx)
}

func (r *refundSubscriptionTestRepo) Delete(ctx context.Context, id int64) error {
	return r.client(ctx).UserSubscription.DeleteOneID(id).Exec(ctx)
}

func subscriptionRefundFixture(t *testing.T) (*dbent.Client, *PaymentService, *RefundPlan, *heldRefundProvider, *SubscriptionService, *dbent.UserSubscription, time.Time) {
	t.Helper()
	db, svc, plan, prov := heldRefundFixture(t)
	return addRefundTestSubscription(t, db, svc, plan, prov)
}

func addRefundTestSubscription(t *testing.T, db *dbent.Client, svc *PaymentService, plan *RefundPlan, prov *heldRefundProvider) (*dbent.Client, *PaymentService, *RefundPlan, *heldRefundProvider, *SubscriptionService, *dbent.UserSubscription, time.Time) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	g, err := db.Group.Create().SetName("subscription refund renewal").Save(ctx)
	require.NoError(t, err)
	sub, err := db.UserSubscription.Create().SetUserID(plan.Order.UserID).SetGroupID(g.ID).
		SetStartsAt(now).SetExpiresAt(now.AddDate(0, 0, 20)).Save(ctx)
	require.NoError(t, err)
	plan.Order, err = db.PaymentOrder.UpdateOne(plan.Order).SetOrderType(payment.OrderTypeSubscription).
		SetSubscriptionGroupID(g.ID).SetSubscriptionDays(30).Save(ctx)
	require.NoError(t, err)
	plan.DeductionType = payment.DeductionTypeSubscription
	plan.BalanceToDeduct = 0
	plan.SubDaysToDeduct = 30
	plan.SubscriptionID = sub.ID
	subSvc := &SubscriptionService{userSubRepo: &refundSubscriptionTestRepo{db: db}, entClient: db,
		now: func() time.Time { return now }}
	return db, svc, plan, prov, subSvc, sub, now
}

func TestRefundSubscriptionMultipleHoldsReturnOnlyRetainedTerm(t *testing.T) {
	ctx := context.Background()
	db, svc, plan, prov, subSvc, sub, now := subscriptionRefundFixture(t)
	_, err := svc.ExecuteRefund(ctx, plan)
	require.NoError(t, err)
	o := createPendingRefundOrderForTest(t, ctx, db, "second-subscription-refund")
	o, err = db.PaymentOrder.UpdateOne(o).SetUserID(plan.Order.UserID).SetStatus(OrderStatusCompleted).
		SetOrderType(payment.OrderTypeSubscription).SetSubscriptionGroupID(sub.GroupID).SetSubscriptionDays(10).Save(ctx)
	require.NoError(t, err)
	second := &RefundPlan{OrderID: o.ID, Order: o, RefundAmount: 100, GatewayAmount: 100,
		DeductionType: payment.DeductionTypeSubscription, SubscriptionID: sub.ID, SubDaysToDeduct: 10}
	_, err = svc.ExecuteRefund(ctx, second)
	require.NoError(t, err)
	_, err = subSvc.ExtendSubscription(ctx, sub.ID, 40)
	require.NoError(t, err)
	prov.queryStatus = payment.ProviderStatusFailed
	for _, id := range []int64{second.OrderID, plan.OrderID} {
		_, err = svc.QueryAndFinalizeRefund(ctx, id)
		require.NoError(t, err)
	}
	final, err := db.UserSubscription.Get(ctx, sub.ID)
	require.NoError(t, err)
	require.True(t, final.ExpiresAt.Equal(now.AddDate(0, 0, 60)), "expiry = %s", final.ExpiresAt)
}

func TestRefundSubscriptionRenewalReturnsOnlyUnabsorbedTerm(t *testing.T) {
	for _, tc := range []struct {
		name      string
		path      string
		remaining int
		want      int
	}{
		{"expired by hold adjustment", "adjust", 20, 60},
		{"expired by hold assignment", "assign", 20, 60},
		{"already expired before hold", "adjust", -5, 40},
		{"still active after hold", "adjust", 80, 120},
		{"replacement term", "restart", 20, 40},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db, svc, plan, prov, subSvc, sub, now := subscriptionRefundFixture(t)
			require.NoError(t, db.UserSubscription.UpdateOneID(sub.ID).SetExpiresAt(now.AddDate(0, 0, tc.remaining)).Exec(ctx))
			_, err := svc.ExecuteRefund(ctx, plan)
			require.NoError(t, err)
			switch tc.path {
			case "adjust":
				_, err = subSvc.ExtendSubscription(ctx, sub.ID, 40)
			case "assign":
				err = subSvc.updateExistingSubscriptionTerm(ctx, sub.ID, 40, "", true)
			case "restart":
				err = subSvc.restartExistingSubscriptionTerm(ctx, sub.ID, 40, "")
			}
			require.NoError(t, err)
			prov.queryStatus = payment.ProviderStatusFailed
			_, err = svc.QueryAndFinalizeRefund(ctx, plan.OrderID)
			require.NoError(t, err)
			final, err := db.UserSubscription.Get(ctx, sub.ID)
			require.NoError(t, err)
			require.True(t, final.ExpiresAt.Equal(now.AddDate(0, 0, tc.want)), "expiry = %s", final.ExpiresAt)
		})
	}
}

func TestRefundSubscriptionRepeatedExpiredRenewal(t *testing.T) {
	ctx := context.Background()
	db, svc, plan, prov, subSvc, sub, now := subscriptionRefundFixture(t)
	_, err := svc.ExecuteRefund(ctx, plan)
	require.NoError(t, err)
	_, err = subSvc.ExtendSubscription(ctx, sub.ID, 40)
	require.NoError(t, err)
	subSvc.now = func() time.Time { return now.AddDate(0, 0, 45) }
	_, err = subSvc.ExtendSubscription(ctx, sub.ID, 40)
	require.NoError(t, err)
	prov.queryStatus = payment.ProviderStatusFailed
	_, err = svc.QueryAndFinalizeRefund(ctx, plan.OrderID)
	require.NoError(t, err)
	final, err := db.UserSubscription.Get(ctx, sub.ID)
	require.NoError(t, err)
	require.True(t, final.ExpiresAt.Equal(now.AddDate(0, 0, 100)))
}

func TestRefundSubscriptionRenewalDuringProviderCalls(t *testing.T) {
	for _, phase := range []string{"refund", "query failure", "query success"} {
		t.Run(phase, func(t *testing.T) {
			ctx := context.Background()
			db, svc, plan, prov, subSvc, sub, now := subscriptionRefundFixture(t)
			renew := func() {
				_, err := subSvc.ExtendSubscription(ctx, sub.ID, 40)
				require.NoError(t, err)
			}
			if phase == "refund" {
				prov.onRefund = renew
			} else {
				prov.onQuery = renew
			}
			_, err := svc.ExecuteRefund(ctx, plan)
			require.NoError(t, err)
			prov.queryStatus = payment.ProviderStatusFailed
			want := 60
			if phase == "query success" {
				prov.queryStatus = payment.ProviderStatusSuccess
				want = 40
			}
			_, err = svc.QueryAndFinalizeRefund(ctx, plan.OrderID)
			require.NoError(t, err)
			final, err := db.UserSubscription.Get(ctx, sub.ID)
			require.NoError(t, err)
			require.True(t, final.ExpiresAt.Equal(now.AddDate(0, 0, want)), "expiry = %s", final.ExpiresAt)
			o, err := db.PaymentOrder.Get(ctx, plan.OrderID)
			require.NoError(t, err)
			require.False(t, o.RefundState.DeductionHeld)
			require.Nil(t, o.RefundState.SubscriptionReturnNanos)
		})
	}
}

func TestRefundSubscriptionRenewalAdjustmentRollsBackWithTerm(t *testing.T) {
	ctx := context.Background()
	db, svc, plan, _, subSvc, sub, _ := subscriptionRefundFixture(t)
	_, err := svc.ExecuteRefund(ctx, plan)
	require.NoError(t, err)
	subSvc.userSubRepo.(*refundSubscriptionTestRepo).failExpiry = true
	_, err = subSvc.ExtendSubscription(ctx, sub.ID, 40)
	require.ErrorContains(t, err, "injected expiry failure")
	o, err := db.PaymentOrder.Get(ctx, plan.OrderID)
	require.NoError(t, err)
	require.Nil(t, o.RefundState.SubscriptionReturnNanos)
	final, err := db.UserSubscription.Get(ctx, sub.ID)
	require.NoError(t, err)
	require.True(t, final.ExpiresAt.Equal(*o.RefundState.SubscriptionExpiryHeld))
}

func TestRefundSubscriptionFailureAfterRevocation(t *testing.T) {
	ctx := context.Background()
	db, svc, plan, prov, subSvc, sub, now := subscriptionRefundFixture(t)
	_, err := svc.ExecuteRefund(ctx, plan)
	require.NoError(t, err)
	require.NoError(t, subSvc.RevokeSubscription(ctx, sub.ID))
	replacement, err := db.UserSubscription.Create().SetUserID(sub.UserID).SetGroupID(sub.GroupID).
		SetStartsAt(now).SetExpiresAt(now.AddDate(0, 0, 40)).Save(ctx)
	require.NoError(t, err)
	prov.queryStatus = payment.ProviderStatusFailed
	_, err = svc.QueryAndFinalizeRefund(ctx, plan.OrderID)
	require.NoError(t, err)
	o, err := db.PaymentOrder.Get(ctx, plan.OrderID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefundFailed, o.Status)
	require.False(t, o.RefundState.DeductionHeld)
	_, err = db.UserSubscription.Get(ctx, sub.ID)
	require.True(t, dbent.IsNotFound(err), "refund failure must not resurrect the revoked subscription")
	deleted, err := db.UserSubscription.Get(mixins.SkipSoftDelete(ctx), sub.ID)
	require.NoError(t, err)
	require.True(t, deleted.ExpiresAt.Equal(sub.ExpiresAt))
	require.NotNil(t, deleted.DeletedAt)
	current, err := db.UserSubscription.Get(ctx, replacement.ID)
	require.NoError(t, err)
	require.True(t, current.ExpiresAt.Equal(replacement.ExpiresAt))
	count, err := db.PaymentAuditLog.Query().Where(paymentauditlog.ActionEQ("REFUND_FAILED")).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	_, err = svc.QueryAndFinalizeRefund(ctx, plan.OrderID)
	require.Error(t, err, "failure settlement must not release the hold twice")
}

func TestRefundSubscriptionRevokedReleaseRollsBackOnAuditFailure(t *testing.T) {
	ctx := context.Background()
	db, svc, plan, prov, subSvc, sub, _ := subscriptionRefundFixture(t)
	_, err := svc.ExecuteRefund(ctx, plan)
	require.NoError(t, err)
	require.NoError(t, subSvc.RevokeSubscription(ctx, sub.ID))
	prov.queryStatus = payment.ProviderStatusFailed
	db.PaymentAuditLog.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(context.Context, ent.Mutation) (ent.Value, error) {
			return nil, errors.New("injected failure audit")
		})
	})
	_, err = svc.QueryAndFinalizeRefund(ctx, plan.OrderID)
	require.ErrorContains(t, err, "injected failure audit")
	o, err := db.PaymentOrder.Get(ctx, plan.OrderID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefundPending, o.Status)
	require.True(t, o.RefundState.DeductionHeld)
	deleted, err := db.UserSubscription.Get(mixins.SkipSoftDelete(ctx), sub.ID)
	require.NoError(t, err)
	require.True(t, deleted.ExpiresAt.Equal(*o.RefundState.SubscriptionExpiryHeld))
}

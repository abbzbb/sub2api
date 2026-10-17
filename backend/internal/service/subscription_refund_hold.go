package service

import (
	"context"
	"math"
	"time"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"
	"entgo.io/ent/dialect/sql/sqljson"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// Called with the subscription row locked, before its expiry is changed. A
// renewal that starts from now (or replaces the term) can absorb some or all of
// a refund hold. Retain only the difference from the same renewal without the
// hold, rather than returning the original number of days on a later failure.
func (s *SubscriptionService) adjustRefundSubscriptionReturns(ctx context.Context, sub *UserSubscription, after time.Time, renew func(time.Time) time.Time) error {
	if s.entClient == nil {
		return nil
	}
	tx := dbent.TxFromContext(ctx)
	if tx == nil {
		return ErrSubscriptionAssignConflict
	}
	orders, err := tx.PaymentOrder.Query().Unique(false).Where(paymentorder.UserIDEQ(sub.UserID), func(q *sql.Selector) {
		q.Where(sql.And(
			sqljson.ValueEQ(paymentorder.FieldRefundState, true, sqljson.Path("deduction_held")),
			sqljson.ValueEQ(paymentorder.FieldRefundState, sub.ID, sqljson.Path("subscription_id")),
		))
	}).Order(paymentorder.ByID()).Where(func(q *sql.Selector) {
		if q.Dialect() == dialect.Postgres {
			q.ForUpdate()
		}
	}).All(ctx)
	if err != nil {
		return err
	}
	var total time.Duration
	for _, o := range orders {
		if err := validateRefundRecovery(o.RefundState); err != nil {
			return err
		}
		amount := subscriptionRefundReturn(o.RefundState)
		if amount < 0 || amount > time.Duration(math.MaxInt64)-total {
			return infraerrors.Conflict("REFUND_STATE_UNKNOWN", "subscription refund return is invalid; reconciliation required")
		}
		total += amount
	}
	if total == 0 {
		return nil
	}
	baseline := renew(sub.ExpiresAt.Add(total))
	if baseline.After(MaxExpiresAt) {
		baseline = MaxExpiresAt
	}
	remaining := baseline.Sub(after)
	if remaining < 0 {
		remaining = 0
	}
	if remaining >= total {
		return nil
	}
	// Allocate the retained entitlement in stable order for multiple holds.
	for _, o := range orders {
		state := *o.RefundState
		keep := subscriptionRefundReturn(&state)
		if keep > remaining {
			keep = remaining
		}
		remaining -= keep
		nanos := int64(keep)
		state.SubscriptionReturnNanos = &nanos
		if err := tx.PaymentOrder.UpdateOneID(o.ID).SetRefundState(&state).Exec(ctx); err != nil {
			return err
		}
	}
	return nil
}

func subscriptionRefundReturn(state *domain.PaymentRefundState) time.Duration {
	if state == nil || !state.DeductionHeld || state.SubscriptionExpiryHeld == nil {
		return 0
	}
	if state.SubscriptionReturnNanos != nil {
		return time.Duration(*state.SubscriptionReturnNanos)
	}
	expiry := *state.SubscriptionExpiryHeld
	return expiry.AddDate(0, 0, state.SubDaysToDeduct).Sub(expiry)
}

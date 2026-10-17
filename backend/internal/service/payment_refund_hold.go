package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/ent/predicate"
	"github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/ent/usersubscription"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// Reserve before contacting the provider, in the same transaction as the
// operation ID. Response loss and retries must keep the reservation intact.
func (s *PaymentService) reserveRefundDeduction(ctx context.Context, tx *dbent.Tx, p *RefundPlan, state *domain.PaymentRefundState) error {
	if state.DeductionHeld {
		return nil
	}
	if state.DeductionType == payment.DeductionTypeBalance && state.BalanceToDeduct > 0 {
		n, err := tx.User.Update().Where(user.IDEQ(p.Order.UserID), user.DeletedAtIsNil(), user.BalanceGTE(state.BalanceToDeduct)).
			AddBalance(-state.BalanceToDeduct).AddFrozenBalance(state.BalanceToDeduct).Save(ctx)
		if err != nil {
			return err
		}
		if n != 1 {
			return infraerrors.Conflict("REFUND_BALANCE_CHANGED", "balance changed before refund reservation; prepare refund again")
		}
		state.BalanceHeld = state.BalanceToDeduct
	}
	if state.DeductionType == payment.DeductionTypeSubscription && state.SubscriptionID > 0 && state.SubDaysToDeduct > 0 {
		sub, err := tx.UserSubscription.Get(ctx, state.SubscriptionID)
		if err != nil {
			return err
		}
		if sub.UserID != p.Order.UserID || sub.DeletedAt != nil {
			return infraerrors.Conflict("REFUND_SUBSCRIPTION_CHANGED", "subscription changed before refund reservation")
		}
		days := state.SubDaysToDeduct
		if days > MaxValidityDays {
			days = MaxValidityDays
		}
		expiry := sub.ExpiresAt.AddDate(0, 0, -days)
		status := sub.Status
		if status == SubscriptionStatusActive && !expiry.After(time.Now()) {
			status = SubscriptionStatusExpired
		}
		n, err := tx.UserSubscription.Update().Where(usersubscription.IDEQ(sub.ID), usersubscription.DeletedAtIsNil(),
			usersubscription.ExpiresAtEQ(sub.ExpiresAt), usersubscription.StatusEQ(sub.Status)).
			SetExpiresAt(expiry).SetStatus(status).Save(ctx)
		if err != nil {
			return err
		}
		if n != 1 {
			return infraerrors.Conflict("REFUND_SUBSCRIPTION_CHANGED", "subscription changed before refund reservation")
		}
		state.SubDaysToDeduct = days
		state.SubscriptionExpiryHeld = &expiry
		state.SubscriptionStatusBefore = sub.Status
	}
	// Older binaries reject v2 instead of treating reserved funds as an unpaid debit.
	state.Version = 2
	state.DeductionHeld = true
	return nil
}

// The caller must first claim the order in this transaction. This fences
// concurrent queries, late provider responses and duplicate failure callbacks.
func (s *PaymentService) settleRefundDeduction(ctx context.Context, tx *dbent.Tx, o *dbent.PaymentOrder, state *domain.PaymentRefundState, success bool) error {
	if state == nil || !state.DeductionHeld {
		return nil
	}
	if state.BalanceHeld > 0 {
		update := tx.User.Update().Where(user.IDEQ(o.UserID), user.FrozenBalanceGTE(state.BalanceHeld)).AddFrozenBalance(-state.BalanceHeld)
		if !success {
			update.AddBalance(state.BalanceHeld)
		}
		n, err := update.Save(ctx)
		if err != nil {
			return err
		}
		if n != 1 {
			return infraerrors.Conflict("REFUND_HOLD_MISSING", "reserved refund balance is missing; reconciliation required")
		}
	}
	if !success && state.SubscriptionExpiryHeld != nil {
		sub, err := tx.UserSubscription.Get(ctx, state.SubscriptionID)
		if err != nil {
			return err
		}
		// Add back only the held days, preserving any intervening renewal.
		expiry := sub.ExpiresAt.AddDate(0, 0, state.SubDaysToDeduct)
		status := sub.Status
		if status == SubscriptionStatusExpired && state.SubscriptionStatusBefore == SubscriptionStatusActive && expiry.After(time.Now()) {
			status = SubscriptionStatusActive
		}
		n, err := tx.UserSubscription.Update().Where(usersubscription.IDEQ(sub.ID), usersubscription.DeletedAtIsNil(),
			usersubscription.ExpiresAtEQ(sub.ExpiresAt), usersubscription.StatusEQ(sub.Status)).
			SetExpiresAt(expiry).SetStatus(status).Save(ctx)
		if err != nil {
			return err
		}
		if n != 1 {
			return infraerrors.Conflict("REFUND_SUBSCRIPTION_CHANGED", "subscription changed while returning refund reservation")
		}
	}
	state.DeductionHeld = false
	state.BalanceHeld = 0
	state.SubscriptionExpiryHeld = nil
	return nil
}

func (s *PaymentService) invalidateRefundHoldCaches(ctx context.Context, o *dbent.PaymentOrder, state *domain.PaymentRefundState) {
	if state.DeductionType == payment.DeductionTypeBalance {
		s.invalidateRefundBalanceCache(ctx, o.UserID)
	}
	if state.DeductionType == payment.DeductionTypeSubscription && s.subscriptionSvc != nil && o.SubscriptionGroupID != nil {
		_ = s.subscriptionSvc.invalidateSubscriptionCaches(o.UserID, *o.SubscriptionGroupID)
	}
}

func (s *PaymentService) failRefundWithHold(ctx context.Context, o *dbent.PaymentOrder, state *domain.PaymentRefundState, cause error, claim predicate.PaymentOrder) (*RefundResult, error) {
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	n, err := tx.PaymentOrder.Update().Where(paymentorder.IDEQ(o.ID), claim).
		SetStatus(OrderStatusRefundFailed).SetFailedAt(time.Now()).SetFailedReason(psErrMsg(cause)).Save(ctx)
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, infraerrors.Conflict("CONFLICT", "order status changed")
	}
	if state != nil {
		copy := *state
		state = &copy
		if err := s.settleRefundDeduction(ctx, tx, o, state, false); err != nil {
			return nil, err
		}
		state.ProviderStatus = payment.ProviderStatusFailed
		if err := tx.PaymentOrder.UpdateOneID(o.ID).SetRefundState(state).Exec(ctx); err != nil {
			return nil, err
		}
	}
	detail, _ := json.Marshal(map[string]string{"detail": psErrMsg(cause)})
	if err := tx.PaymentAuditLog.Create().SetOrderID(fmt.Sprint(o.ID)).SetAction("REFUND_FAILED").
		SetDetail(string(detail)).SetOperator("admin").Exec(ctx); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if state != nil {
		s.invalidateRefundHoldCaches(ctx, o, state)
	}
	return &RefundResult{Success: false, Warning: "gateway refund failed: " + psErrMsg(cause)}, nil
}

package service

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"

	"entgo.io/ent/dialect/sql"
	"entgo.io/ent/dialect/sql/sqljson"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/ent/predicate"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/google/uuid"
)

func refundRecoveryFromPlan(p *RefundPlan, resp *payment.RefundResponse) *domain.PaymentRefundState {
	state := &domain.PaymentRefundState{
		Version: 1, AttemptID: uuid.NewString(), ProviderStatus: strings.TrimSpace(resp.Status), RefundID: refundResponseID(resp),
		DeductionType: p.DeductionType, BalanceToDeduct: p.BalanceToDeduct,
		SubDaysToDeduct: p.SubDaysToDeduct, SubscriptionID: p.SubscriptionID,
	}
	if state.DeductionType == "" {
		state.DeductionType = payment.DeductionTypeNone
	}
	return state
}

func applyRefundRecoveryPlan(p *RefundPlan, state *domain.PaymentRefundState) {
	p.DeductionType = state.DeductionType
	p.DeductBalance = state.DeductionType != payment.DeductionTypeNone
	p.BalanceToDeduct = state.BalanceToDeduct
	p.SubDaysToDeduct = state.SubDaysToDeduct
	p.SubscriptionID = state.SubscriptionID
}

func (s *PaymentService) prepareLegacyRefundDeduction(ctx context.Context, p *RefundPlan) error {
	// A pre-upgrade failed rollback means the old attempt already took the debit.
	alreadyDeducted, err := s.entClient.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(p.OrderID, 10)), paymentauditlog.ActionEQ("REFUND_ROLLBACK_FAILED")).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("read legacy refund deduction: %w", err)
	}
	if alreadyDeducted {
		p.DeductionType = payment.DeductionTypeNone
		p.DeductBalance = false
		p.BalanceToDeduct = 0
		p.SubDaysToDeduct = 0
		p.SubscriptionID = 0
	}
	return nil
}

func (s *PaymentService) persistRefundRecovery(ctx context.Context, p *RefundPlan, resp *payment.RefundResponse) (*domain.PaymentRefundState, error) {
	state := refundRecoveryFromPlan(p, resp)
	if err := validateRefundRecovery(state); err != nil {
		return nil, err
	}
	updated, err := s.entClient.PaymentOrder.Update().
		Where(paymentorder.IDEQ(p.OrderID), paymentorder.StatusEQ(OrderStatusRefunding)).
		SetStatus(OrderStatusRefundPending).
		SetRefundState(state).
		SetRefundAmount(p.RefundAmount).
		SetRefundReason(p.Reason).
		SetForceRefund(p.Force).
		ClearRefundAt().ClearFailedAt().ClearFailedReason().Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("persist refund recovery: %w", err)
	}
	if updated == 0 {
		return nil, infraerrors.Conflict("CONFLICT", "order status changed")
	}
	return state, nil
}

func (s *PaymentService) loadRefundRecovery(ctx context.Context, o *dbent.PaymentOrder) (*domain.PaymentRefundState, error) {
	if o.RefundState != nil {
		state := *o.RefundState
		if err := validateRefundRecovery(&state); err != nil {
			return nil, err
		}
		return &state, nil
	}
	// Legacy pending orders have no structured recovery state. An explicit audit
	// outcome may be used; missing/corrupt evidence must remain pending for reconciliation.
	legacy := s.latestRefundPendingDetail(ctx, o.ID)
	if !legacy.Known {
		return nil, infraerrors.Conflict("REFUND_STATE_UNKNOWN", "refund recovery state is missing; manual reconciliation required")
	}
	plan := s.refundFinalizePlan(o)
	if !legacy.DeductionRollbackOK {
		plan.DeductionType = payment.DeductionTypeNone
		plan.BalanceToDeduct = 0
		plan.SubDaysToDeduct = 0
	} else if o.OrderType == payment.OrderTypeSubscription {
		if early := s.prepDeduct(ctx, o, plan, true); early != nil {
			return nil, infraerrors.Conflict("REFUND_STATE_UNKNOWN", "cannot recover subscription deduction; manual reconciliation required")
		}
	}
	state := refundRecoveryFromPlan(plan, &payment.RefundResponse{Status: payment.ProviderStatusPending, RefundID: legacy.RefundID})
	return state, validateRefundRecovery(state)
}

// A query result may only change the still-unconfirmed attempt it observed.
func refundQueryStatePredicate(o *dbent.PaymentOrder) predicate.PaymentOrder {
	if o.RefundState == nil {
		return paymentorder.RefundStateIsNil()
	}
	return func(s *sql.Selector) {
		s.Where(sql.And(
			sqljson.ValueEQ(paymentorder.FieldRefundState, o.RefundState.AttemptID, sqljson.Path("attempt_id")),
			sqljson.ValueEQ(paymentorder.FieldRefundState, payment.ProviderStatusPending, sqljson.Path("provider_status")),
		))
	}
}

func validateRefundRecovery(state *domain.PaymentRefundState) error {
	validStatus := state.ProviderStatus == payment.ProviderStatusPending || state.ProviderStatus == payment.ProviderStatusSuccess || state.ProviderStatus == payment.ProviderStatusRefunded
	validDeduction := state.DeductionType == payment.DeductionTypeNone || state.DeductionType == payment.DeductionTypeBalance || state.DeductionType == payment.DeductionTypeSubscription
	if state.Version != 1 || strings.TrimSpace(state.AttemptID) == "" || !validStatus || !validDeduction || state.BalanceToDeduct < 0 || math.IsNaN(state.BalanceToDeduct) || math.IsInf(state.BalanceToDeduct, 0) || state.SubDaysToDeduct < 0 || state.SubscriptionID < 0 {
		return infraerrors.Conflict("REFUND_STATE_UNKNOWN", "refund recovery state is invalid; manual reconciliation required")
	}
	return nil
}

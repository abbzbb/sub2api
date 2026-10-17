package service

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

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
	state := refundRequestStateFromPlan(p)
	state.ProviderStatus = strings.TrimSpace(resp.Status)
	state.RefundID = refundResponseID(resp)
	if p.Order != nil && p.Order.RefundState != nil {
		prev := p.Order.RefundState
		if state.OutRequestNo == "" {
			state.OutRequestNo = strings.TrimSpace(prev.OutRequestNo)
		}
		if strings.TrimSpace(prev.AttemptID) != "" && strings.TrimSpace(prev.OutRequestNo) == state.OutRequestNo {
			state.AttemptID = prev.AttemptID
			state.Version = prev.Version
			state.DeductionHeld = prev.DeductionHeld
			state.BalanceHeld = prev.BalanceHeld
			state.SubscriptionExpiryHeld = prev.SubscriptionExpiryHeld
			state.SubscriptionStatusBefore = prev.SubscriptionStatusBefore
			state.SubscriptionReturnNanos = prev.SubscriptionReturnNanos
		}
		if strings.TrimSpace(prev.RefundID) != "" && state.RefundID == "" {
			state.RefundID = prev.RefundID
		}
	}
	return state
}

func refundRequestIDToReuse(state *domain.PaymentRefundState) string {
	if state == nil {
		return ""
	}
	id := strings.TrimSpace(state.OutRequestNo)
	if id == "" {
		return ""
	}
	switch strings.TrimSpace(state.ProviderStatus) {
	case "", payment.ProviderStatusPending:
		return id
	default:
		return ""
	}
}

func newRefundOutRequestNo(orderID string) string {
	orderID = strings.TrimSpace(orderID)
	if orderID == "" {
		orderID = "refund"
	}
	return fmt.Sprintf("%s-refund-%d", orderID, time.Now().UnixNano())
}

func refundRequestStateFromPlan(p *RefundPlan) *domain.PaymentRefundState {
	state := &domain.PaymentRefundState{
		Version:         1,
		AttemptID:       uuid.NewString(),
		ProviderStatus:  payment.ProviderStatusPending,
		OutRequestNo:    strings.TrimSpace(p.IdempotencyKey),
		RefundAmount:    p.RefundAmount,
		GatewayAmount:   p.GatewayAmount,
		Currency:        refundPlanCurrency(p),
		PriorStatus:     refundRestoreStatus(p),
		DeductionType:   refundDeductionType(p.DeductionType),
		BalanceToDeduct: p.BalanceToDeduct,
		SubDaysToDeduct: p.SubDaysToDeduct,
		SubscriptionID:  p.SubscriptionID,
	}
	if p.Order != nil && p.Order.RefundState != nil {
		prev := p.Order.RefundState
		if strings.TrimSpace(prev.OutRequestNo) == state.OutRequestNo && strings.TrimSpace(prev.AttemptID) != "" {
			state.AttemptID = prev.AttemptID
			state.Version = prev.Version
			state.DeductionHeld = prev.DeductionHeld
			state.BalanceHeld = prev.BalanceHeld
			state.SubscriptionExpiryHeld = prev.SubscriptionExpiryHeld
			state.SubscriptionStatusBefore = prev.SubscriptionStatusBefore
			state.SubscriptionReturnNanos = prev.SubscriptionReturnNanos
			if rid := strings.TrimSpace(prev.RefundID); rid != "" {
				state.RefundID = rid
			}
		}
		if state.PriorStatus == OrderStatusCompleted && strings.TrimSpace(prev.PriorStatus) != "" {
			state.PriorStatus = prev.PriorStatus
		}
	}
	return state
}

func refundPlanCurrency(p *RefundPlan) string {
	if p == nil || p.Order == nil {
		return payment.DefaultPaymentCurrency
	}
	return PaymentOrderCurrency(p.Order)
}

func refundDeductionType(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return payment.DeductionTypeNone
	}
	return v
}

func hasInFlightPendingRefund(state *domain.PaymentRefundState) bool {
	if state == nil || strings.TrimSpace(state.OutRequestNo) == "" {
		return false
	}
	switch strings.TrimSpace(state.ProviderStatus) {
	case "", payment.ProviderStatusPending:
		return true
	default:
		return false
	}
}

func refundPlanMatchesPersistedOperation(p *RefundPlan, state *domain.PaymentRefundState) error {
	if p == nil || state == nil {
		return nil
	}
	if state.RefundAmount == 0 && state.GatewayAmount == 0 && strings.TrimSpace(state.Currency) == "" {
		return nil
	}
	currency := strings.TrimSpace(state.Currency)
	if currency == "" {
		currency = refundPlanCurrency(p)
	}
	tol := paymentAmountToleranceForCurrency(currency)
	if math.Abs(p.RefundAmount-state.RefundAmount) > tol || math.Abs(p.GatewayAmount-state.GatewayAmount) > tol {
		return infraerrors.Conflict("REFUND_IN_FLIGHT", "in-flight refund amount does not match the persisted operation")
	}
	if c := strings.TrimSpace(state.Currency); c != "" && !strings.EqualFold(c, refundPlanCurrency(p)) {
		return infraerrors.Conflict("REFUND_IN_FLIGHT", "in-flight refund currency does not match the persisted operation")
	}
	if refundDeductionType(p.DeductionType) != refundDeductionType(state.DeductionType) ||
		math.Abs(p.BalanceToDeduct-state.BalanceToDeduct) > tol ||
		p.SubDaysToDeduct != state.SubDaysToDeduct ||
		p.SubscriptionID != state.SubscriptionID {
		return infraerrors.Conflict("REFUND_IN_FLIGHT", "in-flight refund deduction does not match the persisted operation")
	}
	return nil
}

func (s *PaymentService) ensureRefundIdempotencyKey(ctx context.Context, p *RefundPlan) error {
	if p == nil || p.Order == nil {
		return fmt.Errorf("refund plan is missing order")
	}
	current, err := s.entClient.PaymentOrder.Get(ctx, p.OrderID)
	if err != nil {
		return fmt.Errorf("load refund idempotency state: %w", err)
	}
	// Reload credentials/state but keep the pre-lock PriorStatus.
	prior := p.PriorStatus
	p.Order = current
	p.PriorStatus = prior
	if strings.TrimSpace(p.IdempotencyKey) == "" {
		if id := refundRequestIDToReuse(current.RefundState); id != "" {
			if err := refundPlanMatchesPersistedOperation(p, current.RefundState); err != nil {
				return err
			}
			p.IdempotencyKey = id
			applyRefundRecoveryPlan(p, current.RefundState)
		} else {
			p.IdempotencyKey = newRefundOutRequestNo(current.OutTradeNo)
		}
	}
	return s.persistRefundRequestID(ctx, p)
}

func (s *PaymentService) persistRefundRequestID(ctx context.Context, p *RefundPlan) error {
	state := refundRequestStateFromPlan(p)
	if err := validateRefundRecovery(state); err != nil {
		return err
	}
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockRefundSubscription(ctx, tx, state); err != nil {
		return err
	}
	current, err := tx.PaymentOrder.Get(ctx, p.OrderID)
	if err != nil {
		return err
	}
	copyRefundHold(state, current.RefundState)
	updated, err := tx.PaymentOrder.Update().
		Where(refundInFlightRestorePredicate(p)).
		SetRefundState(state).SetRefundAmount(p.RefundAmount).
		SetRefundReason(p.Reason).SetForceRefund(p.Force).Save(ctx)
	if err != nil {
		return fmt.Errorf("persist refund request id: %w", err)
	}
	if updated == 0 {
		return infraerrors.Conflict("CONFLICT", "order status changed")
	}
	if err := s.reserveRefundDeduction(ctx, tx, p, state); err != nil {
		return err
	}
	if err := tx.PaymentOrder.UpdateOneID(p.OrderID).SetRefundState(state).Exec(ctx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	p.Order.RefundState = state
	s.invalidateRefundHoldCaches(ctx, p.Order, state)
	return nil
}

func applyRefundRecoveryPlan(p *RefundPlan, state *domain.PaymentRefundState) {
	p.DeductionType = state.DeductionType
	p.DeductBalance = state.DeductionType != payment.DeductionTypeNone
	p.BalanceToDeduct = state.BalanceToDeduct
	p.SubDaysToDeduct = state.SubDaysToDeduct
	p.SubscriptionID = state.SubscriptionID
	if strings.TrimSpace(state.OutRequestNo) != "" {
		p.IdempotencyKey = strings.TrimSpace(state.OutRequestNo)
	}
	if state.RefundAmount > 0 {
		p.RefundAmount = state.RefundAmount
	}
	if state.GatewayAmount > 0 {
		p.GatewayAmount = state.GatewayAmount
	}
	if ps := strings.TrimSpace(state.PriorStatus); ps != "" && strings.TrimSpace(p.PriorStatus) == "" {
		p.PriorStatus = ps
	}
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
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	updated, err := tx.PaymentOrder.Update().
		Where(refundInFlightRestorePredicate(p)).
		SetStatus(OrderStatusRefundPending).
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
	current, err := tx.PaymentOrder.Get(ctx, p.OrderID)
	if err != nil {
		return nil, err
	}
	copyRefundHold(state, current.RefundState)
	if err := tx.PaymentOrder.UpdateOneID(p.OrderID).SetRefundState(state).Exec(ctx); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return state, nil
}

// Provider calls can overlap renewal. Preserve the latest transactional return
// amount rather than writing the pre-call snapshot back over it.
func copyRefundHold(dst, src *domain.PaymentRefundState) {
	if src == nil || dst.AttemptID != src.AttemptID {
		return
	}
	dst.Version = src.Version
	dst.DeductionHeld = src.DeductionHeld
	dst.BalanceHeld = src.BalanceHeld
	dst.SubscriptionExpiryHeld = src.SubscriptionExpiryHeld
	dst.SubscriptionStatusBefore = src.SubscriptionStatusBefore
	dst.SubscriptionReturnNanos = src.SubscriptionReturnNanos
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
	status := strings.TrimSpace(state.ProviderStatus)
	validStatus := status == payment.ProviderStatusPending || status == payment.ProviderStatusSuccess || status == payment.ProviderStatusRefunded || status == payment.ProviderStatusFailed
	validDeduction := state.DeductionType == payment.DeductionTypeNone || state.DeductionType == payment.DeductionTypeBalance || state.DeductionType == payment.DeductionTypeSubscription
	validHold := state.BalanceHeld >= 0 && !math.IsNaN(state.BalanceHeld) && !math.IsInf(state.BalanceHeld, 0)
	if state.BalanceHeld > 0 {
		validHold = validHold && state.DeductionHeld && state.DeductionType == payment.DeductionTypeBalance && math.Abs(state.BalanceHeld-state.BalanceToDeduct) < 1e-9
	}
	if state.DeductionHeld && state.DeductionType == payment.DeductionTypeBalance && state.BalanceToDeduct > 0 {
		validHold = validHold && math.Abs(state.BalanceHeld-state.BalanceToDeduct) < 1e-9
	}
	if state.SubscriptionExpiryHeld != nil {
		validHold = validHold && state.DeductionHeld && state.DeductionType == payment.DeductionTypeSubscription && state.SubscriptionID > 0 && state.SubDaysToDeduct > 0
	}
	if state.SubscriptionReturnNanos != nil {
		validHold = validHold && state.DeductionHeld && state.SubscriptionExpiryHeld != nil && *state.SubscriptionReturnNanos >= 0
		if state.SubscriptionExpiryHeld != nil {
			expiry := *state.SubscriptionExpiryHeld
			validHold = validHold && time.Duration(*state.SubscriptionReturnNanos) <= expiry.AddDate(0, 0, state.SubDaysToDeduct).Sub(expiry)
		}
	}
	if !validHold {
		return infraerrors.Conflict("REFUND_STATE_UNKNOWN", "refund reservation state is invalid; reconciliation required")
	}
	if (state.Version != 1 && state.Version != 2) || strings.TrimSpace(state.AttemptID) == "" || !validStatus || !validDeduction || state.BalanceToDeduct < 0 || math.IsNaN(state.BalanceToDeduct) || math.IsInf(state.BalanceToDeduct, 0) || state.SubDaysToDeduct < 0 || state.SubscriptionID < 0 {
		return infraerrors.Conflict("REFUND_STATE_UNKNOWN", "refund recovery state is invalid; manual reconciliation required")
	}
	return nil
}

func (s *PaymentService) persistRefundConfirmation(ctx context.Context, o *dbent.PaymentOrder, state *domain.PaymentRefundState) error {
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	updated, err := tx.PaymentOrder.Update().
		Where(paymentorder.IDEQ(o.ID), refundConfirmationStatusPredicate(o), refundQueryStatePredicate(o)).
		SetStatus(OrderStatusRefundPending).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("persist refund confirmation: %w", err)
	}
	if updated == 0 {
		return infraerrors.Conflict("CONFLICT", "order status changed")
	}
	current, err := tx.PaymentOrder.Get(ctx, o.ID)
	if err != nil {
		return err
	}
	copyRefundHold(state, current.RefundState)
	if err := tx.PaymentOrder.UpdateOneID(o.ID).SetRefundState(state).Exec(ctx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	o.Status = OrderStatusRefundPending
	o.RefundState = state
	return nil
}

func (s *PaymentService) persistRefundTerminalFailure(ctx context.Context, p *RefundPlan, gErr error) error {
	if p == nil || p.Order == nil {
		return fmt.Errorf("refund plan is missing order")
	}
	state := p.Order.RefundState
	if state == nil {
		state = refundRequestStateFromPlan(p)
	} else {
		copy := *state
		state = &copy
	}
	state.ProviderStatus = payment.ProviderStatusFailed
	if err := validateRefundRecovery(state); err != nil {
		return err
	}
	_, err := s.failRefundWithHold(ctx, p.Order, state, gErr, refundInFlightRestorePredicate(p))
	if infraerrors.Reason(err) == "CONFLICT" {
		return nil
	}
	return err
}

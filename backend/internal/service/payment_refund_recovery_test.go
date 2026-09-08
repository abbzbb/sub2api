//go:build unit

package service

import (
	"context"
	"errors"
	"strconv"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestRefundRecoveryRetriesLedgerWithoutProviderQuery(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "recovery-no-query")
	order, err := client.PaymentOrder.UpdateOne(order).SetStatus(OrderStatusCompleted).Save(ctx)
	require.NoError(t, err)
	_, err = client.User.UpdateOneID(order.UserID).SetBalance(100).Save(ctx)
	require.NoError(t, err)
	providerCalls := 0
	restore := replacePaymentProviderFactoryForTest(t, refundSuccessProviderTestDouble{
		onRefund: func() { providerCalls++ },
	})
	defer restore()
	failDeduction := true
	svc := &PaymentService{
		entClient:    client,
		loadBalancer: &captureLoadBalancer{},
		userRepo: &mockUserRepo{deductAvailableBalanceFn: func(ctx context.Context, id int64, amount float64) (float64, error) {
			tx := dbent.TxFromContext(ctx)
			if tx == nil {
				return 0, errors.New("deduction requires order transaction")
			}
			_, err := tx.Client().User.UpdateOneID(id).AddBalance(-amount).Save(ctx)
			if err != nil {
				return 0, err
			}
			if failDeduction {
				return 0, errors.New("injected failure after balance update")
			}
			return amount, nil
		}},
	}
	plan := &RefundPlan{
		OrderID: order.ID, Order: order, RefundAmount: 100, GatewayAmount: 100,
		Reason: "recovery", DeductBalance: true, DeductionType: payment.DeductionTypeBalance,
		BalanceToDeduct: 100,
	}
	result, err := svc.ExecuteRefund(ctx, plan)
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Equal(t, 1, providerCalls)
	user, err := client.User.Get(ctx, order.UserID)
	require.NoError(t, err)
	require.Equal(t, 100.0, user.Balance, "failed finalization must roll back the debit")

	// Recovery must not depend on the best-effort audit log surviving.
	_, err = client.PaymentAuditLog.Delete().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10))).Exec(ctx)
	require.NoError(t, err)
	failDeduction = false
	result, err = svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, 100.0, result.BalanceDeducted)
	require.Equal(t, 1, providerCalls)
	user, err = client.User.Get(ctx, order.UserID)
	require.NoError(t, err)
	require.Zero(t, user.Balance)
	_, err = svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.Error(t, err, "a completed refund cannot be debited twice")
	require.Equal(t, 1, providerCalls)
}

func TestRefundRecoveryPendingCannotIssueAnotherRefund(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "recovery-no-repeat")
	providerCalls := 0
	restore := replacePaymentProviderFactoryForTest(t, refundSuccessProviderTestDouble{
		onRefund: func() { providerCalls++ },
	})
	defer restore()
	svc := &PaymentService{entClient: client, loadBalancer: &captureLoadBalancer{}}
	_, _, err := svc.PrepareRefund(ctx, order.ID, 100, "retry", false, false)
	require.Error(t, err)
	_, err = svc.ExecuteRefund(ctx, &RefundPlan{OrderID: order.ID, Order: order, RefundAmount: 100})
	require.Error(t, err)
	require.Zero(t, providerCalls)
}

func TestRefundRecoveryChecksLegacyAuditBeforeGateway(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "audit-read-failure")
	order, err := client.PaymentOrder.UpdateOne(order).SetStatus(OrderStatusCompleted).Save(ctx)
	require.NoError(t, err)
	client.PaymentAuditLog.Intercept(dbent.InterceptFunc(func(dbent.Querier) dbent.Querier {
		return dbent.QuerierFunc(func(context.Context, dbent.Query) (dbent.Value, error) {
			return nil, errors.New("audit read unavailable")
		})
	}))
	providerCalls := 0
	restore := replacePaymentProviderFactoryForTest(t, refundSuccessProviderTestDouble{
		onRefund: func() { providerCalls++ },
	})
	defer restore()
	svc := &PaymentService{entClient: client, loadBalancer: &captureLoadBalancer{}}
	result, err := svc.ExecuteRefund(ctx, &RefundPlan{OrderID: order.ID, Order: order, RefundAmount: 100, GatewayAmount: 100})
	require.ErrorContains(t, err, "audit read unavailable")
	require.Nil(t, result)
	require.Zero(t, providerCalls)
	current, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, current.Status)
}

func TestRefundRecoveryRejectsStaleFailure(t *testing.T) {
	for _, status := range []string{OrderStatusRefundPending, OrderStatusRefunded} {
		t.Run(status, func(t *testing.T) {
			ctx := context.Background()
			client := newPaymentConfigServiceTestClient(t)
			staleOrder := createPendingRefundOrderForTest(t, ctx, client, "stale-failure")
			state := &domain.PaymentRefundState{Version: 1, AttemptID: "confirmed", ProviderStatus: payment.ProviderStatusSuccess, DeductionType: payment.DeductionTypeNone}
			_, err := client.PaymentOrder.UpdateOneID(staleOrder.ID).
				SetStatus(status).SetRefundState(state).Save(ctx)
			require.NoError(t, err)

			svc := &PaymentService{entClient: client}
			result, err := svc.finalizeRefundFailed(ctx, staleOrder, errors.New("stale provider failure"))
			require.Error(t, err)
			require.Nil(t, result)
			current, err := client.PaymentOrder.Get(ctx, staleOrder.ID)
			require.NoError(t, err)
			require.Equal(t, status, current.Status)
			require.Equal(t, payment.ProviderStatusSuccess, current.RefundState.ProviderStatus)
			require.False(t, svc.hasAuditLog(ctx, staleOrder.ID, "REFUND_FAILED"))
		})
	}
}

func TestRefundRecoveryRejectsQueriesForReplacedAttempt(t *testing.T) {
	for _, responseStatus := range []string{payment.ProviderStatusSuccess, payment.ProviderStatusFailed} {
		t.Run(responseStatus, func(t *testing.T) {
			ctx := context.Background()
			client := newPaymentConfigServiceTestClient(t)
			order := createPendingRefundOrderForTest(t, ctx, client, "replaced-attempt")
			state := refundRecoveryFromPlan(&RefundPlan{DeductionType: payment.DeductionTypeNone}, &payment.RefundResponse{Status: payment.ProviderStatusPending})
			_, err := client.PaymentOrder.UpdateOne(order).SetRefundState(state).Save(ctx)
			require.NoError(t, err)
			replacement := *state
			replacement.AttemptID = "replacement-attempt"
			restore := replacePaymentProviderFactoryForTest(t, &refundRecoveryQueryHook{
				refundQueryProviderTestDouble: refundQueryProviderTestDouble{refundResponse: &payment.RefundResponse{Status: responseStatus}},
				onQuery: func() {
					_, err := client.PaymentOrder.UpdateOneID(order.ID).SetRefundState(&replacement).Save(ctx)
					require.NoError(t, err)
				},
			})
			defer restore()
			svc := &PaymentService{entClient: client, loadBalancer: &captureLoadBalancer{}}
			result, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
			require.Error(t, err)
			require.Nil(t, result)
			current, err := client.PaymentOrder.Get(ctx, order.ID)
			require.NoError(t, err)
			require.Equal(t, OrderStatusRefundPending, current.Status)
			require.Equal(t, &replacement, current.RefundState)
			require.False(t, svc.hasAuditLog(ctx, order.ID, "REFUND_FAILED"))
		})
	}
}

type refundRecoveryQueryHook struct {
	refundQueryProviderTestDouble
	onQuery func()
}

func (p *refundRecoveryQueryHook) QueryRefund(context.Context, payment.RefundQueryRequest) (*payment.RefundResponse, error) {
	p.onQuery()
	return p.refundResponse, nil
}

func TestRefundRecoveryDoesNotReadAuditAfterGatewayAcceptance(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "audit-after-gateway")
	order, err := client.PaymentOrder.UpdateOne(order).SetStatus(OrderStatusCompleted).Save(ctx)
	require.NoError(t, err)
	providerCalled := false
	client.PaymentAuditLog.Intercept(dbent.InterceptFunc(func(next dbent.Querier) dbent.Querier {
		return dbent.QuerierFunc(func(ctx context.Context, query dbent.Query) (dbent.Value, error) {
			if providerCalled {
				return nil, errors.New("post-gateway audit reads unavailable")
			}
			return next.Query(ctx, query)
		})
	}))
	restore := replacePaymentProviderFactoryForTest(t, refundSuccessProviderTestDouble{
		onRefund: func() { providerCalled = true },
	})
	defer restore()
	svc := &PaymentService{entClient: client, loadBalancer: &captureLoadBalancer{}}
	result, err := svc.ExecuteRefund(ctx, &RefundPlan{OrderID: order.ID, Order: order, RefundAmount: 100, GatewayAmount: 100})
	require.NoError(t, err)
	require.True(t, result.Success)
	require.True(t, providerCalled)
	current, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefunded, current.Status)
	require.Equal(t, payment.ProviderStatusSuccess, current.RefundState.ProviderStatus)
}

func TestRefundRecoveryPreservesPendingDeductionPlanWithoutAudit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		kind   string
		amount float64
	}{
		{name: "no deduction", kind: payment.DeductionTypeNone},
		{name: "partial available balance", kind: payment.DeductionTypeBalance, amount: 25},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			client := newPaymentConfigServiceTestClient(t)
			order := createPendingRefundOrderForTest(t, ctx, client, "pending-plan")
			order, err := client.PaymentOrder.UpdateOne(order).SetStatus(OrderStatusRefunding).Save(ctx)
			require.NoError(t, err)
			var deducted float64
			svc := &PaymentService{
				entClient: client, loadBalancer: &captureLoadBalancer{},
				userRepo: &mockUserRepo{deductAvailableBalanceFn: func(ctx context.Context, _ int64, amount float64) (float64, error) {
					require.NotNil(t, dbent.TxFromContext(ctx))
					deducted += amount
					return amount, nil
				}},
			}
			plan := &RefundPlan{OrderID: order.ID, Order: order, RefundAmount: 100, GatewayAmount: 100,
				Reason: "pending plan", DeductionType: tc.kind, BalanceToDeduct: tc.amount}
			result, err := svc.finishRefund(ctx, plan, &payment.RefundResponse{Status: payment.ProviderStatusPending, RefundID: "rf_plan"})
			require.NoError(t, err)
			require.False(t, result.Success)
			require.Zero(t, deducted)
			_, err = client.PaymentAuditLog.Delete().Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10))).Exec(ctx)
			require.NoError(t, err)
			restore := replacePaymentProviderFactoryForTest(t, &refundQueryProviderTestDouble{
				refundResponse: &payment.RefundResponse{Status: payment.ProviderStatusSuccess, RefundID: "rf_plan"},
			})
			defer restore()
			result, err = svc.QueryAndFinalizeRefund(ctx, order.ID)
			require.NoError(t, err)
			require.True(t, result.Success)
			require.Equal(t, tc.amount, deducted)
			require.Equal(t, tc.amount, result.BalanceDeducted)
		})
	}
}

func TestRefundRecoveryRejectsUnknownLegacyEvidence(t *testing.T) {
	for _, detail := range []string{`{`, `{"refundID":"rf"}`, `{"deductionRollbackOK":null}`, `{"deductionRollbackOK":"true"}`} {
		t.Run(detail, func(t *testing.T) {
			ctx := context.Background()
			client := newPaymentConfigServiceTestClient(t)
			order := createPendingRefundOrderForTest(t, ctx, client, "unknown-legacy")
			_, err := client.PaymentAuditLog.Update().Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10))).SetDetail(detail).Save(ctx)
			require.NoError(t, err)
			svc := &PaymentService{entClient: client}
			result, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
			require.Nil(t, result)
			require.Equal(t, "REFUND_STATE_UNKNOWN", infraerrors.Reason(err))
			reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
			require.NoError(t, err)
			require.Equal(t, OrderStatusRefundPending, reloaded.Status)
		})
	}
}

func TestRefundRecoveryInvalidVersionDoesNotFallBackToAudit(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "unknown-version")
	_, err := client.PaymentOrder.UpdateOne(order).SetRefundState(&domain.PaymentRefundState{
		Version: 99, ProviderStatus: payment.ProviderStatusSuccess, DeductionType: payment.DeductionTypeNone,
	}).Save(ctx)
	require.NoError(t, err)
	svc := &PaymentService{entClient: client}
	result, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.Nil(t, result)
	require.Equal(t, "REFUND_STATE_UNKNOWN", infraerrors.Reason(err))
}

func TestRefundRecoverySurvivesAuditWriteFailure(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "audit-write-failure")
	order, err := client.PaymentOrder.UpdateOne(order).SetStatus(OrderStatusRefunding).Save(ctx)
	require.NoError(t, err)
	failAudit := true
	client.PaymentAuditLog.Use(func(next dbent.Mutator) dbent.Mutator {
		return dbent.MutateFunc(func(ctx context.Context, m dbent.Mutation) (dbent.Value, error) {
			if failAudit {
				return nil, errors.New("audit unavailable")
			}
			return next.Mutate(ctx, m)
		})
	})
	svc := &PaymentService{entClient: client}
	result, err := svc.finishRefund(ctx, &RefundPlan{OrderID: order.ID, Order: order,
		RefundAmount: 100, Reason: "audit failure", DeductionType: payment.DeductionTypeNone,
	}, &payment.RefundResponse{Status: payment.ProviderStatusSuccess, RefundID: "rf_audit"})
	require.NoError(t, err)
	require.False(t, result.Success)
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefundPending, reloaded.Status)
	require.NotNil(t, reloaded.RefundState)
	require.Equal(t, payment.ProviderStatusSuccess, reloaded.RefundState.ProviderStatus)
	failAudit = false
	result, err = svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.NoError(t, err)
	require.True(t, result.Success, "confirmed recovery does not need a provider or audit history")
}

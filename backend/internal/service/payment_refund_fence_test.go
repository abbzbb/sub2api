//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

type fenceReconciledRefundProvider struct {
	refundProviderTestDouble
	onRefund func()
}

func (p *fenceReconciledRefundProvider) Refund(_ context.Context, _ payment.RefundRequest) (*payment.RefundResponse, error) {
	if p.onRefund != nil {
		p.onRefund()
	}
	return nil, errors.New("late transport timeout after reconciliation")
}

func (p *fenceReconciledRefundProvider) QueryRefund(_ context.Context, _ payment.RefundQueryRequest) (*payment.RefundResponse, error) {
	return &payment.RefundResponse{RefundID: "confirmed-original-refund", Status: payment.ProviderStatusSuccess}, nil
}

func TestRefundLateFailureCannotReopenReconciledRefund(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "fence-refund-late-failure")
	order, err := client.PaymentOrder.UpdateOne(order).SetStatus(OrderStatusCompleted).Save(ctx)
	require.NoError(t, err)
	svc := &PaymentService{entClient: client, loadBalancer: &captureLoadBalancer{}}
	prov := &fenceReconciledRefundProvider{}
	prov.onRefund = func() {
		result, queryErr := svc.QueryAndFinalizeRefund(ctx, order.ID)
		require.NoError(t, queryErr)
		require.True(t, result.Success)
		confirmed, getErr := client.PaymentOrder.Get(ctx, order.ID)
		require.NoError(t, getErr)
		require.Equal(t, OrderStatusPartiallyRefunded, confirmed.Status)
	}
	restore := replacePaymentProviderFactoryForTest(t, prov)
	defer restore()
	_, err = svc.ExecuteRefund(ctx, &RefundPlan{OrderID: order.ID, Order: order, RefundAmount: 50, GatewayAmount: 50, Reason: "first", DeductionType: payment.DeductionTypeNone})
	require.NoError(t, err)
	finalOrder, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusPartiallyRefunded, finalOrder.Status, "late gateway failure must not reopen a provider-confirmed finalized refund")
}

func TestRefundQueryLedgerFailureRemainsRecoverable(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "fence-query-ledger-failure")
	state := &domain.PaymentRefundState{
		Version: 1, AttemptID: "query-ledger-attempt", OutRequestNo: "query-ledger-request",
		ProviderStatus: payment.ProviderStatusPending, RefundAmount: 50, GatewayAmount: 50,
		Currency: "CNY", PriorStatus: OrderStatusCompleted,
		DeductionType: payment.DeductionTypeBalance, BalanceToDeduct: 50,
	}
	order, err := client.PaymentOrder.UpdateOne(order).SetStatus(OrderStatusCompleted).SetRefundAmount(50).SetRefundState(state).Save(ctx)
	require.NoError(t, err)
	prov := &fenceReconciledRefundProvider{}
	restore := replacePaymentProviderFactoryForTest(t, prov)
	defer restore()
	svc := &PaymentService{entClient: client, loadBalancer: &captureLoadBalancer{}, userRepo: &mockUserRepo{deductAvailableBalanceFn: func(context.Context, int64, float64) (float64, error) {
		return 0, errors.New("ledger temporarily unavailable")
	}}}
	_, err = svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.Error(t, err)
	current, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, payment.ProviderStatusSuccess, current.RefundState.ProviderStatus)
	require.Equal(t, OrderStatusRefundPending, current.Status, "confirmed refund with unfinished ledger must remain recoverable and not executable as a new refund")
	svc.userRepo = &mockUserRepo{deductAvailableBalanceFn: func(context.Context, int64, float64) (float64, error) { return 50, nil }}
	result, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.NoError(t, err)
	require.True(t, result.Success)
}

func TestRefundStaleRestoreDoesNotMutateNewerRefundingAttempt(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "fence-stale-restore")
	oldState := &domain.PaymentRefundState{
		Version: 1, AttemptID: "old-attempt", OutRequestNo: "old-request",
		ProviderStatus: payment.ProviderStatusPending, RefundAmount: 50, GatewayAmount: 50,
		DeductionType: payment.DeductionTypeNone, PriorStatus: OrderStatusCompleted,
	}
	newState := &domain.PaymentRefundState{
		Version: 1, AttemptID: "new-attempt", OutRequestNo: "new-request",
		ProviderStatus: payment.ProviderStatusPending, RefundAmount: 40, GatewayAmount: 40,
		DeductionType: payment.DeductionTypeNone, PriorStatus: OrderStatusRefundFailed,
	}
	order, err := client.PaymentOrder.UpdateOne(order).
		SetStatus(OrderStatusRefunding).SetRefundState(newState).SetRefundAmount(40).Save(ctx)
	require.NoError(t, err)

	svc := &PaymentService{entClient: client}
	svc.restoreStatus(ctx, &RefundPlan{
		OrderID:     order.ID,
		Order:       dbentOrderWithRefundState(order, oldState),
		PriorStatus: OrderStatusCompleted,
	})

	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefunding, reloaded.Status)
	require.Equal(t, "new-attempt", reloaded.RefundState.AttemptID)
}

func TestRefundStaleTerminalFailureDoesNotMutateNewerRefundingAttempt(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "fence-stale-terminal")
	oldState := &domain.PaymentRefundState{
		Version: 1, AttemptID: "old-fail-attempt", OutRequestNo: "old-fail-request",
		ProviderStatus: payment.ProviderStatusPending, RefundAmount: 50, GatewayAmount: 50,
		DeductionType: payment.DeductionTypeNone,
	}
	newState := &domain.PaymentRefundState{
		Version: 1, AttemptID: "new-fail-attempt", OutRequestNo: "new-fail-request",
		ProviderStatus: payment.ProviderStatusPending, RefundAmount: 25, GatewayAmount: 25,
		DeductionType: payment.DeductionTypeNone,
	}
	order, err := client.PaymentOrder.UpdateOne(order).
		SetStatus(OrderStatusRefunding).SetRefundState(newState).Save(ctx)
	require.NoError(t, err)

	svc := &PaymentService{entClient: client}
	err = svc.persistRefundTerminalFailure(ctx, &RefundPlan{
		OrderID: order.ID,
		Order:   dbentOrderWithRefundState(order, oldState),
	}, errRefundProviderFailed)
	require.NoError(t, err)

	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefunding, reloaded.Status)
	require.Equal(t, "new-fail-attempt", reloaded.RefundState.AttemptID)
	require.Equal(t, payment.ProviderStatusPending, reloaded.RefundState.ProviderStatus)
}

func TestRefundLatePersistRecoveryDoesNotOverwriteNewerAttempt(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "fence-stale-recovery")
	oldState := &domain.PaymentRefundState{
		Version: 1, AttemptID: "old-recovery-attempt", OutRequestNo: "old-recovery-request",
		ProviderStatus: payment.ProviderStatusPending, RefundAmount: 50, GatewayAmount: 50,
		DeductionType: payment.DeductionTypeNone,
	}
	newState := &domain.PaymentRefundState{
		Version: 1, AttemptID: "new-recovery-attempt", OutRequestNo: "new-recovery-request",
		ProviderStatus: payment.ProviderStatusPending, RefundAmount: 30, GatewayAmount: 30,
		DeductionType: payment.DeductionTypeNone,
	}
	order, err := client.PaymentOrder.UpdateOne(order).
		SetStatus(OrderStatusRefunding).SetRefundState(newState).Save(ctx)
	require.NoError(t, err)

	svc := &PaymentService{entClient: client}
	oldPlan := &RefundPlan{
		OrderID: order.ID, Order: dbentOrderWithRefundState(order, oldState),
		RefundAmount: 50, GatewayAmount: 50, IdempotencyKey: "old-recovery-request",
		DeductionType: payment.DeductionTypeNone,
	}
	_, err = svc.persistRefundRecovery(ctx, oldPlan, &payment.RefundResponse{
		RefundID: "late-old-success", Status: payment.ProviderStatusSuccess,
	})
	require.Error(t, err)

	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefunding, reloaded.Status)
	require.Equal(t, "new-recovery-attempt", reloaded.RefundState.AttemptID)
	require.Equal(t, payment.ProviderStatusPending, reloaded.RefundState.ProviderStatus)
}

func dbentOrderWithRefundState(order *dbent.PaymentOrder, state *domain.PaymentRefundState) *dbent.PaymentOrder {
	cp := *order
	cp.RefundState = state
	return &cp
}

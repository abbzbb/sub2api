//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

type reviewAmountRefundProvider struct {
	refundProviderTestDouble
	requests []payment.RefundRequest
}

func (p *reviewAmountRefundProvider) Refund(_ context.Context, req payment.RefundRequest) (*payment.RefundResponse, error) {
	p.requests = append(p.requests, req)
	if len(p.requests) == 1 {
		return nil, errors.New("accepted refund, response lost")
	}
	return &payment.RefundResponse{RefundID: "original-refund", Status: payment.ProviderStatusSuccess}, nil
}

func TestCodexReviewRefundRetryKeepsOriginalAmount(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "codex-refund-amount")
	order, err := client.PaymentOrder.UpdateOne(order).SetStatus(OrderStatusCompleted).Save(ctx)
	require.NoError(t, err)
	prov := &reviewAmountRefundProvider{}
	restore := replacePaymentProviderFactoryForTest(t, prov)
	defer restore()
	svc := &PaymentService{entClient: client, loadBalancer: &captureLoadBalancer{}}
	first := &RefundPlan{OrderID: order.ID, Order: order, RefundAmount: 50, GatewayAmount: 50, Reason: "first", DeductionType: payment.DeductionTypeNone}
	_, err = svc.ExecuteRefund(ctx, first)
	require.NoError(t, err)
	order, err = client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	retry := &RefundPlan{OrderID: order.ID, Order: order, RefundAmount: 10, GatewayAmount: 10, Reason: "retry changed amount", DeductionType: payment.DeductionTypeNone}
	result, err := svc.ExecuteRefund(ctx, retry)
	if err != nil {
		require.Len(t, prov.requests, 1, "changed amount must be rejected before another provider call")
		return
	}
	if len(prov.requests) == 1 {
		require.NotNil(t, result)
		require.False(t, result.Success, "a changed retry must not claim success without reconciliation")
		return
	}
	require.Len(t, prov.requests, 2)
	require.Equal(t, prov.requests[0].IdempotencyKey, prov.requests[1].IdempotencyKey)
	require.Equal(t, prov.requests[0].Amount, prov.requests[1].Amount, "same persisted operation must not change its gateway amount")
	order, err = client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, float64(50), order.RefundAmount, "must account for original operation amount")
}

func TestCodexReviewRefundFailurePreservesRequestedStatus(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "codex-refund-status")
	order, err := client.PaymentOrder.UpdateOne(order).SetStatus(OrderStatusRefundRequested).Save(ctx)
	require.NoError(t, err)
	prov := &reviewAmountRefundProvider{}
	restore := replacePaymentProviderFactoryForTest(t, prov)
	defer restore()
	svc := &PaymentService{entClient: client, loadBalancer: &captureLoadBalancer{}}
	plan := &RefundPlan{OrderID: order.ID, Order: order, RefundAmount: 50, GatewayAmount: 50, Reason: "first", DeductionType: payment.DeductionTypeNone}
	_, err = svc.ExecuteRefund(ctx, plan)
	require.NoError(t, err)
	order, err = client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Contains(t, []string{OrderStatusRefundRequested, OrderStatusRefundPending}, order.Status, "failed requested refund must not silently become an ordinary completed order")
}

type reviewReconciledRefundProvider struct {
	refundProviderTestDouble
	onRefund func()
}

func (p *reviewReconciledRefundProvider) Refund(_ context.Context, _ payment.RefundRequest) (*payment.RefundResponse, error) {
	p.onRefund()
	return nil, errors.New("late transport timeout after reconciliation")
}

func (p *reviewReconciledRefundProvider) QueryRefund(_ context.Context, _ payment.RefundQueryRequest) (*payment.RefundResponse, error) {
	return &payment.RefundResponse{RefundID: "confirmed-original-refund", Status: payment.ProviderStatusSuccess}, nil
}

func TestCodexReviewLateFailureCannotReopenReconciledRefund(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "codex-refund-late-failure")
	order, err := client.PaymentOrder.UpdateOne(order).SetStatus(OrderStatusCompleted).Save(ctx)
	require.NoError(t, err)
	svc := &PaymentService{entClient: client, loadBalancer: &captureLoadBalancer{}}
	prov := &reviewReconciledRefundProvider{}
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

func TestCodexReviewQueryLedgerFailureRemainsRecoverable(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "codex-query-ledger-failure")
	state := &domain.PaymentRefundState{Version: 1, AttemptID: "query-ledger-attempt", OutRequestNo: "query-ledger-request", ProviderStatus: payment.ProviderStatusPending, RefundAmount: 50, GatewayAmount: 50, Currency: "CNY", PriorStatus: OrderStatusCompleted, DeductionType: payment.DeductionTypeBalance, BalanceToDeduct: 50}
	order, err := client.PaymentOrder.UpdateOne(order).SetStatus(OrderStatusCompleted).SetRefundAmount(50).SetRefundState(state).Save(ctx)
	require.NoError(t, err)
	prov := &reviewReconciledRefundProvider{}
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

//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type recordingRefundProvider struct {
	refundProviderTestDouble
	keys       []string
	amounts    []string
	fail       error
	failStatus string
}

func (p *recordingRefundProvider) Refund(_ context.Context, req payment.RefundRequest) (*payment.RefundResponse, error) {
	p.keys = append(p.keys, req.IdempotencyKey)
	p.amounts = append(p.amounts, req.Amount)
	if p.fail != nil && len(p.keys) == 1 {
		return nil, p.fail
	}
	if p.failStatus != "" && len(p.keys) == 1 {
		return &payment.RefundResponse{RefundID: "rf_fail", Status: p.failStatus}, nil
	}
	return &payment.RefundResponse{
		RefundID: "rf_" + req.IdempotencyKey,
		Status:   payment.ProviderStatusSuccess,
	}, nil
}

type recordingQueryRefundProvider struct {
	refundProviderTestDouble
	last payment.RefundQueryRequest
	resp *payment.RefundResponse
}

func (p *recordingQueryRefundProvider) QueryRefund(_ context.Context, req payment.RefundQueryRequest) (*payment.RefundResponse, error) {
	p.last = req
	return p.resp, nil
}

func TestExecuteRefundReusesIdempotencyKeyAfterResponseLoss(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "refund-idemp-timeout")
	order, err := client.PaymentOrder.UpdateOne(order).SetStatus(OrderStatusCompleted).Save(ctx)
	require.NoError(t, err)

	prov := &recordingRefundProvider{fail: errors.New("net/http: TLS handshake timeout")}
	restore := replacePaymentProviderFactoryForTest(t, prov)
	defer restore()

	svc := &PaymentService{entClient: client, loadBalancer: &captureLoadBalancer{}}
	plan := &RefundPlan{
		OrderID: order.ID, Order: order, RefundAmount: 50, GatewayAmount: 50,
		Reason: "partial after timeout", DeductionType: payment.DeductionTypeNone,
	}
	result, err := svc.ExecuteRefund(ctx, plan)
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Len(t, prov.keys, 1)
	require.NotEmpty(t, prov.keys[0])

	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Contains(t, []string{OrderStatusCompleted, OrderStatusRefundPending}, reloaded.Status)
	require.NotNil(t, reloaded.RefundState)
	require.Equal(t, prov.keys[0], reloaded.RefundState.OutRequestNo)
	require.Equal(t, payment.ProviderStatusPending, reloaded.RefundState.ProviderStatus)
	require.Equal(t, 50.0, reloaded.RefundState.RefundAmount)

	retryPlan := &RefundPlan{
		OrderID: reloaded.ID, Order: reloaded, RefundAmount: 50, GatewayAmount: 50,
		Reason: "partial after timeout", DeductionType: payment.DeductionTypeNone,
	}
	result, err = svc.ExecuteRefund(ctx, retryPlan)
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Len(t, prov.keys, 2)
	require.Equal(t, prov.keys[0], prov.keys[1], "response-loss retry must reuse the persisted OutRequestNo")

	reloaded, err = client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusPartiallyRefunded, reloaded.Status)
	require.Equal(t, payment.ProviderStatusSuccess, reloaded.RefundState.ProviderStatus)
	firstKey := prov.keys[0]

	reloaded, err = client.PaymentOrder.UpdateOne(reloaded).SetStatus(OrderStatusCompleted).Save(ctx)
	require.NoError(t, err)
	distinctPlan := &RefundPlan{
		OrderID: reloaded.ID, Order: reloaded, RefundAmount: 50, GatewayAmount: 50,
		Reason: "later distinct refund", DeductionType: payment.DeductionTypeNone,
	}
	result, err = svc.ExecuteRefund(ctx, distinctPlan)
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Len(t, prov.keys, 3)
	require.NotEmpty(t, prov.keys[2])
	require.NotEqual(t, firstKey, prov.keys[2], "a later distinct refund must mint a new request id")
}

func TestQueryAndFinalizeRefundPassesOutRequestNo(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "refund-query-out-request")
	state := refundRecoveryFromPlan(&RefundPlan{
		DeductionType:  payment.DeductionTypeNone,
		IdempotencyKey: "sub2_refund-query-out-request-refund-1",
	}, &payment.RefundResponse{Status: payment.ProviderStatusPending, RefundID: "rf_test"})
	order, err := client.PaymentOrder.UpdateOne(order).SetRefundState(state).Save(ctx)
	require.NoError(t, err)

	prov := &recordingQueryRefundProvider{
		resp: &payment.RefundResponse{RefundID: "rf_test", Status: payment.ProviderStatusPending},
	}
	restore := replacePaymentProviderFactoryForTest(t, prov)
	defer restore()

	svc := &PaymentService{entClient: client, loadBalancer: &captureLoadBalancer{}}
	result, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Equal(t, state.OutRequestNo, prov.last.OutRequestNo)
	require.Equal(t, "rf_test", prov.last.RefundID)
}

func TestRefundRequestIDToReuse(t *testing.T) {
	require.Empty(t, refundRequestIDToReuse(nil))
	require.Equal(t, "req-1", refundRequestIDToReuse(&domain.PaymentRefundState{
		OutRequestNo:   "req-1",
		ProviderStatus: payment.ProviderStatusPending,
	}))
	require.Empty(t, refundRequestIDToReuse(&domain.PaymentRefundState{
		OutRequestNo:   "req-1",
		ProviderStatus: payment.ProviderStatusSuccess,
	}))
	require.Empty(t, refundRequestIDToReuse(&domain.PaymentRefundState{
		OutRequestNo:   "req-1",
		ProviderStatus: payment.ProviderStatusFailed,
	}))
	require.Equal(t, "req-2", refundRequestIDToReuse(&domain.PaymentRefundState{
		OutRequestNo: "req-2",
	}))
}

func TestExecuteRefundRejectsChangedAmountBeforeProviderRetry(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "refund-idemp-amount-mismatch")
	order, err := client.PaymentOrder.UpdateOne(order).SetStatus(OrderStatusCompleted).Save(ctx)
	require.NoError(t, err)

	prov := &recordingRefundProvider{fail: errors.New("accepted refund, response lost")}
	restore := replacePaymentProviderFactoryForTest(t, prov)
	defer restore()
	svc := &PaymentService{entClient: client, loadBalancer: &captureLoadBalancer{}}

	_, err = svc.ExecuteRefund(ctx, &RefundPlan{
		OrderID: order.ID, Order: order, RefundAmount: 50, GatewayAmount: 50,
		Reason: "first", DeductionType: payment.DeductionTypeNone,
	})
	require.NoError(t, err)
	require.Len(t, prov.keys, 1)

	order, err = client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	_, err = svc.ExecuteRefund(ctx, &RefundPlan{
		OrderID: order.ID, Order: order, RefundAmount: 10, GatewayAmount: 10,
		Reason: "changed", DeductionType: payment.DeductionTypeNone,
	})
	require.Error(t, err)
	require.Equal(t, "REFUND_IN_FLIGHT", infraerrors.Reason(err))
	require.Len(t, prov.keys, 1)
	require.Len(t, prov.amounts, 1)
}

func TestExecuteRefundTerminalFailedMintsNewKey(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "refund-idemp-failed")
	order, err := client.PaymentOrder.UpdateOne(order).SetStatus(OrderStatusCompleted).Save(ctx)
	require.NoError(t, err)

	prov := &recordingRefundProvider{failStatus: payment.ProviderStatusFailed}
	restore := replacePaymentProviderFactoryForTest(t, prov)
	defer restore()
	svc := &PaymentService{entClient: client, loadBalancer: &captureLoadBalancer{}}

	result, err := svc.ExecuteRefund(ctx, &RefundPlan{
		OrderID: order.ID, Order: order, RefundAmount: 50, GatewayAmount: 50,
		Reason: "first", DeductionType: payment.DeductionTypeNone,
	})
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Len(t, prov.keys, 1)
	firstKey := prov.keys[0]

	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefundFailed, reloaded.Status)
	require.NotNil(t, reloaded.RefundState)
	require.Equal(t, payment.ProviderStatusFailed, reloaded.RefundState.ProviderStatus)
	require.Equal(t, firstKey, reloaded.RefundState.OutRequestNo)

	result, err = svc.ExecuteRefund(ctx, &RefundPlan{
		OrderID: reloaded.ID, Order: reloaded, RefundAmount: 50, GatewayAmount: 50,
		Reason: "new attempt", DeductionType: payment.DeductionTypeNone,
	})
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Len(t, prov.keys, 2)
	require.NotEqual(t, firstKey, prov.keys[1], "a terminal-failed attempt must not reuse its OutRequestNo")
}

func TestQueryAndFinalizeRefundReconcilesNonPendingStatuses(t *testing.T) {
	for _, status := range []string{OrderStatusCompleted, OrderStatusRefundRequested, OrderStatusRefunding} {
		t.Run(status, func(t *testing.T) {
			ctx := context.Background()
			client := newPaymentConfigServiceTestClient(t)
			order := createPendingRefundOrderForTest(t, ctx, client, "refund-reconcile-"+status)
			state := &domain.PaymentRefundState{
				Version: 1, AttemptID: "attempt-" + status, ProviderStatus: payment.ProviderStatusPending,
				OutRequestNo: "out-" + status, RefundID: "rf_test",
				RefundAmount: 50, GatewayAmount: 50, DeductionType: payment.DeductionTypeNone,
			}
			order, err := client.PaymentOrder.UpdateOne(order).
				SetStatus(status).SetRefundState(state).SetRefundAmount(50).Save(ctx)
			require.NoError(t, err)

			prov := &recordingQueryRefundProvider{
				resp: &payment.RefundResponse{RefundID: "rf_test", Status: payment.ProviderStatusSuccess},
			}
			restore := replacePaymentProviderFactoryForTest(t, prov)
			defer restore()
			svc := &PaymentService{entClient: client, loadBalancer: &captureLoadBalancer{}}
			result, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
			require.NoError(t, err)
			require.True(t, result.Success)
			require.Equal(t, state.OutRequestNo, prov.last.OutRequestNo)
			reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
			require.NoError(t, err)
			require.Equal(t, OrderStatusPartiallyRefunded, reloaded.Status)
		})
	}
}

func TestQueryAndFinalizeRefundBlankStatusStaysPending(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "refund-query-blank")
	state := &domain.PaymentRefundState{
		Version: 1, AttemptID: "attempt-blank", ProviderStatus: payment.ProviderStatusPending,
		OutRequestNo: "out-blank", RefundID: "rf_blank",
		RefundAmount: 50, GatewayAmount: 50, DeductionType: payment.DeductionTypeNone,
	}
	order, err := client.PaymentOrder.UpdateOne(order).
		SetStatus(OrderStatusCompleted).SetRefundState(state).Save(ctx)
	require.NoError(t, err)

	prov := &recordingQueryRefundProvider{
		resp: &payment.RefundResponse{RefundID: "rf_blank", Status: payment.ProviderStatusPending},
	}
	restore := replacePaymentProviderFactoryForTest(t, prov)
	defer restore()
	svc := &PaymentService{entClient: client, loadBalancer: &captureLoadBalancer{}}
	result, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Contains(t, result.Warning, "pending")

	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, reloaded.Status)
	require.Equal(t, payment.ProviderStatusPending, reloaded.RefundState.ProviderStatus)
	require.Equal(t, "out-blank", reloaded.RefundState.OutRequestNo)
	require.NotEqual(t, OrderStatusRefundFailed, reloaded.Status)
}

func TestRestoreStatusDoesNotOverwriteFinalizedRefund(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "restore-finalized")
	pending := &domain.PaymentRefundState{
		Version: 1, AttemptID: "attempt-old", ProviderStatus: payment.ProviderStatusPending,
		OutRequestNo: "out-old", RefundAmount: 50, GatewayAmount: 50, DeductionType: payment.DeductionTypeNone,
	}
	final := *pending
	final.ProviderStatus = payment.ProviderStatusSuccess
	order, err := client.PaymentOrder.UpdateOne(order).
		SetStatus(OrderStatusPartiallyRefunded).SetRefundState(&final).SetRefundAmount(50).Save(ctx)
	require.NoError(t, err)

	svc := &PaymentService{entClient: client}
	plan := &RefundPlan{
		OrderID: order.ID, Order: &dbent.PaymentOrder{ID: order.ID, Status: OrderStatusRefunding, RefundState: pending},
		PriorStatus: OrderStatusCompleted, RefundAmount: 50, GatewayAmount: 50, DeductionType: payment.DeductionTypeNone,
	}
	svc.restoreStatus(ctx, plan)
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusPartiallyRefunded, reloaded.Status)
}

func TestRestoreStatusDoesNotMutateNewerRefundingAttempt(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "restore-newer-attempt")
	newer := &domain.PaymentRefundState{
		Version: 1, AttemptID: "attempt-new", ProviderStatus: payment.ProviderStatusPending,
		OutRequestNo: "out-new", RefundAmount: 40, GatewayAmount: 40, DeductionType: payment.DeductionTypeNone,
	}
	order, err := client.PaymentOrder.UpdateOne(order).
		SetStatus(OrderStatusRefunding).SetRefundState(newer).Save(ctx)
	require.NoError(t, err)

	stale := &domain.PaymentRefundState{
		Version: 1, AttemptID: "attempt-old", ProviderStatus: payment.ProviderStatusPending,
		OutRequestNo: "out-old", RefundAmount: 50, GatewayAmount: 50, DeductionType: payment.DeductionTypeNone,
	}
	svc := &PaymentService{entClient: client}
	svc.restoreStatus(ctx, &RefundPlan{
		OrderID: order.ID, Order: &dbent.PaymentOrder{ID: order.ID, Status: OrderStatusRefunding, RefundState: stale},
		PriorStatus: OrderStatusCompleted,
	})
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefunding, reloaded.Status)
	require.Equal(t, "attempt-new", reloaded.RefundState.AttemptID)

	err = svc.persistRefundTerminalFailure(ctx, &RefundPlan{
		OrderID: order.ID, Order: &dbent.PaymentOrder{ID: order.ID, Status: OrderStatusRefunding, RefundState: stale},
	}, errors.New("stale failed"))
	require.NoError(t, err)
	reloaded, err = client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefunding, reloaded.Status)
	require.Equal(t, payment.ProviderStatusPending, reloaded.RefundState.ProviderStatus)
	require.Equal(t, "attempt-new", reloaded.RefundState.AttemptID)
}

func TestQueryAndFinalizeLedgerFailureLeavesRefundPending(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "query-ledger-pending")
	state := &domain.PaymentRefundState{
		Version: 1, AttemptID: "attempt-ledger", OutRequestNo: "out-ledger",
		ProviderStatus: payment.ProviderStatusPending, RefundAmount: 50, GatewayAmount: 50,
		Currency: "CNY", PriorStatus: OrderStatusCompleted, DeductionType: payment.DeductionTypeBalance, BalanceToDeduct: 50,
	}
	order, err := client.PaymentOrder.UpdateOne(order).SetStatus(OrderStatusCompleted).SetRefundState(state).SetRefundAmount(50).Save(ctx)
	require.NoError(t, err)

	restore := replacePaymentProviderFactoryForTest(t, &recordingQueryRefundProvider{
		resp: &payment.RefundResponse{RefundID: "rf_ledger", Status: payment.ProviderStatusSuccess},
	})
	defer restore()
	svc := &PaymentService{
		entClient: client, loadBalancer: &captureLoadBalancer{},
		userRepo: &mockUserRepo{deductAvailableBalanceFn: func(context.Context, int64, float64) (float64, error) {
			return 0, errors.New("ledger temporarily unavailable")
		}},
	}
	_, err = svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.Error(t, err)
	current, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefundPending, current.Status)
	require.Equal(t, payment.ProviderStatusSuccess, current.RefundState.ProviderStatus)

	_, err = svc.ExecuteRefund(ctx, &RefundPlan{
		OrderID: current.ID, Order: current, RefundAmount: 50, GatewayAmount: 50,
		Reason: "must not mint a new key", DeductionType: payment.DeductionTypeNone,
	})
	require.Error(t, err)
	require.Equal(t, "CONFLICT", infraerrors.Reason(err))
}

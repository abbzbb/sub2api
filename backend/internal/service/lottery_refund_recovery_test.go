//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

func TestLotteryRefundAccountingFailurePreservesPendingState(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "lottery-recovery")
	order, err := client.PaymentOrder.UpdateOne(order).SetStatus(OrderStatusRefunding).Save(ctx)
	require.NoError(t, err)
	require.NoError(t, client.User.UpdateOneID(order.UserID).SetBalance(order.Amount).Exec(ctx))
	svc := &PaymentService{entClient: client, lottery: &LotteryService{client: client}}
	plan := &RefundPlan{OrderID: order.ID, Order: order, RefundAmount: order.Amount, GatewayAmount: order.Amount, BalanceToDeduct: order.Amount, DeductionType: payment.DeductionTypeBalance, PriorStatus: OrderStatusCompleted}
	require.NoError(t, svc.ensureRefundIdempotencyKey(ctx, plan))
	failAccounting := true
	client.User.Intercept(dbent.InterceptFunc(func(next dbent.Querier) dbent.Querier {
		return dbent.QuerierFunc(func(ctx context.Context, query dbent.Query) (dbent.Value, error) {
			if failAccounting {
				return nil, errors.New("injected lottery accounting failure")
			}
			return next.Query(ctx, query)
		})
	}))
	result, err := svc.finishRefund(ctx, plan, &payment.RefundResponse{Status: payment.ProviderStatusSuccess, RefundID: "local-qa-refund"})
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Contains(t, result.Warning, "retry finalize")
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefundPending, reloaded.Status)
	require.NotNil(t, reloaded.RefundState)
	require.Equal(t, payment.ProviderStatusSuccess, reloaded.RefundState.ProviderStatus)
	require.True(t, reloaded.RefundState.DeductionHeld)
	require.Equal(t, order.Amount, reloaded.RefundState.BalanceHeld)
	require.Equal(t, "local-qa-refund", reloaded.RefundState.RefundID)
	failAccounting = false
	user, err := client.User.Get(ctx, order.UserID)
	require.NoError(t, err)
	require.Zero(t, user.Balance)
	require.Equal(t, order.Amount, user.FrozenBalance)
}

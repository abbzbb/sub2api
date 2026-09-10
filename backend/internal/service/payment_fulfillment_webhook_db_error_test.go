//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

func createWebhookDBErrorTestOrder(t *testing.T, ctx context.Context, client *dbent.Client, status, outTradeNo string) *dbent.PaymentOrder {
	t.Helper()
	user, err := client.User.Create().
		SetEmail(outTradeNo + "@example.com").
		SetPasswordHash("hash").
		SetUsername(outTradeNo).
		Save(ctx)
	require.NoError(t, err)

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(80).
		SetPayAmount(80).
		SetFeeRate(0).
		SetRechargeCode("WH-" + outTradeNo).
		SetOutTradeNo(outTradeNo).
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-" + outTradeNo).
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(status).
		SetPaidAt(time.Now().Add(-time.Hour)).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)
	return order
}

func interceptPaymentOrderQuery(client *dbent.Client, failAt int, injected error) {
	n := 0
	client.PaymentOrder.Intercept(dbent.InterceptFunc(func(next dbent.Querier) dbent.Querier {
		return dbent.QuerierFunc(func(ctx context.Context, query dbent.Query) (dbent.Value, error) {
			n++
			if n == failAt {
				return nil, injected
			}
			return next.Query(ctx, query)
		})
	}))
}

func TestConfirmPaymentByID_GetInfrastructureError(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createWebhookDBErrorTestOrder(t, ctx, client, OrderStatusPending, "otn_confirm_lookup")

	injected := errors.New("injected confirmPayment get failure")
	interceptPaymentOrderQuery(client, 1, injected)

	svc := &PaymentService{entClient: client, providersLoaded: true}
	err := svc.confirmPaymentByID(ctx, order.ID, "trade-confirm-lookup", order.PayAmount, payment.TypeAlipay, nil)
	require.Error(t, err)
	require.ErrorIs(t, err, injected)
	require.False(t, errors.Is(err, ErrOrderNotFound),
		"infrastructure errors must not be ACKed as unknown-order")
	require.Contains(t, err.Error(), injected.Error())
}

func TestHandlePaymentNotification_ConfirmPaymentLookupDBError(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createWebhookDBErrorTestOrder(t, ctx, client, OrderStatusPending, "otn_confirm_lookup_legacy")

	injected := errors.New("injected confirmPayment get failure")
	// Legacy path: out_trade_no lookup (1) is a genuine miss, confirmPaymentByID Get is query 2.
	interceptPaymentOrderQuery(client, 2, injected)

	svc := &PaymentService{entClient: client, providersLoaded: true}
	err := svc.HandlePaymentNotification(ctx, &payment.PaymentNotification{
		OrderID: orderIDPrefix + strconv.FormatInt(order.ID, 10),
		TradeNo: "trade-confirm-lookup",
		Amount:  order.PayAmount,
		Status:  payment.NotificationStatusSuccess,
	}, payment.TypeAlipay)

	require.Error(t, err)
	require.ErrorIs(t, err, injected)
	require.False(t, errors.Is(err, ErrOrderNotFound),
		"infrastructure errors must not be ACKed as unknown-order")
	require.Contains(t, err.Error(), injected.Error())
}

func TestHandlePaymentNotification_AlreadyProcessedReloadDBError(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createWebhookDBErrorTestOrder(t, ctx, client, OrderStatusCompleted, "otn_already_processed")

	injected := errors.New("injected alreadyProcessed reload failure")
	// Primary path skips confirmPayment Get: query 1 is out_trade_no, query 2 is alreadyProcessed reload.
	interceptPaymentOrderQuery(client, 2, injected)

	svc := &PaymentService{entClient: client, providersLoaded: true}
	err := svc.HandlePaymentNotification(ctx, &payment.PaymentNotification{
		OrderID: order.OutTradeNo,
		TradeNo: "trade-already-processed",
		Amount:  order.PayAmount,
		Status:  payment.NotificationStatusSuccess,
	}, payment.TypeAlipay)

	require.Error(t, err)
	require.ErrorIs(t, err, injected)
	require.False(t, errors.Is(err, ErrOrderNotFound),
		"reload infrastructure errors must not masquerade as order-not-found")
}

func TestAlreadyProcessed_ReloadDBError(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createWebhookDBErrorTestOrder(t, ctx, client, OrderStatusPaid, "otn_direct_reload")

	injected := errors.New("injected alreadyProcessed get failure")
	interceptPaymentOrderQuery(client, 1, injected)

	svc := &PaymentService{entClient: client}
	err := svc.alreadyProcessed(ctx, order)
	require.Error(t, err)
	require.ErrorIs(t, err, injected)
	require.False(t, errors.Is(err, ErrOrderNotFound))
}

func TestConfirmPaymentByID_MissingOrder_ReturnsSentinel(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	svc := &PaymentService{entClient: client, providersLoaded: true}

	missingID := int64(42424242)
	err := svc.HandlePaymentNotification(ctx, &payment.PaymentNotification{
		OrderID: orderIDPrefix + strconv.FormatInt(missingID, 10),
		TradeNo: "trade-missing-legacy",
		Amount:  10,
		Status:  payment.NotificationStatusSuccess,
	}, payment.TypeAlipay)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrOrderNotFound)
	require.Contains(t, err.Error(), fmt.Sprintf("%d", missingID))
}

func TestAlreadyProcessed_MissingOrder_ReturnsSentinel(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	svc := &PaymentService{entClient: client}

	err := svc.alreadyProcessed(ctx, &dbent.PaymentOrder{ID: 42424243})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrOrderNotFound)
}

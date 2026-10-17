//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"entgo.io/ent"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type heldRefundProvider struct {
	refundProviderTestDouble
	refundStatus string
	keys         []string
	queryStatus  string
	refundError  error
	onRefund     func()
	onQuery      func()
}

func (p *heldRefundProvider) Refund(_ context.Context, req payment.RefundRequest) (*payment.RefundResponse, error) {
	p.keys = append(p.keys, req.IdempotencyKey)
	if p.onRefund != nil {
		p.onRefund()
	}
	if p.refundError != nil {
		return nil, p.refundError
	}
	status := p.refundStatus
	if status == "" {
		status = payment.ProviderStatusPending
	}
	return &payment.RefundResponse{RefundID: "held-refund", Status: status}, nil
}

func (p *heldRefundProvider) QueryRefund(context.Context, payment.RefundQueryRequest) (*payment.RefundResponse, error) {
	if p.onQuery != nil {
		p.onQuery()
	}
	return &payment.RefundResponse{RefundID: "held-refund", Status: p.queryStatus}, nil
}

func heldRefundFixture(t *testing.T) (*dbent.Client, *PaymentService, *RefundPlan, *heldRefundProvider) {
	t.Helper()
	client := newPaymentConfigServiceTestClient(t)
	return heldRefundFixtureWithClient(t, client)
}

func heldRefundFixtureWithClient(t *testing.T, client *dbent.Client) (*dbent.Client, *PaymentService, *RefundPlan, *heldRefundProvider) {
	t.Helper()
	ctx := context.Background()
	o := createPendingRefundOrderForTest(t, ctx, client, "held-refund")
	o, err := client.PaymentOrder.UpdateOne(o).SetStatus(OrderStatusCompleted).Save(ctx)
	require.NoError(t, err)
	require.NoError(t, client.User.UpdateOneID(o.UserID).SetBalance(100).SetFrozenBalance(7).Exec(ctx))
	p := &heldRefundProvider{queryStatus: payment.ProviderStatusPending}
	t.Cleanup(replacePaymentProviderFactoryForTest(t, p))
	return client, &PaymentService{entClient: client, loadBalancer: &captureLoadBalancer{}},
		&RefundPlan{OrderID: o.ID, Order: o, RefundAmount: 100, GatewayAmount: 100, DeductionType: payment.DeductionTypeBalance, BalanceToDeduct: 100}, p
}

func TestRefundHoldTimeoutFencesLegacyExecutor(t *testing.T) {
	for _, balance := range []float64{100, 200} {
		t.Run(fmt.Sprint(balance), func(t *testing.T) {
			ctx := context.Background()
			client, svc, plan, prov := heldRefundFixture(t)
			require.NoError(t, client.User.UpdateOneID(plan.Order.UserID).SetBalance(balance).Exec(ctx))
			prov.refundError = errors.New("transport timeout")
			_, err := svc.ExecuteRefund(ctx, plan)
			require.NoError(t, err)
			o, err := client.PaymentOrder.Get(ctx, plan.OrderID)
			require.NoError(t, err)
			require.Equal(t, OrderStatusRefundPending, o.Status)
			// Exact status claim used by the pre-reservation ExecuteRefund.
			n, err := client.PaymentOrder.Update().Where(paymentorder.IDEQ(o.ID),
				paymentorder.StatusIn(OrderStatusCompleted, OrderStatusRefundRequested, OrderStatusRefundFailed)).
				SetStatus(OrderStatusRefunding).Save(ctx)
			require.NoError(t, err)
			require.Zero(t, n)
			require.True(t, o.RefundState.DeductionHeld)
			retry, warning, err := svc.PrepareRefund(ctx, o.ID, 100, "retry", false, true)
			require.NoError(t, err)
			require.Nil(t, warning)
			prov.refundError = nil
			_, err = svc.ExecuteRefund(ctx, retry)
			require.NoError(t, err)
			require.Len(t, prov.keys, 2)
			require.Equal(t, prov.keys[0], prov.keys[1])
			u, err := client.User.Get(ctx, o.UserID)
			require.NoError(t, err)
			require.Equal(t, balance-100, u.Balance)
			require.Equal(t, 107.0, u.FrozenBalance)
			prov.queryStatus = payment.ProviderStatusFailed
			_, err = svc.QueryAndFinalizeRefund(ctx, o.ID)
			require.NoError(t, err)
			u, err = client.User.Get(ctx, o.UserID)
			require.NoError(t, err)
			require.Equal(t, balance, u.Balance)
			require.Equal(t, 7.0, u.FrozenBalance)
		})
	}
}

func TestRefundHoldStaleRetryCannotReopenProviderConfirmation(t *testing.T) {
	ctx := context.Background()
	client, svc, plan, prov := heldRefundFixture(t)
	_, err := svc.ExecuteRefund(ctx, plan)
	require.NoError(t, err)
	o, err := client.PaymentOrder.Get(ctx, plan.OrderID)
	require.NoError(t, err)
	retry, warning, err := svc.PrepareRefund(ctx, o.ID, 100, "retry", false, true)
	require.NoError(t, err)
	require.Nil(t, warning)
	confirmed := *o.RefundState
	confirmed.ProviderStatus = payment.ProviderStatusSuccess
	require.NoError(t, svc.persistRefundConfirmation(ctx, o, &confirmed))
	_, err = svc.ExecuteRefund(ctx, retry)
	require.Equal(t, "CONFLICT", infraerrors.Reason(err))
	require.Len(t, prov.keys, 1)
	o, err = client.PaymentOrder.Get(ctx, o.ID)
	require.NoError(t, err)
	require.Equal(t, payment.ProviderStatusSuccess, o.RefundState.ProviderStatus)
	require.True(t, o.RefundState.DeductionHeld)
}

func TestRefundHoldBlocksSpendingUntilSettled(t *testing.T) {
	for _, status := range []string{payment.ProviderStatusSuccess, payment.ProviderStatusFailed} {
		t.Run(status, func(t *testing.T) {
			ctx := context.Background()
			client, svc, plan, prov := heldRefundFixture(t)
			prov.onRefund = func() {
				u, err := client.User.Get(ctx, plan.Order.UserID)
				require.NoError(t, err)
				require.Zero(t, u.Balance)
				require.Equal(t, 107.0, u.FrozenBalance)
				n, err := client.User.Update().Where(user.IDEQ(u.ID), user.BalanceGTE(80)).AddBalance(-80).Save(ctx)
				require.NoError(t, err)
				require.Zero(t, n, "pending refund funds must be unavailable even during the provider call")
			}
			result, err := svc.ExecuteRefund(ctx, plan)
			require.NoError(t, err)
			require.False(t, result.Success)
			stored, err := client.PaymentOrder.Get(ctx, plan.OrderID)
			require.NoError(t, err)
			require.Equal(t, 2, stored.RefundState.Version, "older binaries must reject reserved operations")
			_, err = svc.QueryAndFinalizeRefund(ctx, plan.OrderID)
			require.NoError(t, err)
			prov.queryStatus = status
			result, err = svc.QueryAndFinalizeRefund(ctx, plan.OrderID)
			require.NoError(t, err)
			require.Equal(t, status == payment.ProviderStatusSuccess, result.Success)
			u, err := client.User.Get(ctx, plan.Order.UserID)
			require.NoError(t, err)
			require.Equal(t, 7.0, u.FrozenBalance, "unrelated frozen balances must be preserved")
			if status == payment.ProviderStatusSuccess {
				require.Zero(t, u.Balance)
				require.Equal(t, 100.0, result.BalanceDeducted)
			} else {
				require.Equal(t, 100.0, u.Balance)
			}
			_, err = svc.QueryAndFinalizeRefund(ctx, plan.OrderID)
			require.Error(t, err)
			after, err := client.User.Get(ctx, u.ID)
			require.NoError(t, err)
			require.Equal(t, u.Balance, after.Balance)
			require.Equal(t, u.FrozenBalance, after.FrozenBalance)
			require.Len(t, prov.keys, 1)
		})
	}
}

func TestRefundHoldResponseLossReusesReservation(t *testing.T) {
	ctx := context.Background()
	client, svc, plan, prov := heldRefundFixture(t)
	prov.refundError = errors.New("transport response lost")
	_, err := svc.ExecuteRefund(ctx, plan)
	require.NoError(t, err)
	o, err := client.PaymentOrder.Get(ctx, plan.OrderID)
	require.NoError(t, err)
	prov.refundError = nil
	retry, warning, err := svc.PrepareRefund(ctx, o.ID, 100, "retry after response loss", false, true)
	require.NoError(t, err)
	require.Nil(t, warning)
	require.Equal(t, 100.0, retry.BalanceToDeduct)
	_, err = svc.ExecuteRefund(ctx, retry)
	require.NoError(t, err)
	require.Len(t, prov.keys, 2)
	require.Equal(t, prov.keys[0], prov.keys[1])
	u, err := client.User.Get(ctx, o.UserID)
	require.NoError(t, err)
	require.Zero(t, u.Balance)
	require.Equal(t, 107.0, u.FrozenBalance)
	prov.queryStatus = payment.ProviderStatusFailed
	_, err = svc.QueryAndFinalizeRefund(ctx, o.ID)
	require.NoError(t, err)
	u, err = client.User.Get(ctx, o.UserID)
	require.NoError(t, err)
	require.Equal(t, 100.0, u.Balance)
	require.Equal(t, 7.0, u.FrozenBalance)
}

func TestRefundHoldFailureSettlementRollsBackAtomically(t *testing.T) {
	ctx := context.Background()
	client, svc, plan, prov := heldRefundFixture(t)
	_, err := svc.ExecuteRefund(ctx, plan)
	require.NoError(t, err)
	prov.queryStatus = payment.ProviderStatusFailed
	fail := true
	client.PaymentAuditLog.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			if fail {
				return nil, errors.New("injected release failure")
			}
			return next.Mutate(ctx, m)
		})
	})
	_, err = svc.QueryAndFinalizeRefund(ctx, plan.OrderID)
	require.ErrorContains(t, err, "injected release failure")
	o, err := client.PaymentOrder.Get(ctx, plan.OrderID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefundPending, o.Status)
	u, err := client.User.Get(ctx, o.UserID)
	require.NoError(t, err)
	require.Zero(t, u.Balance)
	require.Equal(t, 107.0, u.FrozenBalance)
	fail = false
	_, err = svc.QueryAndFinalizeRefund(ctx, o.ID)
	require.NoError(t, err)
	u, err = client.User.Get(ctx, o.UserID)
	require.NoError(t, err)
	require.Equal(t, 100.0, u.Balance)
	require.Equal(t, 7.0, u.FrozenBalance)
}

func TestRefundHoldReservationFailureDoesNotCallProvider(t *testing.T) {
	ctx := context.Background()
	client, svc, plan, prov := heldRefundFixture(t)
	client.PaymentOrder.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			if mutation, ok := m.(*dbent.PaymentOrderMutation); ok {
				if state, set := mutation.RefundState(); set && state.DeductionHeld {
					return nil, errors.New("injected reservation write failure")
				}
			}
			return next.Mutate(ctx, m)
		})
	})
	_, err := svc.ExecuteRefund(ctx, plan)
	require.NoError(t, err)
	require.Empty(t, prov.keys)
	u, err := client.User.Get(ctx, plan.Order.UserID)
	require.NoError(t, err)
	require.Equal(t, 100.0, u.Balance)
	require.Equal(t, 7.0, u.FrozenBalance)
}

func TestRefundHoldSubscriptionFailurePreservesRenewal(t *testing.T) {
	ctx := context.Background()
	client, svc, plan, prov := heldRefundFixture(t)
	g, err := client.Group.Create().SetName("subscription refund hold").Save(ctx)
	require.NoError(t, err)
	expiry := time.Now().AddDate(0, 0, 20).Truncate(time.Second)
	sub, err := client.UserSubscription.Create().SetUserID(plan.Order.UserID).SetGroupID(g.ID).SetStartsAt(time.Now()).SetExpiresAt(expiry).Save(ctx)
	require.NoError(t, err)
	plan.Order, err = client.PaymentOrder.UpdateOne(plan.Order).SetOrderType(payment.OrderTypeSubscription).SetSubscriptionGroupID(g.ID).SetSubscriptionDays(30).Save(ctx)
	require.NoError(t, err)
	plan.DeductionType = payment.DeductionTypeSubscription
	plan.BalanceToDeduct = 0
	plan.SubDaysToDeduct = 30
	plan.SubscriptionID = sub.ID
	_, err = svc.ExecuteRefund(ctx, plan)
	require.NoError(t, err)
	held, err := client.UserSubscription.Get(ctx, sub.ID)
	require.NoError(t, err)
	require.Equal(t, SubscriptionStatusExpired, held.Status)
	require.True(t, held.ExpiresAt.Equal(expiry.AddDate(0, 0, -30)))
	subSvc := &SubscriptionService{userSubRepo: &refundSubscriptionTestRepo{db: client}, entClient: client}
	_, err = subSvc.ExtendSubscription(ctx, sub.ID, 40)
	require.NoError(t, err)
	prov.queryStatus = payment.ProviderStatusFailed
	_, err = svc.QueryAndFinalizeRefund(ctx, plan.OrderID)
	require.NoError(t, err)
	restored, err := client.UserSubscription.Get(ctx, sub.ID)
	require.NoError(t, err)
	require.True(t, restored.ExpiresAt.Equal(expiry.AddDate(0, 0, 40)))
	_, err = client.PaymentAuditLog.Delete().Where(paymentauditlog.ActionEQ("REFUND_PENDING")).Exec(ctx)
	require.NoError(t, err)
}

func TestRefundLegacyShortfallStaysRecoverable(t *testing.T) {
	ctx := context.Background()
	client, svc, plan, prov := heldRefundFixture(t)
	require.NoError(t, client.PaymentOrder.UpdateOneID(plan.OrderID).SetStatus(OrderStatusRefundPending).Exec(ctx))
	require.NoError(t, client.User.UpdateOneID(plan.Order.UserID).SetBalance(20).Exec(ctx))
	svc.userRepo = &mockUserRepo{deductAvailableBalanceFn: func(ctx context.Context, id int64, _ float64) (float64, error) {
		tx := dbent.TxFromContext(ctx)
		require.NotNil(t, tx)
		return 20, tx.User.UpdateOneID(id).AddBalance(-20).Exec(ctx)
	}}
	prov.queryStatus = payment.ProviderStatusSuccess
	_, err := svc.QueryAndFinalizeRefund(ctx, plan.OrderID)
	require.Equal(t, "REFUND_BALANCE_SHORTFALL", infraerrors.Reason(err))
	o, err := client.PaymentOrder.Get(ctx, plan.OrderID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefundPending, o.Status)
	u, err := client.User.Get(ctx, o.UserID)
	require.NoError(t, err)
	require.Equal(t, 20.0, u.Balance)
}

func TestRefundHoldExplicitProviderFailureReturnsReservation(t *testing.T) {
	ctx := context.Background()
	client, svc, plan, prov := heldRefundFixture(t)
	prov.refundStatus = payment.ProviderStatusFailed
	result, err := svc.ExecuteRefund(ctx, plan)
	require.NoError(t, err)
	require.False(t, result.Success)
	u, err := client.User.Get(ctx, plan.Order.UserID)
	require.NoError(t, err)
	require.Equal(t, 100.0, u.Balance)
	require.Equal(t, 7.0, u.FrozenBalance)
	o, err := client.PaymentOrder.Get(ctx, plan.OrderID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefundFailed, o.Status)
	require.False(t, o.RefundState.DeductionHeld)
}

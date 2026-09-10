//go:build unit

package provider

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/smartwalle/alipay/v3"
)

func TestAlipayRefundUsesIdempotencyKeyAsOutRequestNo(t *testing.T) {
	origRefund := alipayTradeRefund
	t.Cleanup(func() { alipayTradeRefund = origRefund })

	var captured alipay.TradeRefund
	alipayTradeRefund = func(_ context.Context, _ *alipay.Client, param alipay.TradeRefund) (*alipay.TradeRefundRsp, error) {
		captured = param
		return &alipay.TradeRefundRsp{FundChange: alipayFundChangeYes, TradeNo: "2024001"}, nil
	}

	provider := &Alipay{client: &alipay.Client{}, config: map[string]string{}}
	resp, err := provider.Refund(context.Background(), payment.RefundRequest{
		OrderID:        "sub2_100",
		Amount:         "10.00",
		Reason:         "partial",
		IdempotencyKey: "sub2_100-refund-fixed",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured.OutRequestNo != "sub2_100-refund-fixed" {
		t.Fatalf("OutRequestNo = %q, want persisted idempotency key", captured.OutRequestNo)
	}
	if resp.RefundID != "2024001" || resp.Status != payment.ProviderStatusSuccess {
		t.Fatalf("unexpected refund response: %+v", resp)
	}
}

func TestAlipayRefundGeneratesOutRequestNoOnlyWhenUnset(t *testing.T) {
	origRefund := alipayTradeRefund
	t.Cleanup(func() { alipayTradeRefund = origRefund })

	var generated []string
	alipayTradeRefund = func(_ context.Context, _ *alipay.Client, param alipay.TradeRefund) (*alipay.TradeRefundRsp, error) {
		generated = append(generated, param.OutRequestNo)
		return &alipay.TradeRefundRsp{FundChange: alipayFundChangeYes, TradeNo: "2024002"}, nil
	}

	provider := &Alipay{client: &alipay.Client{}, config: map[string]string{}}
	req := payment.RefundRequest{OrderID: "sub2_101", Amount: "5.00", Reason: "unset"}
	if _, err := provider.Refund(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := provider.Refund(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(generated) != 2 {
		t.Fatalf("generated = %d, want 2", len(generated))
	}
	for _, id := range generated {
		if !strings.HasPrefix(id, "sub2_101-refund-") {
			t.Fatalf("generated OutRequestNo = %q, want sub2_101-refund-* prefix", id)
		}
	}
	if generated[0] == generated[1] {
		t.Fatal("unset IdempotencyKey must generate a new OutRequestNo per call")
	}

	generated = nil
	if _, err := provider.Refund(context.Background(), payment.RefundRequest{
		OrderID:        "sub2_101",
		Amount:         "5.00",
		IdempotencyKey: "sub2_101-refund-reuse",
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(generated) != 1 || generated[0] != "sub2_101-refund-reuse" {
		t.Fatalf("set IdempotencyKey must not generate, got %v", generated)
	}
}

func TestAlipayQueryRefundUsesOutRequestNo(t *testing.T) {
	origQuery := alipayTradeFastPayRefundQuery
	t.Cleanup(func() { alipayTradeFastPayRefundQuery = origQuery })

	var captured alipay.TradeFastPayRefundQuery
	alipayTradeFastPayRefundQuery = func(_ context.Context, _ *alipay.Client, param alipay.TradeFastPayRefundQuery) (*alipay.TradeFastPayRefundQueryRsp, error) {
		captured = param
		return &alipay.TradeFastPayRefundQueryRsp{
			Error:        alipay.Error{Code: alipay.CodeSuccess},
			TradeNo:      "2024003",
			OutRequestNo: param.OutRequestNo,
			RefundStatus: alipayRefundStatusSuccess,
		}, nil
	}

	provider := &Alipay{client: &alipay.Client{}, config: map[string]string{}}
	resp, err := provider.QueryRefund(context.Background(), payment.RefundQueryRequest{
		OrderID:      "sub2_102",
		TradeNo:      "2024003",
		RefundID:     "ignored-when-out-request-set",
		OutRequestNo: "sub2_102-refund-fixed",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured.OutRequestNo != "sub2_102-refund-fixed" {
		t.Fatalf("query OutRequestNo = %q", captured.OutRequestNo)
	}
	if len(captured.QueryOptions) != 0 {
		t.Fatalf("fastpay query must not send query_options, got %v", captured.QueryOptions)
	}
	if captured.OutTradeNo != "sub2_102" || captured.TradeNo != "2024003" {
		t.Fatalf("query identifiers = %+v", captured)
	}
	if resp.Status != payment.ProviderStatusSuccess || resp.RefundID != "2024003" {
		t.Fatalf("unexpected query response: %+v", resp)
	}
}

func TestAlipayQueryRefundFallsBackToRefundID(t *testing.T) {
	origQuery := alipayTradeFastPayRefundQuery
	t.Cleanup(func() { alipayTradeFastPayRefundQuery = origQuery })

	alipayTradeFastPayRefundQuery = func(_ context.Context, _ *alipay.Client, param alipay.TradeFastPayRefundQuery) (*alipay.TradeFastPayRefundQueryRsp, error) {
		if param.OutRequestNo != "fallback-refund-id" {
			t.Fatalf("OutRequestNo = %q, want RefundID fallback", param.OutRequestNo)
		}
		return &alipay.TradeFastPayRefundQueryRsp{
			Error:        alipay.Error{Code: alipay.CodeSuccess},
			RefundStatus: "",
		}, nil
	}

	provider := &Alipay{client: &alipay.Client{}, config: map[string]string{}}
	resp, err := provider.QueryRefund(context.Background(), payment.RefundQueryRequest{RefundID: "fallback-refund-id"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != payment.ProviderStatusPending {
		t.Fatalf("status = %q, want pending", resp.Status)
	}
}

func TestAlipayRefundFundChangeNoIsPending(t *testing.T) {
	origRefund := alipayTradeRefund
	t.Cleanup(func() { alipayTradeRefund = origRefund })

	alipayTradeRefund = func(_ context.Context, _ *alipay.Client, param alipay.TradeRefund) (*alipay.TradeRefundRsp, error) {
		if param.QueryOptions != nil {
			t.Fatal("TradeRefund must not send query_options")
		}
		return &alipay.TradeRefundRsp{FundChange: "N", TradeNo: "2024004"}, nil
	}

	provider := &Alipay{client: &alipay.Client{}, config: map[string]string{}}
	resp, err := provider.Refund(context.Background(), payment.RefundRequest{
		OrderID:        "sub2_104",
		Amount:         "10.00",
		IdempotencyKey: "sub2_104-refund-fixed",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != payment.ProviderStatusPending {
		t.Fatalf("FundChange=N status = %q, want pending", resp.Status)
	}
}

func TestAlipayQueryRefundBlankStatusIsPending(t *testing.T) {
	origQuery := alipayTradeFastPayRefundQuery
	t.Cleanup(func() { alipayTradeFastPayRefundQuery = origQuery })

	alipayTradeFastPayRefundQuery = func(_ context.Context, _ *alipay.Client, param alipay.TradeFastPayRefundQuery) (*alipay.TradeFastPayRefundQueryRsp, error) {
		if len(param.QueryOptions) != 0 {
			t.Fatalf("fastpay refund query must not send query_options, got %v", param.QueryOptions)
		}
		return &alipay.TradeFastPayRefundQueryRsp{
			Error:        alipay.Error{Code: alipay.CodeSuccess},
			OutRequestNo: param.OutRequestNo,
			RefundStatus: "",
		}, nil
	}

	provider := &Alipay{client: &alipay.Client{}, config: map[string]string{}}
	resp, err := provider.QueryRefund(context.Background(), payment.RefundQueryRequest{OutRequestNo: "sub2_105-refund-fixed"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != payment.ProviderStatusPending {
		t.Fatalf("blank refund_status = %q, want pending", resp.Status)
	}
}

func TestAlipayQueryRefundRequiresOutRequestNo(t *testing.T) {
	provider := &Alipay{client: &alipay.Client{}, config: map[string]string{}}
	_, err := provider.QueryRefund(context.Background(), payment.RefundQueryRequest{})
	if err == nil || !strings.Contains(err.Error(), "missing out_request_no") {
		t.Fatalf("expected missing out_request_no error, got %v", err)
	}
}

func TestAlipayQueryRefundPropagatesQueryError(t *testing.T) {
	origQuery := alipayTradeFastPayRefundQuery
	t.Cleanup(func() { alipayTradeFastPayRefundQuery = origQuery })

	alipayTradeFastPayRefundQuery = func(_ context.Context, _ *alipay.Client, _ alipay.TradeFastPayRefundQuery) (*alipay.TradeFastPayRefundQueryRsp, error) {
		return nil, errors.New("timeout")
	}

	provider := &Alipay{client: &alipay.Client{}, config: map[string]string{}}
	_, err := provider.QueryRefund(context.Background(), payment.RefundQueryRequest{OutRequestNo: "req-1"})
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("expected wrapped timeout, got %v", err)
	}
}

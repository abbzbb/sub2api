package domain

// PaymentRefundState keeps provider confirmation and the exact remaining debit
// together with the order. Audit log retention must not affect refund recovery.
type PaymentRefundState struct {
	Version         int     `json:"version"`
	AttemptID       string  `json:"attempt_id"`
	ProviderStatus  string  `json:"provider_status"`
	RefundID        string  `json:"refund_id"`
	OutRequestNo    string  `json:"out_request_no,omitempty"`
	RefundAmount    float64 `json:"refund_amount,omitempty"`
	GatewayAmount   float64 `json:"gateway_amount,omitempty"`
	Currency        string  `json:"currency,omitempty"`
	PriorStatus     string  `json:"prior_status,omitempty"`
	DeductionType   string  `json:"deduction_type"`
	BalanceToDeduct float64 `json:"balance_to_deduct"`
	SubDaysToDeduct int     `json:"sub_days_to_deduct"`
	SubscriptionID  int64   `json:"subscription_id"`
}

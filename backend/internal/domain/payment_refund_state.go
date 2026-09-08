package domain

// PaymentRefundState keeps provider confirmation and the exact remaining debit
// together with the order. Audit log retention must not affect refund recovery.
type PaymentRefundState struct {
	Version         int     `json:"version"`
	AttemptID       string  `json:"attempt_id"`
	ProviderStatus  string  `json:"provider_status"`
	RefundID        string  `json:"refund_id"`
	DeductionType   string  `json:"deduction_type"`
	BalanceToDeduct float64 `json:"balance_to_deduct"`
	SubDaysToDeduct int     `json:"sub_days_to_deduct"`
	SubscriptionID  int64   `json:"subscription_id"`
}

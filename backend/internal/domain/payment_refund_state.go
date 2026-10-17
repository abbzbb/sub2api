package domain

import "time"

// PaymentRefundState keeps provider confirmation and the exact remaining debit
// together with the order. Audit log retention must not affect refund recovery.
type PaymentRefundState struct {
	// Holds are stored with the operation so retries never reserve or release twice.
	DeductionHeld            bool       `json:"deduction_held,omitempty"`
	BalanceHeld              float64    `json:"balance_held,omitempty"`
	SubscriptionExpiryHeld   *time.Time `json:"subscription_expiry_held,omitempty"`
	SubscriptionStatusBefore string     `json:"subscription_status_before,omitempty"`
	Version                  int        `json:"version"`
	AttemptID                string     `json:"attempt_id"`
	ProviderStatus           string     `json:"provider_status"`
	RefundID                 string     `json:"refund_id"`
	OutRequestNo             string     `json:"out_request_no,omitempty"`
	RefundAmount             float64    `json:"refund_amount,omitempty"`
	GatewayAmount            float64    `json:"gateway_amount,omitempty"`
	Currency                 string     `json:"currency,omitempty"`
	PriorStatus              string     `json:"prior_status,omitempty"`
	DeductionType            string     `json:"deduction_type"`
	BalanceToDeduct          float64    `json:"balance_to_deduct"`
	SubDaysToDeduct          int        `json:"sub_days_to_deduct"`
	SubscriptionID           int64      `json:"subscription_id"`

	// A renewal can absorb part of a held term when it restarts from now.
	// nil preserves the full calendar-day return for existing reservations.
	SubscriptionReturnNanos *int64 `json:"subscription_return_nanos,omitempty"`
}

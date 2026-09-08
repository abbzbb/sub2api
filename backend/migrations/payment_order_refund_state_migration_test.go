package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPaymentOrderRefundStateMigration(t *testing.T) {
	content, err := FS.ReadFile("235_payment_order_refund_state.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "ALTER TABLE payment_orders ADD COLUMN IF NOT EXISTS refund_state JSONB")
	require.NotContains(t, sql, "DEFAULT")
	require.NotContains(t, sql, "UPDATE payment_orders")
}

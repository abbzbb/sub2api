-- NULL preserves unknown legacy recovery state; do not infer a completed debit.
ALTER TABLE payment_orders
ADD COLUMN IF NOT EXISTS refund_state JSONB;

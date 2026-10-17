-- Old executors can rebuild v1 state without validating an existing v2 hold.
-- Fence that write in the database as well as keeping uncertain holds pending.
CREATE OR REPLACE FUNCTION protect_payment_refund_hold() RETURNS trigger AS $$
BEGIN
    IF OLD.refund_state ->> 'deduction_held' = 'true'
       AND (OLD.refund_state ->> 'version')::integer >= 2
       AND COALESCE((NEW.refund_state ->> 'version')::integer, 0) < 2 THEN
        RAISE EXCEPTION 'reserved refund state cannot be downgraded'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS payment_refund_hold_fence ON payment_orders;
CREATE TRIGGER payment_refund_hold_fence
    BEFORE UPDATE OF refund_state ON payment_orders
    FOR EACH ROW EXECUTE FUNCTION protect_payment_refund_hold();

UPDATE payment_orders
SET status = 'REFUND_PENDING'
WHERE status IN ('COMPLETED', 'REFUND_REQUESTED', 'REFUND_FAILED')
  AND (refund_state ->> 'version')::integer >= 2
  AND refund_state ->> 'deduction_held' = 'true'
  AND refund_state ->> 'provider_status' = 'pending';

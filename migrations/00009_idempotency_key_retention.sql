-- +goose Up

-- Idempotency keys are kept for a retention window, then deleted by the
-- sweeper, which acts as the System. Each user still sees only their own
-- keys; the System may additionally see and delete anyone's.
--
-- The SELECT policy is needed because a DELETE whose WHERE reads columns
-- (here started_at) only reaches rows the SELECT policies make visible.
-- Stored responses can hold raw provider payloads, which the System may
-- already read (check_payloads_read), so this widens nothing.
CREATE POLICY idempotency_keys_system_read ON idempotency_keys FOR SELECT
    USING (current_setting('app.user_role', true) = 'system');
CREATE POLICY idempotency_keys_system_expire ON idempotency_keys FOR DELETE
    USING (current_setting('app.user_role', true) = 'system');

-- +goose Down

DROP POLICY idempotency_keys_system_expire ON idempotency_keys;
DROP POLICY idempotency_keys_system_read ON idempotency_keys;

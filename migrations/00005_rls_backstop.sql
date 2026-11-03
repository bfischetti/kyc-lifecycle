-- +goose Up

-- Row-level security as a backstop to the application's policy module
-- (ADR-0001). Policies key on the caller role the server sets per
-- transaction (app.user_role). With no role set, nothing is visible.
-- FORCE makes the policies apply to the table owner as well.

-- Raw provider payloads: compliance and the system only.
ALTER TABLE check_payloads ENABLE ROW LEVEL SECURITY;
ALTER TABLE check_payloads FORCE ROW LEVEL SECURITY;

CREATE POLICY check_payloads_read ON check_payloads FOR SELECT
    USING (current_setting('app.user_role', true) IN ('compliance_officer', 'system'));
CREATE POLICY check_payloads_write ON check_payloads FOR INSERT
    WITH CHECK (current_setting('app.user_role', true) IN ('compliance_officer', 'system'));

-- Audit trail: readable by compliance and the system only. Anyone acting in
-- the lifecycle may append to it (e.g. a support agent completing an
-- upload), but cannot read it back.
ALTER TABLE status_history ENABLE ROW LEVEL SECURITY;
ALTER TABLE status_history FORCE ROW LEVEL SECURITY;

CREATE POLICY status_history_read ON status_history FOR SELECT
    USING (current_setting('app.user_role', true) IN ('compliance_officer', 'system'));
CREATE POLICY status_history_append ON status_history FOR INSERT
    WITH CHECK (current_setting('app.user_role', true) IN ('compliance_officer', 'support_agent', 'system'));

-- +goose Down

DROP POLICY status_history_append ON status_history;
DROP POLICY status_history_read ON status_history;
ALTER TABLE status_history NO FORCE ROW LEVEL SECURITY;
ALTER TABLE status_history DISABLE ROW LEVEL SECURITY;

DROP POLICY check_payloads_write ON check_payloads;
DROP POLICY check_payloads_read ON check_payloads;
ALTER TABLE check_payloads NO FORCE ROW LEVEL SECURITY;
ALTER TABLE check_payloads DISABLE ROW LEVEL SECURITY;

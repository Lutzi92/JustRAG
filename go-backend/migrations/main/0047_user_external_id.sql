-- +goose Up
-- OIDC login support — adds external_id (OIDC `sub`) to users so subsequent
-- logins match on the immutable IdP-issued subject rather than email. On
-- first OIDC login the callback handler links by case-insensitive email to
-- preserve the user UUID (and therefore all KB ownership / share rows) for
-- accounts that were originally provisioned via LDAP.
--
-- The unique partial index enforces that one OIDC `sub` maps to one user
-- row, while still allowing every legacy (LDAP / local) user to keep
-- external_id NULL without competing for the unique slot.

-- +goose StatementBegin
ALTER TABLE users ADD COLUMN IF NOT EXISTS external_id varchar(255);

CREATE UNIQUE INDEX IF NOT EXISTS users_external_id_unique_idx
    ON users (external_id) WHERE external_id IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS users_external_id_unique_idx;
ALTER TABLE users DROP COLUMN IF EXISTS external_id;
-- +goose StatementEnd

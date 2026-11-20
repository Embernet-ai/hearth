-- The signed-in person's Vord tokens, kept server-side for the EmberOPS back end.
--
-- With HEARTH_BACKEND=emberops, EmberOPS is called with the person's own Vord
-- access token as the bearer, so their permissions in EmberOPS apply. The
-- access and refresh tokens are sealed with the same key as every other
-- readable-back secret (internal/platform/secret), bound to the session id, and
-- live and die with the session row. NULL on every session in the default
-- back end, and on any session that did not come from a Vord sign-in.

-- +goose Up
ALTER TABLE sessions ADD COLUMN vord_tokens bytea;

-- +goose Down
ALTER TABLE sessions DROP COLUMN IF EXISTS vord_tokens;

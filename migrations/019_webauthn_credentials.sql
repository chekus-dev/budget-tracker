-- Passkeys (fingerprint / face / device PIN sign-in). One row per registered
-- device. `credential` holds the go-webauthn Credential as JSON so the library's
-- own struct round-trips without field-by-field mapping.
CREATE TABLE IF NOT EXISTS webauthn_credentials (
  id            BIGSERIAL PRIMARY KEY,
  user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  credential_id BYTEA NOT NULL UNIQUE,
  credential    JSONB NOT NULL,
  name          TEXT NOT NULL DEFAULT 'Passkey',
  created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  last_used_at  TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_webauthn_user ON webauthn_credentials (user_id);

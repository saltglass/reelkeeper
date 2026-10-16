-- Accounts and sign-in sessions.
--
-- users: one row per person. password_hash is NULL for accounts that only
-- sign in through single sign-on; oidc_issuer/oidc_subject identify those.
-- sessions: server-side sessions. Only a SHA-256 hash of the session token
-- is stored, so a copy of the database can't be used to hijack a session.

-- +goose Up
CREATE TABLE users (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    username      TEXT NOT NULL,
    password_hash TEXT,
    oidc_issuer   TEXT,
    oidc_subject  TEXT,
    created_at    TIMESTAMPTZ NOT NULL,
    last_login_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX users_username_key ON users (lower(username));
CREATE UNIQUE INDEX users_oidc_key ON users (oidc_issuer, oidc_subject);

CREATE TABLE sessions (
    token_hash   TEXT PRIMARY KEY,
    user_id      BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at   TIMESTAMPTZ NOT NULL,
    expires_at   TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL,
    ip           TEXT NOT NULL,
    user_agent   TEXT NOT NULL
);
CREATE INDEX sessions_user ON sessions (user_id);
CREATE INDEX sessions_expires ON sessions (expires_at);

-- +goose Down
DROP TABLE sessions;
DROP TABLE users;

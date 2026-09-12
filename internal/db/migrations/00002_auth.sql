-- +goose Up
CREATE TABLE users (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email         varchar(254) NOT NULL UNIQUE CHECK (email = lower(email)),
    password_hash text         NOT NULL,
    full_name     varchar(100) NOT NULL,
    role          varchar(20)  NOT NULL DEFAULT 'employee'
                  CHECK (role IN ('employee', 'supervisor', 'hr_admin', 'super_admin')),
    is_active     boolean      NOT NULL DEFAULT true,
    created_at    timestamptz  NOT NULL DEFAULT now(),
    updated_at    timestamptz  NOT NULL DEFAULT now()
);

CREATE TABLE refresh_tokens (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id    bigint      NOT NULL REFERENCES users (id),
    token_hash char(64)    NOT NULL UNIQUE, -- sha256 hex; the token itself is never stored
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Revoking every session of a user and dropping their expired tokens both look up by user.
CREATE INDEX refresh_tokens_user_id_idx ON refresh_tokens (user_id);

-- +goose Down
DROP TABLE refresh_tokens;
DROP TABLE users;

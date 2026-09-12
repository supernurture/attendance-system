-- +goose Up
CREATE TABLE departments (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name       varchar(100) NOT NULL,
    created_at timestamptz  NOT NULL DEFAULT now(),
    updated_at timestamptz  NOT NULL DEFAULT now(),
    deleted_at timestamptz
);

-- Unique among the living only, so a deleted name can be used again.
CREATE UNIQUE INDEX departments_live_name_key ON departments (lower(name)) WHERE deleted_at IS NULL;

ALTER TABLE users
    ADD COLUMN department_id bigint REFERENCES departments (id),
    ADD COLUMN manager_id    bigint REFERENCES users (id),
    ADD COLUMN join_date     date NOT NULL DEFAULT CURRENT_DATE,
    ADD COLUMN deleted_at    timestamptz;

-- Same reason as departments: a deleted employee must not hold their address forever.
ALTER TABLE users DROP CONSTRAINT users_email_key;
CREATE UNIQUE INDEX users_live_email_key ON users (email) WHERE deleted_at IS NULL;

CREATE INDEX users_manager_id_idx ON users (manager_id); -- the subtree CTE walks this
CREATE INDEX users_department_id_idx ON users (department_id);

-- One table for the few sensitive actions; actor_id is why a Postgres trigger cannot replace it.
CREATE TABLE audit_logs (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    actor_id    bigint      NOT NULL REFERENCES users (id),
    action      varchar(50) NOT NULL,
    entity_type varchar(50) NOT NULL,
    entity_id   bigint      NOT NULL,
    before      jsonb,
    after       jsonb,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX audit_logs_entity_idx ON audit_logs (entity_type, entity_id);

-- +goose Down
DROP TABLE audit_logs;

DROP INDEX users_department_id_idx;
DROP INDEX users_manager_id_idx;
DROP INDEX users_live_email_key;
ALTER TABLE users ADD CONSTRAINT users_email_key UNIQUE (email);

ALTER TABLE users
    DROP COLUMN deleted_at,
    DROP COLUMN join_date,
    DROP COLUMN manager_id,
    DROP COLUMN department_id;

DROP TABLE departments;

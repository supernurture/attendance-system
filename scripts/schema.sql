CREATE TABLE IF NOT EXISTS example_notes (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    title      VARCHAR(200) NOT NULL,
    body       TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS example_note_events (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    note_id    BIGINT NOT NULL REFERENCES example_notes (id),
    action     VARCHAR(20) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

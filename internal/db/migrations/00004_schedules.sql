-- +goose Up
CREATE TABLE work_schedules (
    id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name              varchar(100) NOT NULL,
    start_time        time         NOT NULL,
    end_time          time         NOT NULL,
    grace_minutes     int          NOT NULL DEFAULT 0 CHECK (grace_minutes BETWEEN 0 AND 240),
    break_minutes     int          NOT NULL DEFAULT 0 CHECK (break_minutes BETWEEN 0 AND 480),
    -- ISO weekdays, 1=Mon..7=Sun. A shift whose end_time <= start_time crosses midnight; that is
    -- derived, never stored, so the two can never disagree.
    workdays          smallint[]   NOT NULL CHECK (
                          array_length(workdays, 1) BETWEEN 1 AND 7
                          AND workdays <@ ARRAY[1, 2, 3, 4, 5, 6, 7]::smallint[]
                      ),
    -- false for the guards and DC engineers who work through public holidays.
    observes_holidays boolean      NOT NULL DEFAULT true,
    created_at        timestamptz  NOT NULL DEFAULT now(),
    updated_at        timestamptz  NOT NULL DEFAULT now(),
    deleted_at        timestamptz
);

CREATE UNIQUE INDEX work_schedules_live_name_key ON work_schedules (lower(name)) WHERE deleted_at IS NULL;

ALTER TABLE users ADD COLUMN default_schedule_id bigint REFERENCES work_schedules (id);

-- One row per person per day. schedule_id NULL means an explicitly assigned day off, which is how a
-- rotating roster says "not this day" without deleting the row.
CREATE TABLE shift_assignments (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id     bigint       NOT NULL REFERENCES users (id),
    work_date   date         NOT NULL,
    schedule_id bigint       REFERENCES work_schedules (id),
    note        varchar(200) NOT NULL DEFAULT '',
    created_at  timestamptz  NOT NULL DEFAULT now(),
    updated_at  timestamptz  NOT NULL DEFAULT now(),
    UNIQUE (user_id, work_date)
);

CREATE INDEX shift_assignments_work_date_idx ON shift_assignments (work_date);

-- date is unique, but the table still carries an id so audit_logs.entity_id can point at a row.
CREATE TABLE holidays (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    date       date         NOT NULL UNIQUE,
    name       varchar(100) NOT NULL,
    created_at timestamptz  NOT NULL DEFAULT now(),
    updated_at timestamptz  NOT NULL DEFAULT now()
);

CREATE TABLE office_locations (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name       varchar(100)     NOT NULL,
    lat        double precision NOT NULL CHECK (lat BETWEEN -90 AND 90),
    lng        double precision NOT NULL CHECK (lng BETWEEN -180 AND 180),
    radius_m   int              NOT NULL CHECK (radius_m BETWEEN 10 AND 10000),
    created_at timestamptz      NOT NULL DEFAULT now(),
    updated_at timestamptz      NOT NULL DEFAULT now(),
    deleted_at timestamptz
);

CREATE UNIQUE INDEX office_locations_live_name_key ON office_locations (lower(name)) WHERE deleted_at IS NULL;

-- +goose Down
DROP TABLE office_locations;
DROP TABLE holidays;
DROP TABLE shift_assignments;
ALTER TABLE users DROP COLUMN default_schedule_id;
DROP TABLE work_schedules;

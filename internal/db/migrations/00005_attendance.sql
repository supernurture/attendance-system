-- +goose Up
-- One row per person per work date; the daily report lives here too, since its key is the same. work_date is
-- its own column, never DATE(check_in_at): a 22:00-06:00 shift would otherwise split across two days.
CREATE TABLE attendances (
    id                        bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id                   bigint           NOT NULL REFERENCES users (id),
    work_date                 date             NOT NULL,
    schedule_id               bigint           REFERENCES work_schedules (id), -- the hours in force at check-in
    check_in_at               timestamptz      NOT NULL,
    check_out_at              timestamptz,
    -- The evidence columns are empty on a side an approved correction filled in: it proves nothing.
    check_in_lat              double precision CHECK (check_in_lat BETWEEN -90 AND 90),
    check_in_lng              double precision CHECK (check_in_lng BETWEEN -180 AND 180),
    check_in_accuracy_m       double precision CHECK (check_in_accuracy_m >= 0),
    check_in_photo_key        text             UNIQUE,
    check_in_location_id      bigint           REFERENCES office_locations (id),
    check_in_within_geofence  boolean,
    check_in_mock_location    boolean,
    check_out_lat             double precision CHECK (check_out_lat BETWEEN -90 AND 90),
    check_out_lng             double precision CHECK (check_out_lng BETWEEN -180 AND 180),
    check_out_accuracy_m      double precision CHECK (check_out_accuracy_m >= 0),
    check_out_photo_key       text             UNIQUE,
    check_out_location_id     bigint           REFERENCES office_locations (id),
    check_out_within_geofence boolean,
    check_out_mock_location   boolean,
    late_minutes              int              NOT NULL DEFAULT 0 CHECK (late_minutes >= 0),
    early_leave_minutes       int              NOT NULL DEFAULT 0 CHECK (early_leave_minutes >= 0),
    daily_report              text,
    daily_report_updated_at   timestamptz,
    created_at                timestamptz      NOT NULL DEFAULT now(),
    updated_at                timestamptz      NOT NULL DEFAULT now(),
    UNIQUE (user_id, work_date),
    CHECK (check_out_at IS NULL OR check_out_at > check_in_at),
    -- A side's evidence is all there or all missing; the office may still be unknown.
    CHECK (num_nulls(check_in_lat, check_in_lng, check_in_accuracy_m, check_in_photo_key,
                     check_in_within_geofence, check_in_mock_location) IN (0, 6)),
    CHECK (num_nulls(check_out_lat, check_out_lng, check_out_accuracy_m, check_out_photo_key,
                     check_out_within_geofence, check_out_mock_location) IN (0, 6)),
    CHECK (check_out_at IS NOT NULL OR check_out_photo_key IS NULL)
);

CREATE INDEX attendances_work_date_idx ON attendances (work_date);

-- The only way attendance changes after the fact, so this table is its own audit trail.
CREATE TABLE attendance_corrections (
    id                    bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id               bigint       NOT NULL REFERENCES users (id),
    work_date             date         NOT NULL,
    requested_by          bigint       NOT NULL REFERENCES users (id), -- hr_admin filing on someone's behalf
    proposed_check_in_at  timestamptz,
    proposed_check_out_at timestamptz,
    old_check_in_at       timestamptz, -- snapshot taken when approved
    old_check_out_at      timestamptz,
    reason                varchar(500) NOT NULL,
    status                varchar(20)  NOT NULL DEFAULT 'pending'
                          CHECK (status IN ('pending', 'approved', 'rejected', 'cancelled')),
    reviewed_by           bigint       REFERENCES users (id),
    reviewed_at           timestamptz,
    review_note           varchar(500),
    created_at            timestamptz  NOT NULL DEFAULT now(),
    CHECK (num_nonnulls(proposed_check_in_at, proposed_check_out_at) > 0),
    CHECK (proposed_check_out_at IS NULL OR proposed_check_in_at IS NULL
           OR proposed_check_out_at > proposed_check_in_at)
);

-- One open request per day, so two approvals cannot overwrite each other's times.
CREATE UNIQUE INDEX attendance_corrections_pending_key ON attendance_corrections (user_id, work_date)
    WHERE status = 'pending';

-- +goose Down
DROP TABLE attendance_corrections;
DROP TABLE attendances;

-- +goose Up
-- One row per kind of leave, so a new kind is an INSERT rather than a deploy. Keeping them apart is a legal
-- requirement: Article 93 of the Manpower Act bars special leave from reducing the annual quota.
CREATE TABLE leave_types (
    id                            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    code                          varchar(50)  NOT NULL CHECK (code ~ '^[a-z][a-z0-9_]*$'),
    name                          varchar(100) NOT NULL,
    quota_days_per_year           int CHECK (quota_days_per_year BETWEEN 0 AND 366),           -- NULL: no yearly quota
    max_working_days_per_request  int CHECK (max_working_days_per_request BETWEEN 1 AND 366),  -- NULL: no cap
    attachment_required_from_days int CHECK (attachment_required_from_days BETWEEN 1 AND 366), -- NULL: never
    is_paid                       boolean      NOT NULL DEFAULT true,
    auto_approve                  boolean      NOT NULL DEFAULT false,
    created_at                    timestamptz  NOT NULL DEFAULT now(),
    updated_at                    timestamptz  NOT NULL DEFAULT now(),
    deleted_at                    timestamptz
);

CREATE UNIQUE INDEX leave_types_live_code_key ON leave_types (code) WHERE deleted_at IS NULL;

-- Articles 79, 82 and 93. Only annual has a quota; the special kinds cap each occurrence instead.
INSERT INTO leave_types (code, name, quota_days_per_year, max_working_days_per_request,
                         attachment_required_from_days, is_paid)
VALUES ('annual', 'Cuti Tahunan', 12, NULL, NULL, true),
       ('sick', 'Sakit', NULL, NULL, 2, true),
       ('maternity', 'Melahirkan', NULL, NULL, 1, true),
       ('marriage', 'Menikah', NULL, 3, 1, true),
       ('child_marriage', 'Menikahkan anak', NULL, 2, 1, true),
       ('child_ceremony', 'Khitan/baptis anak', NULL, 2, 1, true),
       ('childbirth_spouse', 'Istri melahirkan/keguguran', NULL, 2, 1, true),
       ('bereavement_core', 'Kematian keluarga inti', NULL, 2, 1, true),
       ('bereavement_household', 'Kematian anggota serumah', NULL, 1, 1, true),
       ('religious_pilgrimage', 'Ibadah haji', NULL, 50, 1, true),
       ('unpaid', 'Di luar tanggungan', NULL, NULL, NULL, false);

-- The quota only. Days used are summed from leave_requests every time, so no counter can drift from them.
CREATE TABLE leave_balances (
    user_id           bigint      NOT NULL REFERENCES users (id),
    leave_type_id     bigint      NOT NULL REFERENCES leave_types (id),
    year              int         NOT NULL CHECK (year BETWEEN 2000 AND 9999),
    quota_days        int         NOT NULL CHECK (quota_days BETWEEN 0 AND 366),
    carried_over_days int         NOT NULL DEFAULT 0 CHECK (carried_over_days BETWEEN 0 AND 366),
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, leave_type_id, year)
);

CREATE TABLE leave_requests (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id         bigint       NOT NULL REFERENCES users (id),
    leave_type_id   bigint       NOT NULL REFERENCES leave_types (id),
    start_date      date         NOT NULL,
    end_date        date         NOT NULL,
    working_days    int          NOT NULL CHECK (working_days > 0), -- rule A, counted when filed
    reason          varchar(500) NOT NULL,
    attachment_key  text         UNIQUE,
    attachment_mime varchar(50),
    status          varchar(20)  NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'approved', 'rejected', 'cancelled')),
    reviewed_by     bigint       REFERENCES users (id),
    reviewed_at     timestamptz,
    review_note     varchar(500),
    cancelled_by    bigint       REFERENCES users (id), -- who withdrew it: the owner, or hr_admin
    cancelled_at    timestamptz,
    created_at      timestamptz  NOT NULL DEFAULT now(),
    CHECK (end_date >= start_date),
    CHECK (num_nulls(cancelled_by, cancelled_at) IN (0, 2)),
    CHECK ((status = 'cancelled') = (cancelled_at IS NOT NULL)),
    CHECK (num_nulls(attachment_key, attachment_mime) IN (0, 2)),
    -- Nobody is on two live leaves on one day; a rejected or cancelled one frees its dates.
    CONSTRAINT leave_requests_no_overlap EXCLUDE USING gist (
        user_id WITH =, daterange(start_date, end_date, '[]') WITH &&
    ) WHERE (status IN ('pending', 'approved'))
);

-- +goose Down
DROP TABLE leave_requests;
DROP TABLE leave_balances;
DROP TABLE leave_types;

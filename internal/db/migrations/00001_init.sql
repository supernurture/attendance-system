-- +goose Up
-- btree_gist backs the EXCLUDE constraint against overlapping leave requests (phase 5).
CREATE EXTENSION IF NOT EXISTS btree_gist;

-- +goose Down
-- Not dropped on purpose: it is cluster-wide and may predate this service.
SELECT 1;

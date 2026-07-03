-- +goose Up
-- +goose StatementBegin

-- Keyset (cursor) pagination needs a UNIQUE sort key so a cursor can resume
-- deterministically. date_and_time alone is not unique (two entries can share a
-- timestamp), so the keyset is the pair (date_and_time, id) and the index adds
-- id DESC as the tiebreaker. The row-value comparison in the cursor query —
--   WHERE (date_and_time, id) < ($cursorDate, $cursorId)
--   ORDER BY date_and_time DESC, id DESC
-- maps directly onto this composite, and the offset query shares the same
-- ORDER BY so both pagination modes use one index.
--
-- Build the new index BEFORE dropping the old (user_id, date_and_time DESC) one
-- so the query shape is covered at every instant. The old index is fully
-- subsumed by the new one (a prefix of the same columns), so it is then dropped.
--
-- Plain CREATE INDEX (not CONCURRENTLY) is used because goose runs each
-- migration in a transaction and CONCURRENTLY cannot run inside one. On a large
-- existing table, split this into its own NO TRANSACTION migration using
-- CREATE INDEX CONCURRENTLY to avoid a write lock — see README "Migrations".
CREATE INDEX idx_logs_user_datetime_id ON logs(user_id, date_and_time DESC, id DESC);
DROP INDEX IF EXISTS idx_logs_user_datetime;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

CREATE INDEX idx_logs_user_datetime ON logs(user_id, date_and_time DESC);
DROP INDEX IF EXISTS idx_logs_user_datetime_id;

-- +goose StatementEnd

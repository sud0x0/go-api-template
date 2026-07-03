-- +goose Up
-- +goose StatementBegin

-- gen_random_uuid() is built into core Postgres since v13 (no extension needed).
-- We deliberately do NOT `CREATE EXTENSION pgcrypto` — that requires elevated
-- privileges the migrator role should not need. Targets Postgres 13+.

-- Logs table
-- Allows multiple log entries per user with a timestamp.
-- Use id as the primary lookup key.
CREATE TABLE logs (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID        NOT NULL,
    date_and_time TIMESTAMPTZ NOT NULL,
    log          TEXT        NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Indexes
-- All read queries filter on user_id first, so the (user_id, date_and_time)
-- composite index covers both the user-scoped list and the user-scoped
-- date-range list. A standalone date_and_time index is intentionally NOT
-- created — no query filters on date_and_time alone, so it would only cost
-- writes and disk for no read benefit.
CREATE INDEX idx_logs_user_id        ON logs(user_id);
CREATE INDEX idx_logs_user_datetime  ON logs(user_id, date_and_time DESC);

-- updated_at trigger
-- The BEFORE UPDATE trigger sets NEW.updated_at on every row update.
-- Because BEFORE triggers run before RETURNING is evaluated, the value the
-- application reads from RETURNING reflects the trigger-set timestamp.
-- This is the single source of truth for updated_at; UPDATE statements must
-- NOT also set updated_at = NOW() (the trigger covers it).
CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER update_logs_updated_at
    BEFORE UPDATE ON logs
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TRIGGER IF EXISTS update_logs_updated_at ON logs;
DROP FUNCTION IF EXISTS update_updated_at_column();
DROP TABLE IF EXISTS logs CASCADE;

-- +goose StatementEnd

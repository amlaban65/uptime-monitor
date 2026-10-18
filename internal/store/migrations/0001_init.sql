-- Monitor definitions. Durations are stored as milliseconds so that SQL can
-- do arithmetic on them without parsing Go duration strings.
CREATE TABLE monitors (
    id                 bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name               text        NOT NULL UNIQUE,
    kind               text        NOT NULL CHECK (kind IN ('http', 'tcp')),
    target             text        NOT NULL,
    method             text        NOT NULL DEFAULT '',
    expected_status    integer     NOT NULL DEFAULT 0,
    body_contains      text        NOT NULL DEFAULT '',
    interval_ms        bigint      NOT NULL CHECK (interval_ms > 0),
    timeout_ms         bigint      NOT NULL CHECK (timeout_ms > 0),
    failure_threshold  integer     NOT NULL CHECK (failure_threshold > 0),
    recovery_threshold integer     NOT NULL CHECK (recovery_threshold > 0),
    enabled            boolean     NOT NULL DEFAULT true,
    source             text        NOT NULL DEFAULT 'api' CHECK (source IN ('api', 'file')),
    next_run_at        timestamptz NOT NULL DEFAULT now(),
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now()
);

-- The scheduler's hot query: enabled monitors whose next run is due.
CREATE INDEX monitors_due_idx ON monitors (next_run_at) WHERE enabled;

-- Runtime state for the incident state machine, kept apart from monitors so
-- that recording results (which locks this row) never contends with the
-- scheduler claiming the monitor row.
CREATE TABLE monitor_state (
    monitor_id            bigint      PRIMARY KEY REFERENCES monitors (id) ON DELETE CASCADE,
    status                text        NOT NULL DEFAULT 'unknown' CHECK (status IN ('unknown', 'up', 'down')),
    consecutive_failures  integer     NOT NULL DEFAULT 0,
    consecutive_successes integer     NOT NULL DEFAULT 0,
    streak_started_at     timestamptz,
    last_checked_at       timestamptz,
    last_latency_us       bigint,
    last_error            text        NOT NULL DEFAULT ''
);

-- Raw check results, one row per check. This is the table that grows; see
-- the retention job and daily_stats rollup.
CREATE TABLE check_results (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    monitor_id  bigint      NOT NULL REFERENCES monitors (id) ON DELETE CASCADE,
    checked_at  timestamptz NOT NULL,
    ok          boolean     NOT NULL,
    latency_us  bigint      NOT NULL,
    status_code integer,
    error       text        NOT NULL DEFAULT ''
);

-- Per-monitor window queries (uptime over 24h/7d/30d, recent results).
CREATE INDEX check_results_monitor_time_idx ON check_results (monitor_id, checked_at DESC);
-- Retention deletes by time across all monitors. Rows arrive roughly in time
-- order, so a BRIN index is tiny and good enough for range scans.
CREATE INDEX check_results_checked_at_brin ON check_results USING brin (checked_at);

-- Daily rollup maintained in the same transaction as each insert into
-- check_results. Feeds the 90-day bars and survives raw-result retention.
CREATE TABLE daily_stats (
    monitor_id bigint  NOT NULL REFERENCES monitors (id) ON DELETE CASCADE,
    day        date    NOT NULL,
    total      integer NOT NULL,
    ok         integer NOT NULL,
    PRIMARY KEY (monitor_id, day)
);

CREATE TABLE incidents (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    monitor_id  bigint      NOT NULL REFERENCES monitors (id) ON DELETE CASCADE,
    started_at  timestamptz NOT NULL,
    resolved_at timestamptz,
    cause       text        NOT NULL DEFAULT ''
);

-- At most one open incident per monitor, enforced by the database rather
-- than by application logic alone.
CREATE UNIQUE INDEX incidents_one_open_per_monitor ON incidents (monitor_id) WHERE resolved_at IS NULL;
CREATE INDEX incidents_started_at_idx ON incidents (started_at DESC);

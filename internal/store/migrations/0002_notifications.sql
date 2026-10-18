-- Transactional outbox for alerts. Rows are inserted in the same transaction
-- that opens or resolves an incident, so an alert is never lost because the
-- process died between "incident opened" and "message sent".
CREATE TABLE notifications (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    incident_id     bigint      NOT NULL REFERENCES incidents (id) ON DELETE CASCADE,
    event           text        NOT NULL CHECK (event IN ('opened', 'resolved')),
    channel         text        NOT NULL,
    status          text        NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sent', 'failed')),
    attempts        integer     NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    last_error      text        NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    sent_at         timestamptz,
    -- Dedup: one message per incident event per channel, no matter how
    -- many times or by how many replicas the transition is recorded.
    UNIQUE (incident_id, event, channel)
);

CREATE INDEX notifications_pending_idx ON notifications (next_attempt_at) WHERE status = 'pending';

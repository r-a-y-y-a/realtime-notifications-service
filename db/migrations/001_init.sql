CREATE TABLE IF NOT EXISTS notifications (
    id          TEXT PRIMARY KEY,
    user_id     TEXT NOT NULL,
    tenant_id   TEXT NOT NULL,
    title       TEXT NOT NULL,
    body        TEXT NOT NULL,
    channel     TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'unread',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_notifications_user_id ON notifications(user_id);
CREATE INDEX IF NOT EXISTS idx_notifications_tenant_id ON notifications(tenant_id);

-- Idempotency table to prevent duplicate processing from Kafka at-least-once
CREATE TABLE IF NOT EXISTS processed_requests (
    request_id   TEXT PRIMARY KEY,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

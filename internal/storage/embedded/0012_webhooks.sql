-- 0012_webhooks (embedded copy of migrations/0012_webhooks.sql)
-- Adds webhook registrations and their delivery records.
--
-- A webhook carries the URL to call, the event types it subscribes to, and an
-- optional signing secret, so a receiver can verify that a delivery came from
-- this service.

CREATE TABLE IF NOT EXISTS webhooks (
    id          VARCHAR(64)   NOT NULL,
    bank_id     VARCHAR(64)   NOT NULL,
    url         VARCHAR(2048) NOT NULL,
    secret      VARCHAR(512),
    event_types VARCHAR[]     NOT NULL DEFAULT '{}',
    enabled     BOOLEAN       NOT NULL DEFAULT true,
    http_config JSONB         NOT NULL DEFAULT '{}'::jsonb,
    created_at  TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now(),
    updated_at  TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now(),
    PRIMARY KEY (id),
    FOREIGN KEY (bank_id) REFERENCES banks (id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_webhooks_bank ON webhooks (bank_id, created_at DESC);

-- One delivery attempt. The record keeps the outcome so an operator can see
-- whether a receiver is accepting events.
CREATE TABLE IF NOT EXISTS webhook_deliveries (
    id             VARCHAR(64) NOT NULL,
    webhook_id     VARCHAR(64) NOT NULL,
    bank_id        VARCHAR(64) NOT NULL,
    event_type     VARCHAR(128) NOT NULL,
    status         VARCHAR(32)  NOT NULL,
    status_code    INTEGER,
    error          TEXT,
    attempt        INTEGER NOT NULL DEFAULT 1,
    delivered_at   TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now(),
    PRIMARY KEY (id),
    FOREIGN KEY (webhook_id) REFERENCES webhooks (id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_webhook
    ON webhook_deliveries (webhook_id, delivered_at DESC);

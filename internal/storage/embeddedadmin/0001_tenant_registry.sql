-- 0001_tenant_registry
--
-- The control plane's own tables. This series is independent of the reference
-- schema series: it lives in the admin schema, never runs against a tenant
-- schema, and is not regenerated from the reference database.

CREATE TABLE IF NOT EXISTS tenant (
    tenant_id    text PRIMARY KEY,
    display_name text NOT NULL,
    status       text NOT NULL CHECK (status IN ('provisioning', 'active', 'suspended', 'deleted')),
    config       jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    deleted_at   timestamptz
);

CREATE TABLE IF NOT EXISTS tenant_key (
    key_id       uuid PRIMARY KEY,
    tenant_id    text NOT NULL REFERENCES tenant(tenant_id),
    key_hash     bytea NOT NULL UNIQUE,
    prefix       text NOT NULL,
    label        text NOT NULL,
    ephemeral    boolean NOT NULL DEFAULT false,
    expires_at   timestamptz,
    revoked_at   timestamptz,
    last_used_at timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS admin_audit (
    id     bigserial PRIMARY KEY,
    actor  text NOT NULL,
    action text NOT NULL,
    target text NOT NULL,
    detail jsonb NOT NULL DEFAULT '{}'::jsonb,
    at     timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS tenant_key_tenant_id_idx ON tenant_key (tenant_id);
CREATE INDEX IF NOT EXISTS tenant_status_idx ON tenant (status);

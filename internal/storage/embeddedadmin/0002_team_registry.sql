-- 0002_team_registry
--
-- The team registry: a team is a tenant (kind = 'team') with its own schema, and
-- these tables record who belongs to it and which projects it owns.
--
-- Every team table lives here, in the control plane's schema. A tenant schema
-- must stay object-for-object identical to the reference schema, so nothing
-- team-related may be created there.
--
-- Deferred on purpose: the merge ledger (merge_state, merge_source_unit). It
-- belongs to the background merge job, which does not exist yet; a missing
-- merge_state row means "never merged", so nothing here depends on it. It will
-- arrive as 0003 rather than as an edit to this file.

ALTER TABLE tenant ADD COLUMN IF NOT EXISTS kind text NOT NULL DEFAULT 'personal'
    CHECK (kind IN ('personal', 'team'));

-- One row per member of a team. The member is an ordinary tenant; joining a
-- team never creates or alters a personal bank.
CREATE TABLE IF NOT EXISTS team_member (
    team_tenant_id text NOT NULL REFERENCES tenant(tenant_id) ON DELETE CASCADE,
    tenant_id      text NOT NULL REFERENCES tenant(tenant_id) ON DELETE CASCADE,
    role           text NOT NULL DEFAULT 'member' CHECK (role IN ('owner', 'member')),
    joined_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (team_tenant_id, tenant_id)
);
CREATE INDEX IF NOT EXISTS team_member_tenant_idx ON team_member (tenant_id);

-- One row per project of a team.
--
-- bank_id is "<team_tenant_id>::<name>", computed once at creation: both halves
-- are immutable, so the id never has to be recomputed. Two teams may each own a
-- project of the same name; their bank ids differ by the prefix, which is why
-- the uniqueness is scoped to the team.
--
-- Deleting a project cascades its membership rows; the banks it owns are
-- deleted by the management plane, not by this table.
CREATE TABLE IF NOT EXISTS team_project (
    team_tenant_id text NOT NULL REFERENCES tenant(tenant_id) ON DELETE CASCADE,
    project_id     text NOT NULL,
    name           text NOT NULL CHECK (name !~ ':'),
    bank_id        text NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (team_tenant_id, project_id),
    UNIQUE (team_tenant_id, name),
    UNIQUE (team_tenant_id, bank_id)
);

-- Which team members take part in which project. A member may take part in the
-- same-named project of several teams: each team's project is its own.
--
-- Leaving the team removes these rows through the team_member foreign key.
CREATE TABLE IF NOT EXISTS team_project_member (
    team_tenant_id text NOT NULL,
    project_id     text NOT NULL,
    tenant_id      text NOT NULL,
    added_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (team_tenant_id, project_id, tenant_id),
    FOREIGN KEY (team_tenant_id, project_id)
        REFERENCES team_project(team_tenant_id, project_id) ON DELETE CASCADE,
    FOREIGN KEY (team_tenant_id, tenant_id)
        REFERENCES team_member(team_tenant_id, tenant_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS team_project_member_tenant_idx
    ON team_project_member (tenant_id);
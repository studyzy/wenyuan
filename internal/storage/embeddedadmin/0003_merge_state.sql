-- 0003_merge_state
--
-- The merge ledger: which member fact each team fact is made of, and where each
-- (project, member) pair's incremental cursor stood.
--
-- One deviation from the design's original table is load-bearing: the ledger
-- records the merge_key a source supports, not merely that the source was
-- consumed. A source the model judged redundant lands under '__dropped__' and is
-- excluded from delete detection, so removing it does not force a rebuild. A
-- ledger that only said "consumed" would make every fold-away source's deletion
-- trigger a full rebuild — and a rebuild is bounded, so one stale judgement
-- could fail the project closed and empty the team's content.
--
-- This is a multi-tenant-only migration: a single-tenant deployment has no
-- teams, so the admin series skips it (storage.adminTeamMigrations).

CREATE TABLE IF NOT EXISTS merge_state (
    team_tenant_id    text NOT NULL,
    project_id        text NOT NULL,
    tenant_id         text NOT NULL,
    -- The incremental cursor: the last (updated_at, id) pair consumed from this
    -- member's bank. '-infinity' means "never merged", so a missing row and a
    -- fresh row both read the bank from the start.
    cursor_updated_at timestamptz NOT NULL DEFAULT '-infinity',
    cursor_unit_id    uuid NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000',
    last_merged_at    timestamptz,
    last_error        text,
    -- fail_count is the circuit breaker: consecutive failed rounds. At the
    -- configured threshold the project is skipped until it is reset, so a
    -- permanently failing project stops spending a model call every round.
    fail_count        integer NOT NULL DEFAULT 0,
    -- merging_since is the cross-transaction half of the merge lock. The first
    -- transaction stamps it before the model call and the finishing transaction
    -- clears it; a stamp older than the round deadline is a crashed round and
    -- may be taken over. The merge lock itself is a transaction-scoped advisory
    -- lock, which cannot span the model call, so this row carries the claim.
    merging_since     timestamptz,
    rebuild_requested boolean NOT NULL DEFAULT false,
    PRIMARY KEY (team_tenant_id, project_id, tenant_id),
    FOREIGN KEY (team_tenant_id, project_id)
        REFERENCES team_project(team_tenant_id, project_id) ON DELETE CASCADE,
    FOREIGN KEY (team_tenant_id, tenant_id)
        REFERENCES team_member(team_tenant_id, tenant_id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS merge_source_unit (
    team_tenant_id  text NOT NULL,
    project_id      text NOT NULL,
    tenant_id       text NOT NULL,
    source_unit_id  uuid NOT NULL,
    -- The merge_key of the team fact this source supports, or '__dropped__'
    -- when the model folded it away. Never NULL: "consumed but unsupported" and
    -- "not consumed" must not be the same row.
    merge_key       text NOT NULL,
    consumed_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (team_tenant_id, project_id, tenant_id, source_unit_id),
    FOREIGN KEY (team_tenant_id, project_id)
        REFERENCES team_project(team_tenant_id, project_id) ON DELETE CASCADE,
    FOREIGN KEY (team_tenant_id, tenant_id)
        REFERENCES team_member(team_tenant_id, tenant_id) ON DELETE CASCADE
);

-- Delete detection reads one member's live sources and looks for the ones whose
-- member row has gone. The primary key puts source_unit_id last, so this partial
-- index is what keeps that scan over the supporting rows rather than the whole
-- ledger.
CREATE INDEX IF NOT EXISTS merge_source_unit_live_idx
    ON merge_source_unit (team_tenant_id, project_id, tenant_id)
    WHERE merge_key <> '__dropped__';
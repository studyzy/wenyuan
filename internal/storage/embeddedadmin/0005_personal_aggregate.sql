-- 0005_personal_aggregate
--
-- The personal aggregate bank: `<tenant>::myself`, filled by the personal
-- rollup job from every other bank in that tenant's own schema.
--
-- This series is applied to a single-tenant deployment too. The aggregate is not
-- a team feature: a person's own banks are folded together whether the
-- deployment has teams or not, so this file must NOT be added to
-- adminTeamMigrations.
--
-- The ledger is its own set of tables rather than a reuse of the team merge
-- ledger: that one carries foreign keys into the team registry, and a personal
-- aggregate has no row there. Forging one would be writing team meaning into a
-- personal feature.

-- One row per personal tenant that has ever built an aggregate.
CREATE TABLE IF NOT EXISTS personal_state (
    tenant_id          text PRIMARY KEY,
    -- merging_since is the cross-transaction half of the round claim: the first
    -- transaction stamps it, the second clears it. A stamp older than the round
    -- deadline is a crashed round and may be taken over.
    merging_since      timestamptz,
    last_round_at      timestamptz,
    last_error         text,
    fail_count         integer NOT NULL DEFAULT 0,
    rebuild_requested  boolean NOT NULL DEFAULT false,
    -- adopted_preexisting records that the aggregate bank id already held user
    -- content before the rollup first wrote it. Nothing is migrated: the facts
    -- were not written by a round, so the first one replaces them, and an
    -- operator reading this flag knows why they went.
    adopted_preexisting boolean NOT NULL DEFAULT false,
    created_at         timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (tenant_id) REFERENCES tenant(tenant_id) ON DELETE CASCADE
);

-- Where one source bank's contribution stands: the (updated_at, id) watermark
-- the next round reads after.
--
-- The aggregate bank itself is never a source (it is written by the round and
-- read as the previous version), so no row here names it.
CREATE TABLE IF NOT EXISTS personal_bank_state (
    tenant_id         text NOT NULL,
    source_bank_id    text NOT NULL,
    cursor_updated_at timestamptz NOT NULL DEFAULT '-infinity',
    cursor_unit_id    uuid NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000',
    last_merged_at    timestamptz,
    PRIMARY KEY (tenant_id, source_bank_id),
    FOREIGN KEY (tenant_id) REFERENCES tenant(tenant_id) ON DELETE CASCADE
);

-- The provenance ledger: which source fact an aggregate fact was made from, or
-- '__dropped__' when the round folded it away. A source that supports nothing
-- cannot invalidate the aggregate by vanishing, so delete detection skips those
-- rows.
CREATE TABLE IF NOT EXISTS personal_source (
    tenant_id      text NOT NULL,
    source_unit_id uuid NOT NULL,
    -- The merge_key of the aggregate fact this source supports, or
    -- tenant.DroppedMergeKey.
    merge_key      text NOT NULL,
    -- bank_id is the bank the source fact lives in. The source unit id alone
    -- already implies it, but the directory groups by bank and should not have
    -- to resolve each unit back to the bank it came from.
    bank_id        text NOT NULL,
    consumed_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, source_unit_id),
    FOREIGN KEY (tenant_id) REFERENCES tenant(tenant_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS personal_source_live_idx
    ON personal_source (tenant_id) WHERE merge_key <> '__dropped__';
CREATE INDEX IF NOT EXISTS personal_source_key_idx
    ON personal_source (tenant_id, merge_key);

-- The navigation index: an edge from the aggregate to a bank, derived from
-- personal_source. weight orders the directory.
CREATE TABLE IF NOT EXISTS personal_link (
    tenant_id  text NOT NULL,
    to_bank_id text NOT NULL,
    weight     integer NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, to_bank_id),
    FOREIGN KEY (tenant_id) REFERENCES tenant(tenant_id) ON DELETE CASCADE
);

-- The shared vocabulary: one normalized topic and the bank holding it. It is
-- both how two banks talking about the same thing are grouped together and the
-- edge a read follows to the other places that thing is known.
CREATE TABLE IF NOT EXISTS personal_topic (
    tenant_id  text NOT NULL,
    topic      text NOT NULL,
    bank_id    text NOT NULL,
    weight     integer NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, topic, bank_id),
    FOREIGN KEY (tenant_id) REFERENCES tenant(tenant_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS personal_topic_bank_idx ON personal_topic (tenant_id, bank_id);
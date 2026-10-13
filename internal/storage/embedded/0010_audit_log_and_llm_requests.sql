-- 0010_audit_log_and_llm_requests (embedded copy of migrations/0010_audit_log_and_llm_requests.sql)
-- Adds the audit trail and the model-request trace tables.
--
-- audit_log records every mutating API call with its action, transport, timing
-- and payloads, so an operator can reconstruct what changed a bank and when.
-- llm_requests records each model call with its token accounting and its
-- trace identifiers, which is what makes one operation's calls groupable.

CREATE TABLE IF NOT EXISTS audit_log (
    id         TEXT         NOT NULL,
    action     VARCHAR(128) NOT NULL,
    transport  VARCHAR(64)  NOT NULL,
    bank_id    VARCHAR(64),
    started_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now(),
    ended_at   TIMESTAMP WITH TIME ZONE,
    request    JSONB,
    response   JSONB,
    metadata   JSONB        NOT NULL DEFAULT '{}'::jsonb,
    PRIMARY KEY (id)
);

-- The listing filters by bank and orders newest first.
CREATE INDEX IF NOT EXISTS idx_audit_log_bank_started ON audit_log (bank_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_log_action_started ON audit_log (action, started_at DESC);

CREATE TABLE IF NOT EXISTS llm_requests (
    id             UUID         NOT NULL DEFAULT gen_random_uuid(),
    bank_id        TEXT,
    operation      TEXT,
    scope          TEXT,
    -- One operation invocation shares a trace identifier, and parent_span_id
    -- is that operation's span while span_id is this call, so a run's calls
    -- group together.
    trace_id       TEXT,
    span_id        TEXT,
    parent_span_id TEXT,
    provider       TEXT,
    model          TEXT,
    status         TEXT         NOT NULL,
    started_at     TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now(),
    ended_at       TIMESTAMP WITH TIME ZONE,
    duration_ms    INTEGER,
    input_tokens   INTEGER,
    output_tokens  INTEGER,
    cached_tokens  INTEGER,
    thoughts_tokens INTEGER,
    total_tokens   INTEGER,
    input          JSONB,
    output         JSONB,
    error          TEXT,
    llm_info       JSONB        NOT NULL DEFAULT '{}'::jsonb,
    metadata       JSONB        NOT NULL DEFAULT '{}'::jsonb,
    PRIMARY KEY (id)
);

CREATE INDEX IF NOT EXISTS idx_llm_requests_bank_started ON llm_requests (bank_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_llm_requests_status_started ON llm_requests (status, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_llm_requests_started ON llm_requests (started_at DESC);

-- Observation sources ties an observation to the evidence it was built from,
-- so a consolidation can be traced back to its inputs.
CREATE TABLE IF NOT EXISTS observation_sources (
    observation_id VARCHAR(64) NOT NULL,
    source_id      VARCHAR(64) NOT NULL,
    PRIMARY KEY (observation_id, source_id)
);

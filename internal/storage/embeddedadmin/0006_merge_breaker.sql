-- 0006_merge_breaker
--
-- When a project's merge was last attempted, so the circuit breaker can rest.
--
-- fail_count already says how many rounds failed in a row, but a breaker needs a
-- clock as well: an open breaker lets one round through after a rest, and
-- without a timestamp there is nothing to measure the rest against. It cannot be
-- last_merged_at, which only moves on success — a project that has never merged
-- would look like it had never been tried, and would be retried on every tick,
-- which is the loop this column exists to stop.
--
-- NULL means no counted failure has been recorded yet, which reads as "tried
-- never" and therefore as "not resting".

ALTER TABLE merge_state ADD COLUMN IF NOT EXISTS last_attempt_at timestamptz;

-- The breaker's own read is per (team, project), over every member row: the
-- project rests while *all* of its members are failing, so the read is bounded by
-- the project's member count and the existing primary key already covers it.
COMMENT ON COLUMN merge_state.last_attempt_at IS
    'When a counted merge failure was last recorded; NULL when none has been.';

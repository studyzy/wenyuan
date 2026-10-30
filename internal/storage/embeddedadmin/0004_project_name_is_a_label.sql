-- 0004_project_name_is_a_label
--
-- A project's name becomes presentation only.
--
-- The name used to be the suffix of every bank id the project owned, so it had to
-- be a slug and could not carry a colon: a bank id is split on the first "::" and a
-- name containing one would make the project's banks unresolvable. Bank ids are now
-- built from the project *id*, which is the project's only identity, so the name is
-- free to be what a person reads — Chinese included.
--
-- Dropping the constraint is what lets a name like "产品:Web" through. No ids move:
-- team_project.bank_id was computed at creation and is not recomputed here, so an
-- existing project keeps the bank it already owns.

ALTER TABLE team_project DROP CONSTRAINT IF EXISTS team_project_name_check;

-- 0001_extensions
--
-- The extensions the reference's objects depend on. The reference database had
-- them installed before its tables existed, so its own dump does not carry
-- them; a schema built from this series has to install them itself, before the
-- objects that need them.
--
-- The creations run under an advisory lock. CREATE EXTENSION IF NOT EXISTS is
-- not safe under concurrency: two sessions both pass the existence check, both
-- attempt the create, and one fails on the catalogue's unique index on the
-- extension name. A migration runs from every process that starts, so two
-- deployments coming up together would collide. The lock serializes them and is
-- released when the transaction ends.

SELECT pg_advisory_xact_lock(hashtext('wenyuan:extensions'));
CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE EXTENSION IF NOT EXISTS btree_gin;

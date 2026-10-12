-- 0002_disposition_traits (embedded copy)

ALTER TABLE banks ADD COLUMN IF NOT EXISTS disposition_skepticism INTEGER NOT NULL DEFAULT 3;
ALTER TABLE banks ADD COLUMN IF NOT EXISTS disposition_literalism  INTEGER NOT NULL DEFAULT 3;
ALTER TABLE banks ADD COLUMN IF NOT EXISTS disposition_empathy     INTEGER NOT NULL DEFAULT 3;
ALTER TABLE banks ADD COLUMN IF NOT EXISTS reflect_mission         TEXT NOT NULL DEFAULT '';

ALTER TABLE banks DROP CONSTRAINT IF EXISTS banks_disposition_skepticism_check;
ALTER TABLE banks ADD CONSTRAINT banks_disposition_skepticism_check
    CHECK (disposition_skepticism BETWEEN 1 AND 5);
ALTER TABLE banks DROP CONSTRAINT IF EXISTS banks_disposition_literalism_check;
ALTER TABLE banks ADD CONSTRAINT banks_disposition_literalism_check
    CHECK (disposition_literalism BETWEEN 1 AND 5);
ALTER TABLE banks DROP CONSTRAINT IF EXISTS banks_disposition_empathy_check;
ALTER TABLE banks ADD CONSTRAINT banks_disposition_empathy_check
    CHECK (disposition_empathy BETWEEN 1 AND 5);
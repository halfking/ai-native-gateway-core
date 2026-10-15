-- Additive compatibility repair: retain kind, its view projection and indexes.
-- Removing them would destroy the writer contract and hide newly stored bodies.
-- Roll back the binary/configuration; older writers safely use turn_delta default.
SELECT 1;

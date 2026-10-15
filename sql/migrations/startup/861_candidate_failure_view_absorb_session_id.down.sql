-- Down migration for 861: drop and recreate the view WITHOUT session_id in
-- its snapshot is not expressible via SELECT * (the tables carry the column).
-- The only faithful revert is dropping the column from both tables, which is
-- a data-losing operation nobody should perform silently. Instead this down
-- file documents that 861 is a pure view rebuild: reverting means restoring
-- the 392-era view over tables that already had session_id, which yields the
-- exact same snapshot (SELECT * absorbs all current columns). In other words:
-- 861 has no distinct previous state to go back to — the migration is its own
-- inverse-by-replay. Kept as a no-op marker so the down-file convention holds.

SELECT 1;

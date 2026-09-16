-- Quick validation queries for 715 deployment
-- Run these immediately after deployment to verify the migration

-- 1. Confirm migration was applied
SELECT version FROM schema_migrations WHERE version = '715' LIMIT 1;

-- 2. Check constraint includes 'pending'
SELECT conname, consrc 
FROM pg_constraint 
WHERE conrelid = 'route_incidents'::regclass 
  AND conname = 'route_incidents_state_check';

-- 3. Check unique index includes 'pending' in WHERE clause
SELECT indexdef 
FROM pg_indexes 
WHERE tablename = 'route_incidents' 
  AND indexname = 'uq_route_incidents_active_route';

-- 4. Current state distribution
SELECT state, COUNT(*) 
FROM route_incidents 
GROUP BY state;

-- 5. Check for any pending events with high streak (should be 0)
SELECT COUNT(*) as anomaly_count
FROM route_incidents 
WHERE state = 'pending' AND failure_streak >= 3;

-- 6. Check credential states are being persisted
SELECT 
    COUNT(*) as total_credentials,
    SUM(CASE WHEN available THEN 1 ELSE 0 END) as available_count,
    SUM(CASE WHEN recover_at IS NOT NULL THEN 1 ELSE 0 END) as with_recover_at,
    SUM(CASE WHEN recover_at > NOW() THEN 1 ELSE 0 END) as currently_cooling
FROM credential_states;

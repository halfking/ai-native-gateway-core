BEGIN;
SET LOCAL statement_timeout='5s';
CREATE TEMP TABLE node_probe_state (credential_id integer,raw_model_name text,paused boolean,last_err_code text,in_flight_until timestamptz,last_direct_ok boolean,consecutive_failures integer,last_attempt_at timestamptz,next_retry_at timestamptz);
INSERT INTO node_probe_state VALUES (1,'broken',false,'http_503',NULL,false,3,now()-interval '2h',now()-interval '1h'),(2,'queue-foreign-lease',false,'http_503',now()+interval '1m',false,3,now()-interval '2h',now()-interval '1h'),(3,'owned-lease',false,'http_503',now()+interval '1m',false,3,now()-interval '2h',now()-interval '1h');
CREATE TEMP TABLE before_observation AS SELECT *,CASE
		WHEN nps.paused = TRUE AND nps.last_err_code = 'manual_offline' THEN 'unknown'
		WHEN nps.in_flight_until IS NOT NULL AND nps.in_flight_until > NOW() THEN 'probing'
		WHEN nps.last_direct_ok IS NULL THEN 'probing'
		WHEN nps.last_direct_ok THEN 'healthy_confirmed'
		WHEN nps.consecutive_failures >= 3 THEN 'broken_confirmed'
		ELSE 'suspicious'
	END AS state FROM node_probe_state nps;
UPDATE node_probe_state SET
	last_attempt_at = now()
	WHERE credential_id = 1 AND raw_model_name = 'broken';
UPDATE node_probe_state SET
	last_attempt_at = now()
	WHERE credential_id = 2 AND raw_model_name = 'queue-foreign-lease';
UPDATE node_probe_state SET
	last_attempt_at = now(), in_flight_until = NULL
	WHERE credential_id = 3 AND raw_model_name = 'owned-lease';
CREATE TEMP TABLE after_observation AS SELECT *,CASE
		WHEN nps.paused = TRUE AND nps.last_err_code = 'manual_offline' THEN 'unknown'
		WHEN nps.in_flight_until IS NOT NULL AND nps.in_flight_until > NOW() THEN 'probing'
		WHEN nps.last_direct_ok IS NULL THEN 'probing'
		WHEN nps.last_direct_ok THEN 'healthy_confirmed'
		WHEN nps.consecutive_failures >= 3 THEN 'broken_confirmed'
		ELSE 'suspicious'
	END AS state,(nps.last_attempt_at IS NULL OR nps.last_attempt_at <= nps.next_retry_at OR nps.last_attempt_at <= now() - interval '5 minutes') AS cadence_ready FROM node_probe_state nps;
DO $$ BEGIN
IF EXISTS (SELECT 1 FROM after_observation a JOIN before_observation b USING(credential_id) WHERE a.last_direct_ok IS DISTINCT FROM b.last_direct_ok OR a.consecutive_failures<>b.consecutive_failures OR a.next_retry_at<>b.next_retry_at) THEN RAISE EXCEPTION 'observation changed authority'; END IF;
IF (SELECT state FROM after_observation WHERE credential_id=1)<>'broken_confirmed' THEN RAISE EXCEPTION 'broken node was released'; END IF;
IF (SELECT in_flight_until FROM after_observation WHERE credential_id=2) IS DISTINCT FROM (SELECT in_flight_until FROM before_observation WHERE credential_id=2) THEN RAISE EXCEPTION 'queue cleared foreign lease'; END IF;
IF (SELECT state FROM after_observation WHERE credential_id=3)<>'broken_confirmed' THEN RAISE EXCEPTION 'owned lease did not return to broken'; END IF;
IF EXISTS (SELECT 1 FROM after_observation WHERE cadence_ready) THEN RAISE EXCEPTION 'fresh observation immediately repicked'; END IF;
END $$;
SELECT raw_model_name,state,cadence_ready FROM after_observation ORDER BY credential_id;
UPDATE node_probe_state SET last_attempt_at=now()-interval '6m';
DO $$ BEGIN IF EXISTS (SELECT 1 FROM node_probe_state nps WHERE NOT (nps.last_attempt_at IS NULL OR nps.last_attempt_at <= nps.next_retry_at OR nps.last_attempt_at <= now() - interval '5 minutes')) THEN RAISE EXCEPTION 'observation never becomes due'; END IF; END $$;
ROLLBACK;

package bg

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type HealthCheckDef struct {
	CheckID  string
	Severity string
	Query    string
}

func AllHealthChecks() []HealthCheckDef {
	return []HealthCheckDef{
		{CheckID: "canonical_id_null", Severity: "critical", Query: `SELECT pm.id, pm.raw_model_name, mc.id FROM provider_models pm JOIN models_canonical mc ON mc.canonical_name = pm.raw_model_name WHERE pm.canonical_id IS NULL ORDER BY pm.id LIMIT 200`},
		{CheckID: "billing_mismatch", Severity: "critical", Query: `SELECT cmb.id, c.id || ':' || pm.raw_model_name, c.plan_type, cmb.billing_mode FROM credential_model_bindings cmb JOIN credentials c ON c.id = cmb.credential_id JOIN provider_models pm ON pm.id = cmb.provider_model_id WHERE c.plan_type IN ('token_plan','code_plan','agent_plan') AND cmb.billing_mode NOT IN ('token_plan','code_plan','agent_plan') ORDER BY cmb.id LIMIT 200`},
		{CheckID: "probe_missing", Severity: "warning", Query: `SELECT cmb.id, c.id || ':' || pm.raw_model_name, pm.raw_model_name, c.id FROM credential_model_bindings cmb JOIN provider_models pm ON pm.id = cmb.provider_model_id JOIN credentials c ON c.id = cmb.credential_id WHERE cmb.available = TRUE AND c.status = 'active' AND c.lifecycle_status = 'active' AND NOT EXISTS (SELECT 1 FROM model_probe_state mps WHERE mps.credential_id = cmb.credential_id AND mps.raw_model_name = pm.raw_model_name) ORDER BY cmb.id LIMIT 200`},
		{CheckID: "family_unknown", Severity: "warning", Query: `SELECT id, canonical_name, canonical_name, canonical_name FROM models_canonical WHERE (family = 'unknown' OR family IS NULL) AND canonical_name ~* '^(claude|gpt|o[1-4]|llama|gemini|gemma|mistral|mixtral|ministral|glm|kimi|moonshot|step|stepfun|doubao|seed|qwen|deepseek|minimax|mimo|baichuan|yi|spark|xinghuo|pangu|ernie|wenxin|hunyuan|abab|falcon|nemotron|phi|sonar|grok|command|embed|rerank|bloom|pythia)' ORDER BY id LIMIT 200`},
		{CheckID: "circuit_open", Severity: "warning", Query: `SELECT cmb.id, c.id || ':' || pm.raw_model_name, c.circuit_state, c.availability_state FROM credential_model_bindings cmb JOIN provider_models pm ON pm.id = cmb.provider_model_id JOIN credentials c ON c.id = cmb.credential_id WHERE cmb.available = TRUE AND c.circuit_state NOT IN ('closed', 'disabled') ORDER BY c.circuit_state, cmb.id LIMIT 50`},
	}
}

func RunChecks(ctx context.Context, db *pgxpool.Pool) (newCritical, newWarning int, err error) {
	checks := AllHealthChecks()
	now := time.Now()

	for _, chk := range checks {
		rows, qErr := db.Query(ctx, chk.Query)
		if qErr != nil {
			err = fmt.Errorf("query %s: %w", chk.CheckID, qErr)
			return
		}
		found := 0
		for rows.Next() {
			found++
			var entityID int64
			var entityName, detail, fixSQL string

			switch chk.CheckID {
			case "canonical_id_null":
				var pmID, mcID int64
				var rawName string
				if scanErr := rows.Scan(&pmID, &rawName, &mcID); scanErr != nil {
					rows.Close()
					continue
				}
				entityID = pmID
				entityName = rawName
				detail = fmt.Sprintf("provider_models.id=%d raw_model_name='%s' → models_canonical.id=%d (canonical_id=NULL)", pmID, rawName, mcID)
				fixSQL = fmt.Sprintf("UPDATE provider_models SET canonical_id = %d WHERE id = %d;", mcID, pmID)

			case "billing_mismatch":
				var cmbID int64
				var credModel, credPlan, cmbBilling string
				if scanErr := rows.Scan(&cmbID, &credModel, &credPlan, &cmbBilling); scanErr != nil {
					rows.Close()
					continue
				}
				entityID = cmbID
				entityName = credModel
				detail = fmt.Sprintf("cred_plan='%s' cmb_billing='%s' → v.is_routable=false", credPlan, cmbBilling)
				parts := strings.SplitN(credModel, ":", 2)
				if len(parts) == 2 {
					fixSQL = fmt.Sprintf("UPDATE credential_model_bindings cmb SET billing_mode = '%s', plan_type_origin = 'manual_fix', plan_type_updated_at = now() FROM provider_models pm WHERE pm.id = cmb.provider_model_id AND cmb.credential_id = %s AND pm.raw_model_name = '%s';", credPlan, parts[0], parts[1])
				}

			case "probe_missing":
				var cmbID, credID int64
				var modelName string
				if scanErr := rows.Scan(&cmbID, &entityName, &modelName, &credID); scanErr != nil {
					rows.Close()
					continue
				}
				entityID = cmbID
				detail = fmt.Sprintf("credential_id=%d model='%s' 无 probe_state 记录", credID, modelName)
				fixSQL = "-- 需要通过 admin API 触发 probe: POST /api/credentials/{credID}/test"

			case "family_unknown":
				var id int64
				var name string
				if scanErr := rows.Scan(&id, &name, &entityName, &detail); scanErr != nil {
					rows.Close()
					continue
				}
				entityID = id
				entityName = name
				detail = fmt.Sprintf("canonical_name='%s' family='unknown'，可由 InferFamily 自动归类", name)
				fixSQL = fmt.Sprintf("UPDATE models_canonical SET family = '<需要人工确认>' WHERE id = %d;", id)

			case "circuit_open":
				var cmbID int64
				var credModel, circuitState, availState string
				if scanErr := rows.Scan(&cmbID, &credModel, &circuitState, &availState); scanErr != nil {
					rows.Close()
					continue
				}
				entityID = cmbID
				entityName = credModel
				detail = fmt.Sprintf("circuit_state='%s' availability_state='%s'", circuitState, availState)
				fixSQL = fmt.Sprintf("-- credential 熔断器未关闭，需手动恢复或等待自动恢复\n-- 检查: SELECT circuit_state, circuit_opened_at FROM credentials WHERE id = %s;", strings.SplitN(credModel, ":", 2)[0])
			}

			rows.Close()

			_, insErr := db.Exec(ctx, `
				INSERT INTO routing_health_checks
					(check_id, severity, entity_type, entity_id, entity_name, detail, fix_sql, status, created_at, updated_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,'open',$8,$8)
				ON CONFLICT (check_id, entity_type, entity_id)
				DO UPDATE SET detail=EXCLUDED.detail, fix_sql=EXCLUDED.fix_sql, updated_at=EXCLUDED.updated_at
				WHERE routing_health_checks.status IN ('open','auto_fixed')`,
				chk.CheckID, chk.Severity, chk.CheckID, entityID, entityName, detail, fixSQL, now)
			if insErr != nil {
				err = fmt.Errorf("upsert %s:%d: %w", chk.CheckID, entityID, insErr)
				return
			}

			if chk.Severity == "critical" {
				newCritical++
			} else {
				newWarning++
			}
		}
		rows.Close()
		if rows.Err() != nil {
			err = fmt.Errorf("rows %s: %w", chk.CheckID, rows.Err())
			return
		}
	}

	// auto-fix: canonical_id_null (safe exact match)
	applied, fixErr := autoFixCanonicalID(ctx, db, now)
	if fixErr != nil {
		err = fmt.Errorf("auto_fix canonical_id: %w", fixErr)
		return
	}
	if applied > 0 {
		newCritical -= applied
	}

	return
}

func autoFixCanonicalID(ctx context.Context, db *pgxpool.Pool, now time.Time) (int, error) {
	tag, err := db.Exec(ctx, `
		UPDATE provider_models pm
		SET canonical_id = mc.id
		FROM models_canonical mc
		WHERE pm.canonical_id IS NULL
		  AND pm.raw_model_name = mc.canonical_name`)
	if err != nil {
		return 0, err
	}
	applied := int(tag.RowsAffected())
	if applied > 0 {
		db.Exec(ctx, `
			UPDATE routing_health_checks
			SET status = 'auto_fixed', auto_fixed_at = $1, auto_fix_result = 'applied', updated_at = $1
			WHERE check_id = 'canonical_id_null' AND status = 'open'`, now)
	}
	return applied, nil
}

package dbinit

import (
	"crypto/sha256"
	"embed"
	"fmt"
	"strings"
)

// These are exact copies of deployment assets absent from StartupFiles. Their
// anchors put them before the installer retires or replaces their dependencies.
//
//go:embed handoffsql/*.sql
var freshHandoffAssets embed.FS

var freshHandoffBefore = map[string][]string{
	"565_cost_usd_pricing_backfill.sql": {
		"683b_session_dim_table_prelude.sql",
		"357_session_analytics_aggregation_views.sql",
		"358_session_ownership.sql",
	},
	"541_candidate_binding_scope_revision.sql": {
		"394_request_stats_minute.sql",
	},
	"572_session_summary_large_token_ratio.sql": {
		"578_candidate_binding_scope_filter_disabled.sql",
	},
	"662_feature_distribution_stats.sql": {
		"661_session_summary_token_ratio_reassert.sql",
	},
	"669_training_human_annotations.sql": {
		"V371__supplier_errors_hot_and_stats.sql",
	},
	"682_model_offers_context_window_columns.sql": {
		"682b_model_offers_context_window_columns_prelude.sql",
	},
	"853_credential_probe_queue_automatic.sql": {
		"538_node_probe_runs_trigger_kind_unified_queue.sql",
	},
	"696_request_logs_view_system_fingerprint.sql": {
		"487b_request_logs_hot_system_fingerprint.sql",
		"487c_request_logs_hot_raw_model_name.sql",
	},
	"707_session_turns_s1a.sql": {
		"706b_session_turns_hot_prelude.sql",
	},
	"708_session_bodies_s1a.sql": {
		"707b_session_bodies_hot_prelude.sql",
	},
	"730_session_role_hierarchy.sql": {
		"727_sql_audit_slow_query_indexes.sql",
		"728_sql_audit_request_logs_credential_model_index.sql",
	},
	"734_request_logs_view_details_join.sql": {
		"513_schema_unification_and_session_turns_dual_write.sql",
		"680b_request_logs_helper_view_column_resync.sql",
	},
	"735_models_canonical_active_folded_unique.sql": {
		"729_sql_audit_session_turns_credential_ts_index.sql",
	},
	"745_report_snapshots.sql": {
		"744_sql_audit_partial_indexes.sql",
	},
	"750_usage_facts_daily_partition.sql": {
		"749_usage_facts_occurred_at_index.sql",
	},
	"760_analysis_events_inbox_ttl_indexes.sql": {
		"407_request_context_attrs.sql",
	},
	"813_supplier_errors_partitions_heap.sql": {
		"810_heap_partitions_toastless_heal.sql",
		"811_partition_bounds_shanghai_midnight_repair.sql",
		"812_model_probe_runs_partitions_heap.sql",
	},
	"821_session_turns_abandoned_marker.sql": {
		"820_audio_modality_backfill.sql",
	},
	"880_session_write_timeout_budget.sql": {
		"044_health_source_probe_now.sql",
	},
}

type freshStartupStep struct {
	name         string
	supplemental bool
}

func freshStartupPlan(core []string) []freshStartupStep {
	plan := make([]freshStartupStep, 0, len(core))
	for _, name := range core {
		for _, extra := range freshHandoffBefore[name] {
			plan = append(plan, freshStartupStep{name: extra, supplemental: true})
		}
		plan = append(plan, freshStartupStep{name: name})
	}
	return plan
}

const createFreshHandoffSQL = `
CREATE TABLE public.gateway_db_installer_handoff (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    file_count integer NOT NULL CHECK (file_count > 0)
);
CREATE TABLE public.gateway_db_installer_migration_receipts (
    file_name text PRIMARY KEY CHECK (file_name ~ '^[0-9A-Za-z_.-]+[.]sql$'),
    content_sha256 text NOT NULL CHECK (content_sha256 ~ '^[a-f0-9]{64}$'),
    execution_sha256 text NOT NULL CHECK (execution_sha256 ~ '^[a-f0-9]{64}$')
);`

type installerMigrationReceipt struct {
	name                      string
	sourceHash, executionHash [32]byte
}

func (r *Runner) applyFreshStartup(step freshStartupStep) (installerMigrationReceipt, error) {
	if !step.supplemental {
		return r.applySQL("startup/" + step.name)
	}
	content, err := freshHandoffAssets.ReadFile("handoffsql/" + step.name)
	if err != nil {
		return installerMigrationReceipt{}, err
	}
	// The deployment originals include standalone concurrent indexes and psql
	// meta commands. Use file mode without an outer transaction, as deploy does.
	if err := r.runSQL(content, false); err != nil {
		return installerMigrationReceipt{}, err
	}
	hash := sha256.Sum256(content)
	return installerMigrationReceipt{step.name, hash, hash}, nil
}

func (r *Runner) sealFreshHandoff(receipts []installerMigrationReceipt) error {
	if len(receipts) == 0 {
		return fmt.Errorf("cannot seal empty installer migration plan")
	}
	var sql strings.Builder
	sql.WriteString("INSERT INTO public.gateway_db_installer_migration_receipts (file_name, content_sha256, execution_sha256) VALUES\n")
	for index, receipt := range receipts {
		if index > 0 {
			sql.WriteString(",\n")
		}
		fmt.Fprintf(&sql, "('%s','%x','%x')", strings.ReplaceAll(receipt.name, "'", "''"), receipt.sourceHash, receipt.executionHash)
	}
	fmt.Fprintf(&sql, ";\nINSERT INTO public.gateway_db_installer_handoff (file_count) VALUES (%d);", len(receipts))
	return r.execSQL(sql.String())
}

package admin

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/pashagolub/pgxmock/v4"

	"github.com/kaixuan/llm-gateway-go/db"
)

// 2026-09-17 数据源统一回归:热力图 node_status 必须与 v_node_probe_state_compat
// 使用同一个状态派生 CASE(单一事实源 db.NodeProbeStateCaseSQL),否则会重现
// "probe-health 显示 frozen healthy、热力图显示真实失败"的两页不一致。
func TestNodeStatusStateSQL_MatchesCompatViewContract(t *testing.T) {
	if !strings.Contains(nodeStatusStateSQL, "manual_offline") {
		t.Fatal("state derivation must keep the manual_offline (手动下线) branch")
	}
	if !strings.Contains(nodeStatusStateSQL, "broken_confirmed") ||
		!strings.Contains(nodeStatusStateSQL, "healthy_confirmed") ||
		!strings.Contains(nodeStatusStateSQL, "suspicious") ||
		!strings.Contains(nodeStatusStateSQL, "probing") {
		t.Fatal("state derivation must emit the full legacy vocabulary")
	}
	if nodeStatusStateSQL != db.NodeProbeStateCaseSQL("nps") {
		t.Fatal("heatmap must use the canonical db.NodeProbeStateCaseSQL derivation")
	}
}

// collapseWS normalizes whitespace so the text-containment check compares
// semantics (branches/values) rather than indentation.
func collapseWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func TestNodeStatusStateSQL_MigrationAndBaselineInSync(t *testing.T) {
	canonical := collapseWS(db.NodeProbeStateCaseSQL("nps"))
	for _, f := range []string{
		"../sql/migrations/startup/716_unify_probe_health_views.sql",
		"../sql/objects/views/v_node_probe_state_compat.sql",
	} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		// The migration/baseline mirror the canonical CASE text; drift here
		// means the file-defined view and the boot-time rebuilt view disagree.
		if !strings.Contains(collapseWS(string(b)), canonical) {
			t.Errorf("%s is out of sync with db.NodeProbeStateCaseSQL (canonical state derivation)", f)
		}
	}
}

func TestApplyNodeStatus_EnrichesAndDefaultsUnprobed(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	creds := []HeatmapCredential{
		{
			CredentialID: 3, Label: "default", ProviderName: "商汤",
			Models: []HeatmapModel{
				{RawModelName: "minimax-m3"},
				{RawModelName: "glm-5.2"},
			},
		},
		{
			CredentialID: 8, Label: "nvidia-build-new", ProviderName: "NVIDIA NIM",
			Models: []HeatmapModel{{RawModelName: "minimaxai/minimax-m3"}},
		},
	}

	broken := false
	lastErr := "http_410"
	rows := pgxmock.NewRows([]string{
		"credential_id", "raw_model_name", "state", "routable",
		"last_direct_ok", "last_err_code", "last_attempt_at", "next_retry_at",
		"consecutive_failures", "consecutive_successes", "paused",
	}).AddRow("8", "minimaxai/minimax-m3", "broken_confirmed", false,
		&broken, &lastErr, nil, nil, 9, 0, false)

	mock.ExpectQuery(`manual_offline[\s\S]*broken_confirmed[\s\S]*FROM node_probe_state nps`).
		WithArgs([]string{"3", "3", "8"}, []string{"minimax-m3", "glm-5.2", "minimaxai/minimax-m3"}).
		WillReturnRows(rows)

	applyNodeStatus(context.Background(), mock, creds)

	ns := creds[1].Models[0].NodeStatus
	if ns == nil || ns.State != "broken_confirmed" || ns.Routable {
		t.Fatalf("broken node not enriched: %+v", ns)
	}
	if ns.LastErrCode == nil || *ns.LastErrCode != "http_410" || ns.ConsecutiveFails != 9 {
		t.Fatalf("broken node fields not mapped: %+v", ns)
	}
	for _, m := range creds[0].Models {
		if m.NodeStatus == nil || m.NodeStatus.State != "unprobed" || !m.NodeStatus.Routable {
			t.Fatalf("model without node_probe_state row must default to unprobed/routable: %s %+v", m.RawModelName, m.NodeStatus)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestApplyNodeStatus_QueryErrorIsBestEffort(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	creds := []HeatmapCredential{{
		CredentialID: 3, Models: []HeatmapModel{{RawModelName: "minimax-m3"}},
	}}
	mock.ExpectQuery(`FROM node_probe_state nps`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(context.DeadlineExceeded)

	applyNodeStatus(context.Background(), mock, creds)

	if creds[0].Models[0].NodeStatus != nil {
		t.Fatalf("enrichment failure must be best-effort (node_status stays nil), got %+v", creds[0].Models[0].NodeStatus)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestApplyNodeStatus_NoPairsSkipsQuery(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	// No credentials → no query must be issued (implicit: no ExpectQuery).
	applyNodeStatus(context.Background(), mock, nil)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

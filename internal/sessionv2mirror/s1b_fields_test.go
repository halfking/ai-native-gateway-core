package sessionv2mirror

import (
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	v2 "github.com/kaixuan/llm-gateway-go/domains/session/v2"
)

func newReq() *v2.ProcessedRequest {
	return &v2.ProcessedRequest{SessionID: "s", TenantID: "default", RequestID: "r"}
}

func mustTime(t *testing.T, iso string) *time.Time {
	t.Helper()
	tv, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		t.Fatalf("parse %q: %v", iso, err)
	}
	return &tv
}

// s1b 特征层映射契约（会话存储解耦 v3）：有源 26 列逐一拷贝、缺源 4 列
// 保持 NULL、全零 entry 不产生 Details。
func TestApplyStorageS1BFieldsMapping(t *testing.T) {
	clientModel := "gpt-x-client"
	providerID := 7
	confidence := 0.87
	dueAt := mustTime(t, "2026-09-21T10:00:00Z")
	entry := &telemetry.RequestLogEntry{
		ClientModel:       &clientModel,
		ProviderID:        &providerID,
		ConfidenceNum:     &confidence,
		QualityFlags:      []string{"empty_tool_name"},
		RequestType:       strP("main"),
		RequestClass:      strP("scheduled"),
		DueAt:             dueAt,
		Attachments:       []byte(`[{"name":"a.png"}]`),
		OutboundMsgHashes: []byte(`["h1","h2"]`),
	}

	req := newReq()
	applyStorageS1BFields(req, entry)
	if req.Details == nil {
		t.Fatal("non-zero entry must produce a Details record")
	}
	d := req.Details
	if d.ClientModel == nil || *d.ClientModel != clientModel {
		t.Fatalf("client_model mapping: %+v", d.ClientModel)
	}
	if d.ProviderID == nil || *d.ProviderID != 7 {
		t.Fatalf("provider_id mapping: %+v", d.ProviderID)
	}
	if d.ConfidenceNum == nil || *d.ConfidenceNum != 0.87 {
		t.Fatalf("confidence_num mapping: %+v", d.ConfidenceNum)
	}
	if len(d.QualityFlags) != 1 || d.QualityFlags[0] != "empty_tool_name" {
		t.Fatalf("quality_flags mapping: %v", d.QualityFlags)
	}
	if d.RequestClass == nil || *d.RequestClass != "scheduled" {
		t.Fatalf("request_class mapping: %+v", d.RequestClass)
	}
	if d.DueAt == nil || !d.DueAt.Equal(*dueAt) {
		t.Fatalf("due_at mapping: %+v", d.DueAt)
	}
	if string(d.Attachments) == "" || string(d.OutboundMsgHashes) != `["h1","h2"]` {
		t.Fatalf("raw json mapping: %q / %q", d.Attachments, d.OutboundMsgHashes)
	}
	// 缺源 4 列（entry 无字段）
	if d.VirtualIP != nil || d.VirtualMAC != nil || d.KeyAlias != nil || d.OwnerUser != nil {
		t.Fatal("source-less columns must stay nil (virtual_ip/virtual_mac/key_alias/owner_user)")
	}
}

func TestApplyStorageS1BFieldsZeroEntrySkips(t *testing.T) {
	req := newReq()
	applyStorageS1BFields(req, &telemetry.RequestLogEntry{})
	if req.Details != nil {
		t.Fatal("all-zero entry must not produce a Details record")
	}
	applyStorageS1BFields(req, nil)
	if req.Details != nil {
		t.Fatal("nil entry must be a no-op")
	}
}

func strP(s string) *string { return &s }

// TestEntryToProcessedRequest_SessionAttribution730 —— R50 F15 写入方桥钉桩：
// 730 sessions 归因三列必须经 Mirror-only 传输字段（AgentRole/ParentSessionID/
// GwTaskID）从 RequestLogEntry 抵达 ProcessedRequest，upsertSessionSnapshot
// 才有源可写（此前三列 289k 行全为默认/NULL）。修复前：req 三字段恒零值。
func TestEntryToProcessedRequest_SessionAttribution730(t *testing.T) {
	role := "worker"
	parent := "gw_parent_abc"
	task := "task-9"
	entry := &telemetry.RequestLogEntry{
		RequestID:       "r-attr",
		GwSessionID:     strPtrPtr("gw_sess_attr"),
		AgentRole:       &role,
		ParentSessionID: &parent,
		GwTaskID:        &task,
	}
	req := entryToProcessedRequest(entry, "gw_sess_attr")
	if req == nil {
		t.Fatalf("entryToProcessedRequest returned nil")
	}
	if req.AgentRole != "worker" {
		t.Errorf("AgentRole: got %q, want worker", req.AgentRole)
	}
	if req.ParentSessionID != "gw_parent_abc" {
		t.Errorf("ParentSessionID: got %q, want gw_parent_abc", req.ParentSessionID)
	}
	if req.ParentTaskID != "task-9" {
		t.Errorf("ParentTaskID: got %q, want task-9 (from GwTaskID)", req.ParentTaskID)
	}
}

func strPtrPtr(s string) *string { return &s }

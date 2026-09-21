// Package sessionv2mirror — s1b_fields.go
//
// 会话存储解耦 v3（migration 731/732，docs/storage/
// 2026-09-20-session-storage-decoupling-plan.md §3.3）：把
// telemetry.RequestLogEntry 上 710 视图 NULL 占位组的特征列补采进
// session_turn_details 特征层，随 turn+bodies 同事务落 hot。
//
// 数据源事实（2026-09-20 审计，client.go 逐字段核对）：
//
//	· 有源 26 列：client_model/provider_id/client_profile/affinity_hit/
//	  transform_rule_id/gw_task_id/api_key_prefix/api_key_owner_user/
//	  application_code/auto_profile/confidence_num/model_chosen/
//	  strategy_used/compression_reason/outbound_msg_count/
//	  outbound_token_est/outbound_msg_hashes/quality_flags/
//	  quality_fix_actions/quality_score/stream_chunk_errors/
//	  stream_chunks_sent/attachments/request_type/request_class/due_at
//	· 缺源 4 列（RequestLogEntry 无字段，保持 NULL；历史值由 731 回填
//	  从 request_logs 补齐，后续管道接线登记）：virtual_ip/virtual_mac/
//	  key_alias/owner_user
//	· 储备组（node_switch_count 等 24 列）entry 无源，列已在 731 建好，
//	  待 S3 视图迁移接线。
//
// 全部指针安全：nil 字段跳过，落库转 SQL NULL（DetailsRecord 零值语义）。
package sessionv2mirror

import (
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	v2 "github.com/kaixuan/llm-gateway-go/domains/session/v2"
)

// applyStorageS1BFields copies the v3 details-layer feature group from a
// telemetry entry onto a ProcessedRequest's Details record. Called from
// entryToProcessedRequest after the S1a groups. Returns nil only when every
// mapped field is zero — the writer then skips the details upsert entirely.
func applyStorageS1BFields(req *v2.ProcessedRequest, entry *telemetry.RequestLogEntry) {
	if req == nil || entry == nil {
		return
	}
	d := &v2.DetailsRecord{
		// 视图契约组（732 LEFT JOIN 替换 NULL 占位）
		ClientModel:       strPtrVal(entry.ClientModel),
		ClientProfile:     strPtrVal(entry.ClientProfile),
		AffinityHit:       boolPtrVal(entry.AffinityHit),
		TransformRuleID:   strPtrVal(entry.TransformRuleID),
		GwTaskID:          strPtrVal(entry.GwTaskID),
		APIKeyPrefix:      strPtrVal(entry.APIKeyPrefix),
		APIKeyOwnerUser:   strPtrVal(entry.APIKeyOwnerUser),
		ApplicationCode:   strPtrVal(entry.ApplicationCode),
		AutoProfile:       strPtrVal(entry.AutoProfile),
		ConfidenceNum:     floatPtrVal(entry.ConfidenceNum),
		ModelChosen:       strPtrVal(entry.ModelChosen),
		StrategyUsed:      strPtrVal(entry.StrategyUsed),
		CompressionReason: strPtrVal(entry.CompressionReason),
		OutboundMsgCount:  intPtrVal(entry.OutboundMsgCount),
		OutboundTokenEst:  intPtrVal(entry.OutboundTokenEst),
		OutboundMsgHashes: rawOrNil(entry.OutboundMsgHashes),
		QualityFlags:      entry.QualityFlags,
		QualityFixActions: rawOrNil(entry.QualityFixActions),
		QualityScore:      floatPtrVal(entry.QualityScore),
		StreamChunkErrors: intPtrVal(entry.StreamChunkErrors),
		StreamChunksSent:  intPtrVal(entry.StreamChunksSent),
		Attachments:       rawOrNil(entry.Attachments),
		RequestType:       strPtrVal(entry.RequestType),
		RequestClass:      strPtrVal(entry.RequestClass),
		DueAt:             timePtrVal(entry.DueAt),

		// 缺源 4 列：virtual_ip / virtual_mac / key_alias / owner_user
		// （RequestLogEntry 无字段，2026-09-20 审计登记）
	}
	if entry.ProviderID != nil {
		pid := int64(*entry.ProviderID)
		d.ProviderID = &pid
	}
	if detailsRecordHasData(d) {
		req.Details = d
	}
}

// detailsRecordHasData reports whether at least one mapped column carries a
// value — an all-zero record has nothing to upsert.
func detailsRecordHasData(d *v2.DetailsRecord) bool {
	return d.ClientModel != nil || d.ProviderID != nil || d.ClientProfile != nil ||
		d.AffinityHit != nil || d.TransformRuleID != nil || d.GwTaskID != nil ||
		d.APIKeyPrefix != nil || d.APIKeyOwnerUser != nil || d.ApplicationCode != nil ||
		d.AutoProfile != nil || d.ConfidenceNum != nil || d.ModelChosen != nil ||
		d.StrategyUsed != nil || d.CompressionReason != nil || d.OutboundMsgCount != nil ||
		d.OutboundTokenEst != nil || len(d.OutboundMsgHashes) > 0 || len(d.QualityFlags) > 0 ||
		len(d.QualityFixActions) > 0 || d.QualityScore != nil || d.StreamChunkErrors != nil ||
		d.StreamChunksSent != nil || len(d.Attachments) > 0 || d.RequestType != nil ||
		d.RequestClass != nil || d.DueAt != nil
}

func strPtrVal(p *string) *string {
	if p == nil || *p == "" {
		return nil
	}
	return p
}

func intPtrVal(p *int) *int {
	if p == nil {
		return nil
	}
	return p
}

func floatPtrVal(p *float64) *float64 {
	if p == nil {
		return nil
	}
	return p
}

func boolPtrVal(p *bool) *bool {
	if p == nil {
		return nil
	}
	return p
}

func timePtrVal(p *time.Time) *time.Time {
	if p == nil || p.IsZero() {
		return nil
	}
	return p
}

func rawOrNil(raw []byte) []byte {
	if len(raw) == 0 {
		return nil
	}
	return raw
}

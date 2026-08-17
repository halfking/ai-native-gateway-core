package nodestatecache

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// recentSuccessFunc 函数适配器（测试注入用）。
type recentSuccessFunc func(ref NodeRef, window time.Duration, now time.Time) bool

func (f recentSuccessFunc) HadSuccessWithin(ref NodeRef, window time.Duration, now time.Time) bool {
	return f(ref, window, now)
}

// UT-NS-11a：Packed 位域 ↔ (状态枚举, 错误分类, 重试, 切换而来, sticky)
// 编解码映射表钉死（R10.3 位定义逐位断言）。
func TestNodeUseRecordPackedMapping(t *testing.T) {
	// 逐位字面量钉死：state=StateProbing(3) | errKind=ErrKindRateLimit(4)<<8
	// | retry(1<<16) | switched(1<<17) | sticky(1<<18)。
	packed := PackUseRecord(StateProbing, ErrKindRateLimit, true, true, true)
	assert.EqualValues(t, 3, packed&0xff, "bit0-7 state")
	assert.EqualValues(t, 4, packed>>8&0xff, "bit8-15 error class")
	assert.EqualValues(t, 1, packed>>16&0x1, "bit16 retry")
	assert.EqualValues(t, 1, packed>>17&0x1, "bit17 switched")
	assert.EqualValues(t, 1, packed>>18&0x1, "bit18 sticky")
	assert.EqualValues(t, 0, packed>>19, "bit19-31 reserved, must be zero")
	assert.EqualValues(t, uint32(3|4<<8|1<<16|1<<17|1<<18), packed) // 0x0007_0403

	// 全组合往返：编解码互逆、无位丢失。
	states := []uint8{StateUnknown, StateAvailable, StateDegraded, StateProbing, StateOffline}
	kinds := []uint8{ErrKindNone, ErrKindNetwork, ErrKindTimeout, ErrKindAuth, ErrKindRateLimit, ErrKindOverflow, ErrKindUpstream, ErrKindClient, ErrKindCanceled, ErrKindUnknown, 42}
	for _, st := range states {
		for _, k := range kinds {
			for _, retry := range []bool{false, true} {
				for _, sw := range []bool{false, true} {
					for _, sticky := range []bool{false, true} {
						p := PackUseRecord(st, k, retry, sw, sticky)
						gotState, gotKind, gotRetry, gotSw, gotSticky := UnpackUseRecord(p)
						assert.Equal(t, st, gotState)
						assert.Equal(t, k, gotKind)
						assert.Equal(t, retry, gotRetry)
						assert.Equal(t, sw, gotSw)
						assert.Equal(t, sticky, gotSticky)
					}
				}
			}
		}
	}
}

// UT-NS-11b：NodeUseRecord ↔ journey attempt_* 字段映射表钉死。
// 映射固定（热路径紧凑结构 ↔ 全量诊断事件），改动此表即为契约变更：
//
//	NodeUseRecord.Seq       ↔ journey attempt 事件 attempt_no（1 起递增）
//	NodeUseRecord.ModelID   ↔ attempt 事件携带的标准模型维度（model id）
//	NodeUseRecord.NodeID    ↔ 节点维度（tenant+credential+raw_model 的 dense id）
//	NodeUseRecord.Packed:
//	  bit0-7  状态枚举      ↔ attempt_succeeded/attempt_failed 终态
//	  bit8-15 错误分类      ↔ ErrorKind 字符串（JourneyErrorKindName 表）
//	  bit16   是否重试      ↔ attempt_no>1 / retry_scheduled 事件存在
//	  bit17   切换而来      ↔ node_switched / model_switched 事件存在
//	  bit18   sticky        ↔ node_selected 事件 sticky 原因
//	  bit19-31 预留         ↔ 禁止复用为其它语义
func TestNodeUseRecordJourneyFieldMapping(t *testing.T) {
	// 错误分类 ↔ journey ErrorKind 词表（dispatch classifyError /
	// requestjourney contract 冻结词表）逐项钉死。
	assert.Equal(t, map[uint8]string{
		ErrKindNone:      "",
		ErrKindNetwork:   "network_error",
		ErrKindTimeout:   "deadline_exceeded",
		ErrKindAuth:      "auth_error",
		ErrKindRateLimit: "rate_limit",
		ErrKindOverflow:  "overflow",
		ErrKindUpstream:  "upstream_error",
		ErrKindClient:    "client_error",
		ErrKindCanceled:  "canceled",
		ErrKindUnknown:   "upstream_error", // 未知归并 upstream_error
		42:               "upstream_error", // 未定义值同上
	}, journeyErrorKindTable())

	// 状态枚举 ↔ attempt 终态事件映射钉死。
	assert.Equal(t, map[uint8]string{
		StateAvailable: "attempt_succeeded",
		StateDegraded:  "attempt_failed", // 软失败（仍可路由）
		StateProbing:   "attempt_failed", // 连续失败转待探测
		StateOffline:   "attempt_failed",
	}, journeyStateEventTable())

	// 重试/切换/sticky 位 ↔ journey 事件存在性映射钉死。
	rec := NodeUseRecord{Seq: 2, ModelID: 7, NodeID: 1234, Packed: PackUseRecord(StateDegraded, ErrKindRateLimit, true, true, true)}
	state, kind, retry, switched, sticky := UnpackUseRecord(rec.Packed)
	assert.Equal(t, "attempt_failed", journeyStateEventTable()[state])
	assert.Equal(t, "rate_limit", JourneyErrorKindName(kind))
	assert.True(t, retry && switched && sticky, "bits map to retry_scheduled/model_switched/sticky presence")
}

// journeyErrorKindName 的全枚举快照（钉死）。
func journeyErrorKindTable() map[uint8]string {
	out := map[uint8]string{}
	for _, k := range []uint8{ErrKindNone, ErrKindNetwork, ErrKindTimeout, ErrKindAuth, ErrKindRateLimit, ErrKindOverflow, ErrKindUpstream, ErrKindClient, ErrKindCanceled, ErrKindUnknown, 42} {
		out[k] = JourneyErrorKindName(k)
	}
	return out
}

// 状态 → journey attempt 终态事件映射（文档级契约，钉死防漂移）。
func journeyStateEventTable() map[uint8]string {
	return map[uint8]string{
		StateAvailable: "attempt_succeeded",
		StateDegraded:  "attempt_failed",
		StateProbing:   "attempt_failed",
		StateOffline:   "attempt_failed",
	}
}

// UT-NS-11c：append-only 上限 = 尝试预算 100。
func TestAppendUseRecordCap100(t *testing.T) {
	var records []NodeUseRecord
	for i := 1; i <= 150; i++ {
		records = AppendUseRecord(records, NodeUseRecord{
			Seq:     int32(i),
			ModelID: 1,
			NodeID:  int32(i),
			Packed:  PackUseRecord(StateAvailable, ErrKindNone, i > 1, false, false),
		})
	}
	assert.Len(t, records, MaxUseRecords)
	assert.EqualValues(t, 100, records[99].Seq, "first 100 kept in order")
	assert.EqualValues(t, 100, records[99].NodeID)
}

package extpoints

import (
	"fmt"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain"
)

// ★ D10（2026-11-19 审计轮）元数据指数放大钉测。
//
// 缺陷本体：buildRequest 把 envelope.Metadata **全量**拷进 req.Attributes，
// 插件把结果写进同一个 Attributes，Response 又携带整个 Attributes 回来，
// syncResponseToEnvelope 再**全量**写回（且加一层 "extpoint_" 前缀）⇒
//
//	S(k+1) = 2·S(k)
//
// 12 阶段全启用 ⇒ 元数据 ×4096 ⇒ 请求对象膨胀到不可接受。
//
// 现改为**差量回写**（只写本次真正产生/改变的键）⇒ 键数随阶段线性增长。

// TestD10EnvelopeMetadataGrowthIsLinear 钉住线性上界。
func TestD10EnvelopeMetadataGrowthIsLinear(t *testing.T) {
	hook := &ExtensionPointHook{}
	envelope := &domain.PipelineRequest{
		Metadata: map[string]any{
			"model": "gpt-4",
			"path":  "/v1/chat/completions",
		},
	}

	const stages = 12
	counts := make([]int, 0, stages)
	for stage := 0; stage < stages; stage++ {
		req := hook.buildRequest(envelope)
		// 模拟插件：本次新增一个产物（键名带插件 id ⇒ 跨阶段不撞名）。
		req.Attributes[fmt.Sprintf("plugin_p%d_out", stage)] = stage
		hook.syncResponseToEnvelope(&Response{Attributes: req.Attributes}, envelope)
		counts = append(counts, len(envelope.Metadata))
	}

	// 线性上界：原始 2 个 + 每阶段 1 个新键。
	for stage, got := range counts {
		maxAllowed := 2 + stage + 1
		if got > maxAllowed {
			t.Fatalf("stage %d: metadata 键数 = %d，超过线性上界 %d（指数放大未修复）：%v",
				stage, got, maxAllowed, counts)
		}
	}

	// 负控：12 阶段后绝不能接近 2^12。
	if last := counts[len(counts)-1]; last > 64 {
		t.Fatalf("12 阶段后 metadata 键数 = %d，指数放大嫌疑：%v", last, counts)
	}
}

// TestD10WriteBackDoesNotNestPrefix 钉住「已是 extpoint_ 前缀的键不再二次
// 加前缀」——否则会产生 extpoint_extpoint_* 这种逐层嵌套的垃圾键。
//
// ★ 场景必须是「值被改变」的情形：差量回写（值未变 ⇒ 直接跳过）已经挡掉
// 了「原样拷贝回来」这条主路径；只有当插件**改写**了一个已带前缀的键时，
// 才会走到「写回键名怎么定」这一步。这才是本用例的可判别点。
func TestD10WriteBackDoesNotNestPrefix(t *testing.T) {
	hook := &ExtensionPointHook{}
	envelope := &domain.PipelineRequest{Metadata: map[string]any{}}

	// 第一阶段：插件产出一个键。
	req := hook.buildRequest(envelope)
	req.Attributes["plugin_p_out"] = 1
	hook.syncResponseToEnvelope(&Response{Attributes: req.Attributes}, envelope)

	// 第二阶段：插件**改写**了上一阶段回写进来的键（值变了 ⇒ 差量回写会
	// 走到「写回键名怎么定」这一步）。
	req2 := hook.buildRequest(envelope)
	if _, ok := req2.Attributes[extpointMetaPrefix+"plugin_p_out"]; !ok {
		t.Fatalf("第二阶段应看到上一阶段的回写键，got %v", req2.Attributes)
	}
	req2.Attributes[extpointMetaPrefix+"plugin_p_out"] = 2
	req2.Attributes["plugin_p2_out"] = 2
	hook.syncResponseToEnvelope(&Response{Attributes: req2.Attributes}, envelope)

	for k := range envelope.Metadata {
		if strings.HasPrefix(k, extpointMetaPrefix+extpointMetaPrefix) {
			t.Fatalf("出现二次前缀键 %q（元数据：%v）", k, envelope.Metadata)
		}
	}
	// 改写后的值必须落在**同一个**键上（不是新开一个嵌套键）。
	v, ok := envelope.Metadata[extpointMetaPrefix+"plugin_p_out"]
	if !ok {
		t.Fatalf("被改写的键丢失：%v", envelope.Metadata)
	}
	if v != 2 {
		t.Fatalf("被改写的键值未更新：got %v, want 2", v)
	}
}

// TestD10MetadataKeyCap 钉住键数上限（安全网）。
func TestD10MetadataKeyCap(t *testing.T) {
	hook := &ExtensionPointHook{}
	envelope := &domain.PipelineRequest{Metadata: map[string]any{}}

	// 预置到上限。
	for i := 0; i < maxEnvelopeMetadataKeys; i++ {
		envelope.Metadata[fmt.Sprintf("k%d", i)] = i
	}

	req := hook.buildRequest(envelope)
	req.Attributes["plugin_p_new"] = 1
	hook.syncResponseToEnvelope(&Response{Attributes: req.Attributes}, envelope)

	if len(envelope.Metadata) > maxEnvelopeMetadataKeys {
		t.Fatalf("metadata 键数 %d 超过上限 %d", len(envelope.Metadata), maxEnvelopeMetadataKeys)
	}
	if _, ok := envelope.Metadata[extpointMetaPrefix+"plugin_p_new"]; ok {
		t.Fatal("达到上限后不应再写入新键")
	}
}

package executors

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/nodehealth"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// 本文件钉住 2026-11-02「第九轮（F-B）」在**执行器侧**的闸门：
// 「无可用后端」必须**保留自己的档位**，不许塌成 ErrorKindUpstream。
//
// ★ 为什么这一格是关键：4xx 与 5xx 的「无可用后端」归责层级**相反**
//   （4xx ⇒ 绑定级；5xx ⇒ 凭据级），而这个区分**只能靠上游状态码**做。
//   而 `nodeHealthErrorKind(kind)` 这个入口**只看 kind、不看状态码**
//   ⇒ 它必须在 kind 这一层就把两者的公共载体保住
//   （nodehealth.ErrorKindNoAvailableChannel），
//   由 reducer 的 isBindingScoped(observation) 再按状态码分档。
//   ★ 一旦在这里塌成 ErrorKindUpstream，状态码就再也用不上了 ⇒
//     4xx 那一支会被连坐整个凭据（3 连败 ⇒ 整凭据 cooling ⇒ 全租户 503）。
//
// ★ 判据本体在 domains/nodehealth（no_available_channel_scope_test.go）；
//   本文件钉执行器这一侧的接线。

// TestNodeHealthErrorKindNoAvailableChannel 钉「不许塌成 upstream」。
func TestNodeHealthErrorKindNoAvailableChannel(t *testing.T) {
	got := nodeHealthErrorKind(errorsx.KindNoAvailableChannel)
	if got != nodehealth.ErrorKindNoAvailableChannel {
		t.Fatalf("nodeHealthErrorKind(KindNoAvailableChannel) = %q, want %q",
			got, nodehealth.ErrorKindNoAvailableChannel)
	}
	if got == nodehealth.ErrorKindUpstream {
		t.Fatal("nodeHealthErrorKind(KindNoAvailableChannel) = ErrorKindUpstream ⇒ " +
			"4xx 的「无可用后端」会连坐该凭据上的全部模型（3 连败 ⇒ 整凭据 cooling）")
	}
	// 请求侧族与既有档位不得被挤进这一档。
	for _, k := range []errorsx.ErrorKind{
		errorsx.KindRequestRejected, errorsx.KindClientBug,
		errorsx.KindModelNotFound, errorsx.KindModelUnavailable, errorsx.KindAuth,
	} {
		if got := nodeHealthErrorKind(k); got == nodehealth.ErrorKindNoAvailableChannel {
			t.Fatalf("nodeHealthErrorKind(%q) = %q —— 既有 kind 被这一档抢走了", k, got)
		}
	}
}

// TestErrorKindFromNodeHealthNoAvailableChannel 钉**回写对称**。
//
// ★ 回写必须是 KindNoAvailableChannel：它决定 credential.Writer 写进
// credential_model_bindings 的 reason（'auto_no_available_channel'）⇒
// 若回写成 KindModelNotFound，就会被记成「模型不存在」那套**终态**语义
// （与实际不符，且会把升级臂与 model_not_found 串扰起来）。
func TestErrorKindFromNodeHealthNoAvailableChannel(t *testing.T) {
	got := errorKindFromNodeHealth(nodehealth.ErrorKindNoAvailableChannel, 400)
	if got != errorsx.KindNoAvailableChannel {
		t.Fatalf("errorKindFromNodeHealth(ErrorKindNoAvailableChannel, 400) = %q, want %q",
			got, errorsx.KindNoAvailableChannel)
	}
	if got == errorsx.KindModelNotFound {
		t.Fatal("errorKindFromNodeHealth(ErrorKindNoAvailableChannel) = KindModelNotFound ⇒ " +
			"写入凭据状态的 reason 会变成「模型不存在」，与事实不符（这一档说的是" +
			"「无可用后端」，不是「模型不存在」）")
	}
}

// TestNodeHealthKindRoundTripNoAvailableChannel 钉**往返一致**。
func TestNodeHealthKindRoundTripNoAvailableChannel(t *testing.T) {
	// ★ 往返要带**状态码**：4xx（绑定级）那一支才回写 KindNoAvailableChannel
	//   （第十一轮 · F-C 起的分档回写，见下面的 scope split 门）。
	rt := errorKindFromNodeHealth(nodeHealthErrorKind(errorsx.KindNoAvailableChannel), 400)
	if rt != errorsx.KindNoAvailableChannel {
		t.Fatalf("往返 nodeHealthErrorKind → errorKindFromNodeHealth(400) = %q, want %q",
			rt, errorsx.KindNoAvailableChannel)
	}
}

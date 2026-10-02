//go:build s4audit

package admin

// request_logs_stop_write_coverage_gate_test.go — 2026-10-02。
//
// S4 停写逐点依赖评估的**硬条件**：读点清单里的每一个文件都必须有分级，
// 且分级不得为 unclassified。跑法：
//
//	go test -tags s4audit ./admin/ -run TestRequestLogsStopWriteNothingLeftUnclassified
//
// 为什么单独放 build tag：这是「把一件事做完」的闸，不是「防止事情变坏」的守卫。
// 放进常跑测试会让 `go test ./...` 长期红，挡住所有人，却不会让缺口被填上；
// 条件跳过（读到未完成就 Skip）同样有害——它把未完成伪装成通过。
// build tag 给出第三条路：**默认不挡路，显式调用时绝不放过**。
// 进度由常跑的 TestRequestLogsStopWriteClassificationProgress 打日志暴露。
//
// ⚠️ 门红时不要用 `-skip` 或删条目消音——那正是审计 §5.5.5 记的
// 「自称补全却从没人核过它覆盖了多少」。

import (
	"sort"
	"strings"
	"testing"
)

func TestRequestLogsStopWriteNothingLeftUnclassified(t *testing.T) {
	var todo []string
	for file := range requestLogsReadInventory {
		c, ok := requestLogsStopWriteClassification[file]
		switch {
		case !ok, c.Effect == effectUnclassified:
			todo = append(todo, file)
		}
	}
	if len(todo) == 0 {
		return
	}
	sort.Strings(todo)
	t.Fatalf("S4 停写的逐点依赖评估未完成：%d/%d 个文件未评估。\n"+
		"这正是审计 §8.5 记的那件事——读点清单钉住了 104 个，却没有一个被评估过。\n"+
		"最危险的两档是 %s 与 %s：它们不会让灰度失败，只会让灰度通过之后继续给出错误答案。\n\n"+
		"待评估清单（%d 个）：\n  %s\n\n%s",
		len(todo), len(requestLogsReadInventory),
		effectSilentlyEmpty, effectSilentlyFrozen,
		len(todo), strings.Join(todo, "\n  "), classificationHowTo)
}

// TestRequestLogsControlPlaneNothingLeftUnreviewed 是**控制面轴**的覆盖率硬门，
// 与上面那道读端门正交、不可互相替代。
//
// 为什么必须单列一道门：读端那道门的五档（errors_out / silently_empty /
// silently_frozen / unaffected / validator_dual_read）描述的都是「响应长什么样」。
// 一批**活的、门控外的、输出直接授权写入**的 v1 读点，可以在读端那张表里被
// 合法地填成任意一档而全部变绿——bg/credential_recovery.go 就是这种形状：
// 它的响应是 200、结果集是空、无错误信号，读端判「silently_empty」甚至不算错，
// 但它真正丢掉的是**凭据恢复写入的授权来源**。
//
// 分母取自 requestLogsReadInventory 的非 admin 子集（请求路径 + 后台 worker），
// 逐个文件显式登记为 not_control_plane / live / dormant 才算数。
func TestRequestLogsControlPlaneNothingLeftUnreviewed(t *testing.T) {
	todo := controlPlaneUnreviewed()
	if len(todo) == 0 {
		return
	}
	var nonAdmin int
	for file := range requestLogsReadInventory {
		if !strings.HasPrefix(file, "admin/") {
			nonAdmin++
		}
	}
	t.Fatalf("S4 控制面依赖评估未完成：%d/%d 个非 admin 读点文件未判定。\n"+
		"这道门与读端覆盖率门正交：读端五档全绿**不代表**停写安全——\n"+
		"停写的门控只管写侧，「读 v1 去决定写什么」的读点永远在门控之外。\n\n"+
		"对每个文件回答一个问题：**这个读点的输出，决定了哪个写入或哪个身份？**\n"+
		"  not_control_plane     输出只流向展示/导出/对账（写进 requestLogsControlPlaneReaders）\n"+
		"  control_plane_live    输出决定写入或身份，且消费方是活的（必须写 BlastRadius）\n"+
		"  control_plane_dormant 结构上是控制面，但消费方默认关闭或无调用方\n\n"+
		"判 live 之前**必须先查消费方是否真的被调用**。本表已因此自我更正过一次：\n"+
		"credentialstate/popularity_tracker.go 结构上完全符合 live，核实后是 dormant\n"+
		"（默认关闭 + 唯一输出无生产调用方）。反过来也成立：consumer 存在不等于它在门内。\n\n"+
		"待判定清单（%d 个）：\n  %s",
		len(todo), nonAdmin, len(todo), strings.Join(todo, "\n  "))
}

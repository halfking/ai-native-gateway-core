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

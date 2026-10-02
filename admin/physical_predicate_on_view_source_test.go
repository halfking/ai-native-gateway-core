//go:build !integration

package admin

import (
	"os"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/bg"
)

// TestNoPhysicalPredicateOnViewSource 守住 815 引入的一个**新**静默洞
// （2026-10-02，审计 §9.22 决策 1）。
//
// 背景是两条各自成立、叠在一起才出事的事实：
//
//  1. 815 把 origin_stage 投影进了 request_logs_with_current_month。于是物理表
//     版谓词 probeTrafficExclusionPredicate（两臂 = quality_flags + origin_stage）
//     用在视图源上**不再 42703** —— §9.20.3 当初写「补 origin_stage 会让视图读方
//     立刻又 42703」，那是错的：origin_stage 进了契约之后两臂都解析得了。
//  2. 但能解析 ≠ 语义对。quality_flags 在 session 分臂由 733 特征层供值，
//     details 缺行时为 NULL，于是
//     `NOT COALESCE('probe' = ANY(NULL), FALSE)` = NOT FALSE = **TRUE**，
//     该臂对整个 session 分臂静默失效；剩下唯一有效的是 origin_actor 臂。
//     结果：视图读方改用物理谓词 ⇒ 不报错、200、而探测流量被当成业务用量
//     ⇒ INV-3（「扫描只探真用过的模型」）重新变成名义上的。
//
// 换句话说：修掉 500 之后，原来靠 500 挡着的错误用法失去了挡板。这道门就是
// 新挡板。TestNoPhysicalOnlyColumnsInViewSourcedSQL 挡不住它——origin_stage
// 已被移出 physicalOnlyRequestLogColumns（它确实在契约里了）。
//
// 为什么按「物理谓词常量名的引用 + 该文件是否声明视图源」判定，而不是做完整
// 的 SQL 绑定分析：bg 包内 6 处物理谓词调用点全部合法地读 request_logs_hot
// （2026-10-02 逐处手验），而这条禁令的判据是「视图源 vs 物理源」——同一文件
// 里两者可以共存（拼接式 SQL），静态单文件判定会误报。真正可靠的判据是
// 「这个文件里有没有任何地方引用了 canonical 视图」，有则该文件不得引用物理
// 谓词：宁可让 bg 的物理读方显式豁免，也不让视图读方静默拿到语义错的谓词。
func TestNoPhysicalPredicateOnViewSource(t *testing.T) {
	// 物理谓词常量的**正文**特征串（不是 Go 标识符）：跨包复制手写谓词的
	// 形态是字符串拼接，只查 bg.probeTrafficExclusionPredicate 这个标识符
	// 会漏掉「本地再抄一份」——而本地副本没有漂移守卫，正是 R50 内联漏过
	// 整套测试的同一个失效模式。
	const physicalPredicateMarker = "origin_stage, 'business') = 'business'"
	// 例外：bg 包自身。物理表版谓词的定义与全部合法调用点都在那里，6 处调用
	// 的 FROM 逐处核对均为 request_logs_hot（物理表），origin_stage 在那里是
	// 权威标记。视图源不得跨包取用本谓词。
	bgPkgRationale := "物理表版谓词的定义与全部合法调用点都在本包：model_probe / " +
		"credential_selfcheck / today_success_probe / model_tier 共 6 处，2026-10-02 " +
		"逐处核对 FROM 均为 request_logs_hot（物理表）。视图源不得跨包取用本谓词。"

	files, err := goFilesUnder("..")
	if err != nil {
		t.Fatalf("walk repo root: %v", err)
	}
	for _, f := range files {
		rel := relToRepoRoot(f)
		if rel == "bg" || strings.HasPrefix(rel, "bg/") {
			_ = bgPkgRationale // 理由随本函数注释登记；豁免按包粒度，不按行
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		code := stripGoComments(string(raw))
		if !strings.Contains(code, physicalPredicateMarker) {
			continue
		}
		// 命中物理谓词正文的文件，必须不声明 canonical 视图源。
		if !declaresCanonicalViewSource(code) {
			continue
		}
		t.Errorf("%s 同时含物理表版探测排除谓词与 canonical 视图源。\n\t%s\n"+
			"origin_stage 已由 815 投影进视图契约，所以这条谓词用在视图上**不再 42703**，"+
			"但它的 quality_flags 臂在 session 分臂恒 NULL ⇒ COALESCE 兜底成 FALSE ⇒ "+
			"探测流量被当成业务用量（INV-3 静默泄漏）。视图源必须用 "+
			"bg.ProbeTrafficExclusionPredicateView。",
			rel, excerptAround(code, strings.Index(code, physicalPredicateMarker)))
	}
}

// declaresCanonicalViewSource 判「这个文件里出现过 canonical 视图的名字」。
// 刻意只做「出现过」而不做绑定分析：绑定分析需要跨字面量运行时拼接的信息
// （buildHeatmapSQL 那个形状），静态门看不见——那道形状由
// TestCredentialHeatmapSQL_ExecutesOnRealDatabase（真库执行门）兜底。
func declaresCanonicalViewSource(code string) bool {
	return strings.Contains(code, "request_logs_with_current_month")
}

// TestViewPredicateFormatArityAndArmsShape 顺带把「视图变体谓词不许悄悄长出
// 物理臂」钉住：一旦有人给 ProbeTrafficExclusionPredicateView 加上
// origin_stage 臂，它就变成了物理谓词的同义词，而 viewSourcePhysicalOnly
// 清单已不再能区分两者 ⇒ 上面那道门失去判据。
func TestViewPredicateHasNoPhysicalArm(t *testing.T) {
	const p = bg.ProbeTrafficExclusionPredicateView
	if strings.Contains(p, "origin_stage") {
		t.Errorf("视图变体谓词出现了 origin_stage 臂：它与物理谓词的区别只剩 quality_flags，" +
			"而该列在 session 分臂可空 ⇒ 两者不再是可区分的两种形状，静态禁令失去判据")
	}
	if !strings.Contains(p, "origin_actor") {
		t.Errorf("视图变体谓词丢了 origin_actor 臂：session 分臂的探测行靠它识别（真库实测 " +
			"node-probe-worker 756,040 / probe-service 195,343 行）")
	}
}

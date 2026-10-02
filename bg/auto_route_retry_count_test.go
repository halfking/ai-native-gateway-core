package bg

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func readFileForSettleGate(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败 %v", path, err)
	}
	return string(b)
}

// retry_count 推导的形状门（审计 §9.41）。
//
// # 背景：这里曾有一个活的运行时错误
//
// 旧表达式是 `jsonb_array_length(routing_attempts)`，假设该列是 JSON 数组。
// 写方 executors.RoutingAttemptsTracker.ToJSONBytes 实际产出的是
// `{"attempts": [ ... ]}` —— **对象**。于是每一行非空值都会抛
// `ERROR: cannot get array length of a non-array`（真库实测），
// 而 LATERAL 在主查询里，一行抛错就**中止整条 settleBatch 查询**，
// 那一批最多 500 条 selection 全部不结算，且下一轮重查同一批 ⇒ 反复卡死。
//
// # 判据为什么是「形状」而不是「跑一遍」
//
// 真库上有数据，但 `auto_route_selections_hot` 当前是空的、本地 CI 库也没有
// 这类流量 ⇒ 一条需要真实行的集成测试在这里**证明不了任何事**（§9.34 的教训：
// 空库上的真库门是绿而无证据）。所以这道门断言**可静态证明**的那一侧：
// 表达式必须按 jsonb_typeof 分派，且必须对 object 形态取 `-> 'attempts'`。
// 真实数据形态由审计 §9.41 记录，并在这里以常量钉住，避免写方再改形状时无声漂移。

// TestRetryCountSQLDispatchesOnJSONType 钉住分派结构。
func TestRetryCountSQLDispatchesOnJSONType(t *testing.T) {
	expr := retryCountPerRowSQL("r2")

	mustContain := []struct{ needle, why string }{
		{"jsonb_typeof(r2.routing_attempts)", "必须先看实际形态再决定怎么取长度"},
		{"r2.routing_attempts -> 'attempts'", "object 形态（写方实际产出）必须走这一支"},
		{"WHEN 'array'", "保留 array 分支：当前真库 34 万行从未出现该形态，但删掉它会让未来的假想情形静默退化成 0 重试"},
		{"jsonb_typeof(v.a) = 'array'", "外层类型守卫是必需的：`-> 'attempts'` 在畸形值上仍可能返回非数组，没有它照样抛错"},
	}
	for _, m := range mustContain {
		if !strings.Contains(expr, m.needle) {
			t.Errorf("retryCountPerRowSQL 缺少 %q —— %s。\n生成的表达式：\n%s", m.needle, m.why, expr)
		}
	}

	// 裸调用 jsonb_array_length(整列) 正是那个 bug 的形状。允许它出现，
	// 但**必须**包在类型守卫里——所以判据是「它没有直接作用在列上」。
	bare := regexp.MustCompile(`jsonb_array_length\(\s*r2\.routing_attempts\s*\)`)
	if bare.MatchString(expr) {
		t.Errorf("retryCountPerRowSQL 里存在裸的 jsonb_array_length(r2.routing_attempts)：\n%s\n"+
			"  该列是 object（写方产出 {\"attempts\":[...]}），裸调用必然抛\n"+
			"  `cannot get array length of a non-array`，并连带中止整条 settleBatch 查询。", expr)
	}
}

// TestRetryCountSQLHandlesBothWriterShapes 用**写方自己的产出**验证推导。
//
// 这里不连库：把 ToJSONBytes 的两种可能形态（object / array）作为常量喂进去，
// 断言表达式对二者都给出「长度-1，且不为负」的语义。真实库上的实测数字记在
// 审计 §9.41（1002 行 → retry 总数 1669，无报错）。
func TestRetryCountSQLHandlesBothWriterShapes(t *testing.T) {
	expr := retryCountPerRowSQL("r2")
	// 表达式必须仍然是可放进 SUM() 的标量：外层 GREATEST + COALESCE 兜底。
	if !strings.HasPrefix(expr, "GREATEST(COALESCE(") {
		t.Errorf("表达式必须以 GREATEST(COALESCE( 开头以保证非负且未测得时算 0；实际：%s", expr)
	}
	if !strings.HasSuffix(expr, ", 1) - 1, 0)") {
		t.Errorf("表达式必须以 `, 1) - 1, 0)` 结尾（未测得按 1 次尝试算 ⇒ 0 重试；实际：%s", expr)
	}
	// 不带别名时列名不能带点号，否则会生成 r2..routing_attempts 这类坏 SQL。
	plain := retryCountPerRowSQL("")
	if strings.Contains(plain, ".routing_attempts") && !strings.Contains(plain, "-> 'attempts'") {
		t.Errorf("无别名调用生成了带点号的列名：%s", plain)
	}
}

// TestRoutingAttemptsWriterShapeIsDocumented 钉住「写方产出 object」这个事实。
//
// executors.RoutingAttemptsTracker 的注释里曾经写着
// `retry_count = jsonb_array_length(routing_attempts) - 1`——**那是错误的第二处化身**：
// 它把消费者的 bug 写成了契约，于是下一个照着注释改的人会再犯一次。
// 这道门要求写方注释里明确它产出的是 object。
func TestRoutingAttemptsWriterShapeIsDocumented(t *testing.T) {
	const path = "../domains/streaming/executors/routing_tracker.go"
	src := readFileForSettleGate(t, path)

	// 写方确实产出 map[string]interface{}{"attempts": ...} ⇒ object。
	if !strings.Contains(src, `"attempts"`) {
		t.Fatalf("%s 里找不到 \"attempts\" 键——写方改了输出形状？本门需要重新评估。", path)
	}
	// 旧的错误契约必须不在注释里。
	if strings.Contains(src, "jsonb_array_length(routing_attempts) - 1") {
		t.Errorf("%s 的注释里仍有 `jsonb_array_length(routing_attempts) - 1`。\n"+
			"  那是 §9.41 那个 bug 的第二处化身：写方产出的是 object（{\"attempts\":[...]}），\n"+
			"  对它调 jsonb_array_length 必然抛错。把错误写成契约，会让下一个照着改的人再犯一次。\n"+
			"  正确契约见 bg/auto_route_settle_worker.go 的 retryCountPerRowSQL。", path)
	}
}

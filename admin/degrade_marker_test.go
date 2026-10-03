package admin

// degrade_marker_test.go —— 回归门：降级载荷必须自报家门。
//
// ## 挡住的是什么
//
// IsSchemaBehindError ��「schema 落后于代码」的查询失败降级成 200 + 空载荷。
// 这本身是合理的（可选分析视图没迁移时不该整页 500）。问题在于载荷里
// **没有任何东西说明这是降级**，于是：
//
//	period-compare    → 200 + 全 0，而 2026-09 实际有 1139.62 美元
//	cache-economics   → 200 + 全 0，而 UsageCost.vue 一次渲染 6 个指标
//	userProfile.list  → 200 + 空列表
//
// 这三种在页面上都和「真的没有数据」一模一样。2026-10-03 的实测就是
// period-compare 把 1139.62 美元显示成 0 —— 用户看到的是「本月没花钱」。
//
// 修法不是删掉降级（那会让未迁移的部署整页 500），而是让载荷自报家门：
// 恒发 degraded，false = 服务端确认过是好的，true = 这些数字别当结论。
//
// ## 判据的鉴别力
//
// 这条门**动态扫描** admin/ 下所有 IsSchemaBehindError 调用点，不硬编码文件
// 列表 —— 新增一个降级点若忘了带标记，同样报红。反向对照：把一个没带标记的
// 合成站点喂给检测器，必须被报出来，否则这条门只是恒绿装饰。

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// degradeMarkerWindow 是 IsSchemaBehindError 之后允许标记出现的行数上限。
// 取 25 是因为三个降级站点都是 `if 判定 { 记日志 → 构造载荷 → 写回 }` 的形状。
// 窗口太大会让「碰巧在别处写了标记」也算通过，太小会误报。
const degradeMarkerWindow = 25

// markerPatterns 是「这个载荷带降级标记」的判定。
// 两种形状都要认：结构体字面量（Degraded: true）与 map 载荷（"degraded": true）。
var markerPatterns = []string{
	"Degraded:",
	"Degraded: true",
	`"degraded":`,
	`"degraded": true`,
}

// findUnmarkedDegradeSites 返回所有「紧跟 IsSchemaBehindError 却没有带标记」
// 的降级站点，形如 `file:line`。
func findUnmarkedDegradeSites(src, file string) []string {
	lines := strings.Split(src, "\n")
	var bad []string
	for i, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		// 注释不是调用点 —— 判据只管可执行代码。
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "/*") {
			continue
		}
		// 函数**声明**不是调用点。`func IsSchemaBehindError(err error) bool {`
		// 里同样含有 "IsSchemaBehindError(" —— 第一版检测器把它当降级站点报了出来，
		// 而 dashboard_degrade.go 正是本机制的定义处。误报的门比没有门更坏：
		// 它只会诱导下一个人去加豁免，而不是去理解为什么。
		if strings.HasPrefix(trimmed, "func ") {
			continue
		}
		if !strings.Contains(ln, "IsSchemaBehindError(") {
			continue
		}
		// 取后续窗口判断标记；本行也算。
		windowed := strings.Join(lines[i:minInt(i+1+degradeMarkerWindow, len(lines))], "\n")
		marked := false
		for _, pat := range markerPatterns {
			if strings.Contains(windowed, pat) {
				marked = true
				break
			}
		}
		if !marked {
			bad = append(bad, file+":"+strconv.Itoa(i+1))
		}
	}
	return bad
}

// TestDegradePayloadsCarryMarker 扫真实的 admin 包。
func TestDegradePayloadsCarryMarker(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read admin dir: %v", err)
	}

	var sites, bad []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		// 去注释：注释里出现的 IsSchemaBehindError 不是调用点。
		code := stripGoCommentsKeepLines(string(raw))
		if strings.Contains(code, "IsSchemaBehindError(") {
			sites = append(sites, name)
		}
		bad = append(bad, findUnmarkedDegradeSites(code, name)...)
	}

	// 反向对照先行：检测器对「没有标记」的合成站点必须报红。
	// 少这一条，下面「全部有标记」的结论就没有说服力。
	synthetic := `if IsSchemaBehindError(err) {
		writeJSON(w, http.StatusOK, SomeResponse{})
		return
	}`
	if len(findUnmarkedDegradeSites(synthetic, "synthetic.go")) == 0 {
		t.Fatal("检测器漏报了未标记的降级站点 —— 下面那次扫描报绿没有意义")
	}
	// 阳性对照：带标记的合法写法不得误报。
	marked := `if IsSchemaBehindError(err) {
		writeJSON(w, http.StatusOK, SomeResponse{Degraded: true, DegradedReason: reason})
		return
	}`
	if got := findUnmarkedDegradeSites(marked, "synthetic.go"); len(got) > 0 {
		t.Errorf("检测器误报了合法站点: %v", got)
	}
	// 阳性对照二：机制自身的**函数声明**不得被当成降级站点。
	// 第一版就是在这里误报的，代价是 dashboard_degrade.go 出现在违规清单里。
	declaration := `func IsSchemaBehindError(err error) bool {
	return IsMissingRelationError(err) || IsMissingColumnError(err)
}`
	if got := findUnmarkedDegradeSites(declaration, "synthetic.go"); len(got) > 0 {
		t.Errorf("检测器把函数声明误当成降级站点: %v", got)
	}

	// 零样本说明检测器没跑过，不是「全部合规」。
	if len(sites) == 0 {
		t.Fatal("admin 包里一个 IsSchemaBehindError 调用点都没有 —— " +
			"要么机制被删了，要么本门扫错了目录。先查清再谈通过。")
	}

	if len(bad) > 0 {
		t.Errorf("%d 个降级站点返回 200 + 空载荷却没有 degraded 标记，"+
			"调用方无法区分「算不出来」与「真的没有数据」:\n  %s",
			len(bad), strings.Join(bad, "\n  "))
	}
}

// TestDegradeMarkerIsAlwaysEmitted 钉住「恒发」这个决定。
//
// 反向对照：先断言 JSON tag 真的是 degraded 且没有 omitempty。
// 若有人后来加上 omitempty，false 会被整个丢掉 —— 字段缺失与 false 在
// API 语义上同形，那正好把这个标记自己废掉一半。
func TestDegradeMarkerIsAlwaysEmitted(t *testing.T) {
	for _, typ := range []reflect.Type{
		reflect.TypeOf(PeriodCompareResponse{}),
		reflect.TypeOf(CacheEconomicsResponse{}),
	} {
		f, ok := typ.FieldByName("Degraded")
		if !ok {
			t.Errorf("%s 缺 Degraded 字段：降级载荷无法自报家门", typ)
			continue
		}
		tag := f.Tag.Get("json")
		if tag != "degraded" {
			t.Errorf("%s.Degraded 的 json tag = %q, want \"degraded\"", typ, tag)
		}
		if strings.Contains(tag, "omitempty") {
			t.Errorf("%s.Degraded 带 omitempty：健康时 false 会被整个丢掉，"+
				"「字段缺失」与「false」在 API 语义上无法区分，标记就白加了", typ)
		}
	}
}

// TestUserProfileDegradeHasMarker 单独钉 map 载荷那处。
//
// userProfile.list 返回 map[string]any，没有结构体可反射，只能查字面量。
func TestUserProfileDegradeHasMarker(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(".", "user_profile.go"))
	if err != nil {
		t.Fatalf("read user_profile.go: %v", err)
	}
	code := stripGoCommentsKeepLines(string(raw))
	if !strings.Contains(code, "IsSchemaBehindError(") {
		t.Skip("user_profile.go 已不再降级 —— 本断言随之失效，请确认是有意移除")
	}
	if !strings.Contains(code, `"degraded"`) {
		t.Error("user_profile.go 的降级载荷没有 \"degraded\" 标记：" +
			"空列表与「真的没有用户」在页面上同形")
	}
}

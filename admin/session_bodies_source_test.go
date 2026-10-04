//go:build !integration

package admin

// bodies 读端灰度开关的门（审计 §9.230）。
//
// # 这道门守的是什么
//
// `admin/session_export.go` 与 `admin/session_compare.go` 的 bodies 腿改成
// 经 `sessionBodiesFromSQL()` 取源。**默认必须是 v1 那一臂**——
// 理由不是「还没写完」，是数据：生产 252 实测 2026-09-30 一天仍有 1,113 条
// turn 有 v1 正文而没有 `session_bodies` 行（审计 §9.229.2）。
// 打开会让那一天的会话导出正文变成 `{}`，而接口仍 200。
//
// ⇒ 「默认关闭」不是一句注释，是**必须被检查的事实**。
// 把默认改成 true 只需要动一个字符，而那一字符的后果是
// 「导出看起来成功、每条正文为空」——本项目反复记下的最难发现的那类失效。

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// TestSessionBodiesSourceDefaultsToV1 钉住「默认关闭」。
//
// ⚠ 为什么不能只在测试里 `settings.GetPlatformBool(key, false)` 然后断言：
// 那个 `false` 是**调用点写的 fallback**，不是配置里的默认值。
// 有人把 spec 的 `Default: true` 改掉，调用点仍然是 `false`，
// 而 `GetPlatformBool` 在 key 不存在时才用 fallback ⇒ 配置一旦存在就以 true 生效。
// ⇒ **两个默认值都要查**：spec 的 `Default` 与调用点的 fallback。
func TestSessionBodiesSourceDefaultsToV1(t *testing.T) {
	// ① 调用点 fallback：未加载任何设置时必须走 v1。
	got := sessionBodiesFromSQL()
	if !strings.HasPrefix(got, "request_logs_bodies_with_current_month") {
		t.Errorf("未加载设置时 sessionBodiesFromSQL() = %q，期望以 "+
			"request_logs_bodies_with_current_month 开头（默认必须走 v1）", got)
	}
	// 精确串住整个默认形状，含别名 `rb`。
	// ⚠ 只 HasPrefix 前缀的话，忘了带别名 `rb` 也会绿 ——
	// 而调用点的 ON 子句写的是 `rb.request_id`，少了别名就是 42703。
	if want := "request_logs_bodies_with_current_month rb"; got != want {
		t.Errorf("默认臂 = %q，期望 %q（含别名 rb：调用点的 ON 子句靠它）", got, want)
	}

	// ② spec 里的 Default 必须是 false。
	specSrc := readRepoFile(t, "settings", "spec_storage.go")
	if !strings.Contains(specSrc, sessionBodiesNativeReadSetting) {
		t.Errorf("settings/spec_storage.go 里没有登记 %q —— "+
			"未登记的 key 不会出现在配置界面，而 GetPlatformBool 会一直用调用点的 fallback，"+
			"于是「想打开开关的人找不到它」。", sessionBodiesNativeReadSetting)
	}
	// 取该 key 所在的那一段，看 Default。
	seg := specSegmentForKey(specSrc, sessionBodiesNativeReadSetting)
	if seg == "" {
		t.Errorf("在 settings/spec_storage.go 里定位不到 %q 所在的 spec 段", sessionBodiesNativeReadSetting)
	} else if !strings.Contains(seg, "Default:         false") &&
		!strings.Contains(seg, "Default:         false,") {
		if !regexp.MustCompile(`Default:\s*false`).MatchString(seg) {
			t.Errorf("%q 的 spec Default 不是 false：\n%s\n"+
				"⚠ 默认开启会让生产 2026-09-30 的会话导出正文全变 `{}`（1,113 条 turn，"+
				"审计 §9.229.2 实测），而接口仍 200。", sessionBodiesNativeReadSetting, seg)
		}
	}
	// ③ 调用点传的 fallback 必须是 false。
	//
	// ⚠ **第一版这里写的是 `GetPlatformBool(key, true)` 然后断言它为 false。**
	// 那是**反的**：`GetPlatformBool` 在 key 不存在时返回 fallback，
	// 所以在无设置的测试环境里它必然返回我传的 `true` ⇒ 门恒红。
	// 而它**也无法**区分「key 存在且为 true」与「key 不存在」——
	// 用它做这个断言在语义上就不成立。
	//
	// 真正能查的是**调用点传了什么**：`GetPlatformBool` 的第三个参数。
	// 那只能从源码文本读（没有反射接口能拿到别人写的字面量）。
	switchSrc := readRepoFile(t, "admin", "session_bodies_source.go")
	if !strings.Contains(switchSrc,
		"GetPlatformBool(sessionBodiesNativeReadSetting, false)") {
		t.Errorf("admin/session_bodies_source.go 里 GetPlatformBool 的 fallback 不是 false：\n%s\n"+
			"⚠ fallback 只在 key **不存在**时生效，但 spec 一旦登记该 key，"+
			"生效的就是配置里的值（Default）；两处都必须是 false。", switchSrc)
	}
	_ = settings.GetPlatformBool // 保持 import；下面的断言走源码文本
}

// TestSessionBodiesConsumersRouteThroughTheSwitch 钉住「两个消费点都走了开关」。
//
// 缺了这条，改开关只对**一个**文件生效，而另一个照旧直读 v1
// ⇒ 运维以为「切换已完成」，而一半的读点没动。
func TestSessionBodiesConsumersRouteThroughTheSwitch(t *testing.T) {
	for _, rel := range []string{"session_export.go", "session_compare.go"} {
		src := readRepoFile(t, "admin", rel)
		if !strings.Contains(src, "sessionBodiesFromSQL()") {
			t.Errorf("admin/%s 的 bodies 腿没有走 sessionBodiesFromSQL() —— "+
				"灰度开关对它无效，切换只做了一半。", rel)
		}
		// 直写的 v1 关系名只允许出现在开关的 fallback 里（admin/session_bodies_source.go）。
		if strings.Contains(src, "LEFT JOIN request_logs_bodies_with_current_month") {
			t.Errorf("admin/%s 里仍直接写着 `LEFT JOIN request_logs_bodies_with_current_month` —— "+
				"它会绕过开关。", rel)
		}
		// 调用点仍必须投影 v1 同名列，否则开关打开后 COALESCE(rb.request_body,…) 会 42703。
		if !strings.Contains(src, "rb.request_body") || !strings.Contains(src, "rb.response_body") {
			t.Errorf("admin/%s 的投影里没有 rb.request_body / rb.response_body —— "+
				"这两个名字是 db.SessionFamilyBodiesSourceSQL 保证的契约，"+
				"改了投影就必须同步改 helper 的 AS 列表。", rel)
		}
	}
}

// specSegmentForKey 返回 spec 文件里包含该 key 的那一段 spec 字面量。
func specSegmentForKey(src, key string) string {
	i := strings.Index(src, `"`+key+`"`)
	if i < 0 {
		return ""
	}
	// 从该 key 往前找最近的 `{`，往后找配对的 `}``（spec 是扁平的字面量，取近似即可）。
	start := strings.LastIndex(src[:i], "{")
	end := strings.Index(src[i:], "}")
	if start < 0 || end < 0 {
		return ""
	}
	return src[start : i+end]
}

// readRepoFile 按**仓库根**解析路径（parts 里不要带 "admin/" 的重复前缀，
// 但**要**带包名，如 "admin/session_export.go"）。
//
// ⚠ 第一版我把调用写成 `readRepoFile(t, ".", rel)` 与 `readRepoFile(t, "..", "settings", …)`，
// 报 `open /tmp/wt-bs/session_export.go` 与 `open /tmp/settings/spec_storage.go`
// —— 因为这个 helper 向上找的是 **go.mod 所在的仓根**，不是包目录。
// 「.」= 仓根，「..」= 仓的父目录。⇒ 量具的路径语义要先确认，再写。
func readRepoFile(t *testing.T, parts ...string) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", filepath.Dir(thisFile))
		}
		dir = parent
	}
	b, err := os.ReadFile(filepath.Join(append([]string{dir}, parts...)...))
	if err != nil {
		t.Fatalf("read %v: %v", parts, err)
	}
	return string(b)
}

//go:build !integration

package admin

// bodies 读端灰度开关在 **admin 包这一侧**的接线门（审计 §9.233）。
//
// # 这个文件为什么还在
//
// §9.233 把切换层从 `admin/session_bodies_source.go` 下沉到 `db` 包
// （`db.SessionBodiesSourceSQL()`），并**删掉了 admin 侧那个薄包装**。
// 那层包装是有害的：门与 v1 关系名字面量之间又隔了一层，而
// `indirectSourceConsumers` 按「谁调用了切换层」识别 ——
// 一个只做 `+ " rb"` 的包装让 **9 个 admin 消费点在一次全仓解析里全部消失**
// （实测 5 个 vs 应有的 14 个）。
//
// ⚠ 于是「默认必须关闭」这件事，**在 admin 包里就没有自己的函数可测了**。
// 那不等于不需要门 —— 本文件守的是**消费点这一侧**的三件事，而它们
// 每一条都在本轮被实测违反过：
//
//	① 每个消费点都必须真的调 `db.SessionBodiesSourceSQL()`（而不是别的东西）；
//	② spec 登记的 key 与 db 侧常量**必须同一个**（同名两份 = 开关读不到）；
//	③ 默认必须是 v1 那一臂（理由是数据：生产 2026-09-30 缺 1,113 条）。
//
// ① ② 的判据在 `admin/v1_bodies_reader_shapes_test.go` 与
// `admin/request_logs_stop_write_classification_test.go`；本文件守 ③
// 以及「db 那一层的默认臂确实返回 v1 视图」这个**端到端**事实。

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	dbpkg "github.com/kaixuan/llm-gateway-go/db"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// TestBodiesSwitchDefaultsToV1 钉住「默认关闭」，三处都查。
//
// ⚠ 为什么不能只在测试里 `settings.GetPlatformBool(key, false)` 然后断言：
// 那个 `false` 是**调用点写的 fallback**，不是配置里的默认值。
// 有人把 spec 的 `Default: true` 改掉，调用点的 `false` 仍然在，
// 而 `GetPlatformBool` 在 key 存在时以配置值为准 ⇒ 开关实际是开的。
// ⇒ **两个默认值都要查**：spec 的 `Default` 与调用点的 fallback。
func TestBodiesSwitchDefaultsToV1(t *testing.T) {
	// ① 调用点 fallback：db 侧传的必须是 false。
	src := readRepoFile(t, "db", "request_logs_view_schema.go")
	want := "settings.GetPlatformBool(SessionBodiesNativeReadSetting, false)"
	if !strings.Contains(src, want) {
		t.Errorf("db/request_logs_view_schema.go 里 GetPlatformBool 的调用不是 %q。\n"+
			"⚠ fallback 只在 key **不存在**时生效，但 spec 一旦登记该 key，"+
			"生效的就是配置里的值（Default）；两处都必须是 false。", want)
	}

	// ② 未加载任何设置时，端到端返回 v1 那一臂。
	got := dbpkg.SessionBodiesSourceSQL()
	if want := "request_logs_bodies_with_current_month"; got != want {
		t.Errorf("未加载设置时 db.SessionBodiesSourceSQL() = %q，期望 %q（默认必须走 v1）\n"+
			"⚠ 默认开启会让生产 2026-09-30 的会话导出正文全变 `{}`（1,113 条 turn，"+
			"审计 §9.229.2 实测），而接口仍 200。", got, want)
	}

	// ③ spec 里的 Default 必须是 false。
	specSrc := readRepoFile(t, "settings", "spec_storage.go")
	if !strings.Contains(specSrc, dbpkg.SessionBodiesNativeReadSetting) {
		t.Errorf("settings/spec_storage.go 里没有登记 %q —— "+
			"未登记的 key 不会出现在配置界面，而 GetPlatformBool 会一直用调用点的 fallback，"+
			"于是「想打开开关的人找不到它」。", dbpkg.SessionBodiesNativeReadSetting)
	}
	if seg := specSegmentForKey(specSrc, dbpkg.SessionBodiesNativeReadSetting); seg == "" {
		t.Errorf("在 settings/spec_storage.go 里定位不到 %q 所在的 spec 段",
			dbpkg.SessionBodiesNativeReadSetting)
	} else if !strings.Contains(seg, "Default:         false") {
		t.Errorf("%q 的 spec Default 不是 false：\n%s\n"+
			"⚠ 默认开启会让生产 2026-09-30 的会话导出正文全变 `{}`，而接口仍 200。",
			dbpkg.SessionBodiesNativeReadSetting, seg)
	}
	_ = settings.GetPlatformBool // 保持 import；断言走源码文本
}

// TestBodiesSwitchKeyIsSingleSourced 钉住「开关 key 只有一份」。
//
// §9.233 之前它叫 `storage.admin_session_bodies_native_read`，而现在叫
// `storage.session_bodies_native_read` —— **改名是有意的**：它已不只服务 admin。
// 若旧 key 还留在 spec 里，它会变成一个**改了没人读**的僵尸配置项：
// 运维在界面上看得见、以为它是开关，拨了却什么都不发生。
//
// ⇒ 这里同时禁掉两件事：旧 key 残留、以及 db/admin 两侧各写一份字面量。
func TestBodiesSwitchKeyIsSingleSourced(t *testing.T) {
	const retiredKey = "storage.admin_session_bodies_native_read"
	for _, rel := range [][2]string{
		{"settings", "spec_storage.go"},
		{"db", "request_logs_view_schema.go"},
	} {
		// ⚠ **先剥注释再查。** 第一版直接 `strings.Contains`，结果把
		// spec 里那句「key 由 … 改名而来」也判成残留 ⇒ 门逼人把改名的
		// 来龙去脉删掉。而**删文档是比门更坏的结果**：下一个读 spec 的人
		// 会以为这个 key 从来没存在过，旧部署上的配置项也变得无法解释。
		// ⇒ 判据只针对**活代码**。
		src := stripGoComments(readRepoFile(t, rel[0], rel[1]))
		if strings.Contains(src, retiredKey) {
			t.Errorf("%s/%s 里仍有已退役的开关 key %q。\n"+
				"  §9.233 把它改名为 %q（不再只服务 admin）。\n"+
				"  ⚠ 留着它会变成**僵尸配置项**：界面上看得见、拨了什么都不发生。",
				rel[0], rel[1], retiredKey, dbpkg.SessionBodiesNativeReadSetting)
		}
	}
	// db 侧必须用常量而不是字面量，这样 admin 侧引用同一份。
	dbSrc := stripGoComments(readRepoFile(t, "db", "request_logs_view_schema.go"))
	if !strings.Contains(dbSrc, "const SessionBodiesNativeReadSetting = \"storage.session_bodies_native_read\"") {
		t.Errorf("db 侧的 SessionBodiesNativeReadSetting 常量定义被改动过 —— " +
			"确认它仍等于 \"storage.session_bodies_native_read\"（spec 侧按同一个值核对）。")
	}
}

// specSegmentForKey 返回 spec 文件里包含该 key 的那一段 spec 字面量。
func specSegmentForKey(src, key string) string {
	i := strings.Index(src, `"`+key+`"`)
	if i < 0 {
		return ""
	}
	start := strings.LastIndex(src[:i], "{")
	end := strings.Index(src[i:], "}")
	if start < 0 || end < 0 {
		return ""
	}
	return src[start : i+end]
}

// readRepoFile 按**仓库根**解析路径（parts 里带包名，如 "admin/session_export.go"）。
//
// ⚠ 第一版我把它写成 `readRepoFile(t, ".", rel)` 与 `readRepoFile(t, "..", "settings", …)`，
// 报 `open /tmp/.../session_export.go` 与 `open /tmp/settings/spec_storage.go`
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

// ⚠ 包内已有 **三份**「剥 Go 注释」的实现（本文件写第三份时才发现）：
//
//	admin/session_view_dependency_risk_test.go      stripGoComments（正则版）
//	admin/usage_ledger_sourceless_columns_test.go   stripGoCommentsKeepLines
//	本文件曾经写的第三份                            （已删）
//
// 三份迟早分叉，而分叉的方向恰好是「有的实现吃掉字符串字面量里的 `//`」。
// ⇒ 这里直接复用既有的 `stripGoComments`，**不**再写第四份。
// 清理这三份 unify 属另一件事（不属本轮），已记入 handoff 遗留项。

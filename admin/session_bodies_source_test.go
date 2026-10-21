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
	"strings"
	"testing"

	dbpkg "github.com/kaixuan/llm-gateway-go/db"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// TestBodiesSwitchLayerIsAlwaysSessionFamily 钉住「bodies 切换层恒返回会话族」。
//
// ★ 2026-10-20（迁移 890，v1 族真删）：本门**改名**（原
// `TestBodiesSwitchDefaultsToV1`）—— 被守的东西已经不存在了。
//
//	原来：灰度开关 `storage.session_bodies_native_read` 关（默认）时返回
//	      v1 正文族视图 `request_logs_bodies_with_current_month`，开时返回
//	      会话族源；判据钉「默认必须是 v1 那一臂」。
//	现在：890 删掉了 v1 正文族**连同那张视图** ⇒ 默认臂指向一个**不存在的
//	      关系** ⇒ 走默认臂的读方整条语句 42P01（不是那条腿失败，也不是
//	      静默空集）。⇒ 灰度期结束，`db.SessionBodiesSourceSQL()` **恒**
//	      返回会话族源，开关不再参与选源。
//
// 为什么必须留下这道门（而不是删掉）：那道开关**仍然存在**、仍在
// settings spec 里登记、仍可能被运维拨动。⇒ 必须钉住「切换层不再读它、
// 且恒返回会话族」，否则有人把 v1 臂「顺手加回来」是零成本的，而代价是
// 12 个消费点整条 42P01。
//
// ⚠ 判据打在**剥注释后**的源码上：db 侧保留了大量讲 v1 臂历史的注释，
// 判据若打在注释上，改对代码后门反而更红（反向的假红）。
func TestBodiesSwitchLayerIsAlwaysSessionFamily(t *testing.T) {
	// ① db 侧不再读那个开关（切换层已退化为 no-op）。
	src := stripGoComments(readRepoFile(t, "db", "request_logs_view_schema.go"))
	if strings.Contains(src, "GetPlatformBool(SessionBodiesNativeReadSetting") {
		t.Errorf("db/request_logs_view_schema.go 里仍在读 %q —— 890 之后 v1 臂已不存在，"+
			"「关」这个选项没有意义；开关若被拨到任何取值都必须仍然返回会话族源。",
			dbpkg.SessionBodiesNativeReadSetting)
	}

	// ② 未加载任何设置时，端到端返回**会话族**那一臂（恒等）。
	got := dbpkg.SessionBodiesSourceSQL()
	if want := dbpkg.SessionFamilyBodiesSourceSQL(); got != want {
		t.Errorf("db.SessionBodiesSourceSQL() = %q，期望 %q（恒返回会话族源）\n"+
			"⚠ v1 正文族已随 890 被 DROP，指回它 = 12 个消费点整条语句 42P01。", got, want)
	}
	// 负向判据：不得再出现 v1 正文族关系名（按标识符边界）。
	if hits := retiredV1RelationsInSQL(got); len(hits) > 0 {
		t.Errorf("db.SessionBodiesSourceSQL() 的输出仍引用已退役 v1 族关系名 %v —— 读了 = 42P01", hits)
	}
	// 就地阳性对照：同一把尺子必须能认出 v1 形态（否则上面那条恒绿）。
	if hits := retiredV1RelationsInSQL("FROM request_logs_bodies_with_current_month rb"); len(hits) == 0 {
		t.Error("retiredV1RelationsInSQL 认不出 v1 正文视图 —— 负向断言已恒绿，判据坏了")
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

// ⚠ readRepoFile 原先定义在本文件（带 `//go:build !integration`）⇒ 在
// `-tags=integration` 树下它不存在，而 admin/admin_500_channel_gap_realdb_test.go
// （无 build tag）在 :281 调用它 ⇒ **集成标签树编译不过**
// （vet: undefined: readRepoFile）⇒ `sql/schema` 的集成树编译门恒红。
// ⇒ 已于 2026-10-22 把它提到无 build tag 的 admin/repo_file_reader_test.go（**定义没变，
// 只是换了个文件**）⇒ 两棵树都编译、两棵树的腿都不丢。
//
// ⚠ 包内已有 **三份**「剥 Go 注释」的实现（本文件写第三份时才发现）：
//
//	admin/session_view_dependency_risk_test.go      stripGoComments（正则版）
//	admin/usage_ledger_sourceless_columns_test.go   stripGoCommentsKeepLines
//	本文件曾经写的第三份                            （已删）
//
// 三份迟早分叉，而分叉的方向恰好是「有的实现吃掉字符串字面量里的 `//`」。
// ⇒ 这里直接复用既有的 `stripGoComments`，**不**再写第四份。
// 清理这三份 unify 属另一件事（不属本轮），已记入 handoff 遗留项。

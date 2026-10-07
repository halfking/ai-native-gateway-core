// Package admin — cross_table_request_id_representation_test.go
//
// 跨表关联 id 的**文本表示一致性**守卫（2026-10-08 建于 §4.6.66；2026-10-08 扩面于 §4.6.70）。
//
// 背景（D-RL-01 的实测闭合）
// ----------------------------
// 三张表记的是**同一个** request id，但列类型不同：
//
//	request_logs.request_id                 text NOT NULL   → 32-hex（无连字符）
//	request_state_transitions.request_id     text            → 32-hex（无连字符）
//	routing_decision_log.request_id         uuid NOT NULL   → 36 位带连字符
//
// PG 把 text 里那个 32-hex 灌进 uuid 列时会规范化；而 uuid 列 `::text` 出来是**带连字符**的
// ⇒ 同一个 id 在跨表比较时字符串**不相等**，join / 精确匹配必然落空。
// 生产实测（只读消费端）：
//
//	routing-log 的 id 原样打 /api/admin/request-journeys/{id}   15/15 → 404
//	同一批 id 去掉连字符后再打                                  15/15 → 200
//
// （`/journey` 查的是 `request_state_transitions … WHERE request_id = $2`，是 text 列等值，
//   见 domains/requestjourney —— 所以左边必须是 32-hex。）
//
// ★★ 修法已实施（§4.6.70）：四处 uuid::text 全部 `replace(…, '-', '')` 规范化。
//    本守卫现在的职责**从「登记已知缺陷」变成「防止它重新长出来」** ——
//    这比登记更难，因为**没有登记表可依**，必须靠正反两侧：
//      正向：任何**未经 replace 规范化**的 `request_id::text`（uuid 列来源）⇒ 红。
//      反向：登记在册的「text 列 → text 空操作」白名单若**消失了/变了**⇒ 红，
//            否则白名单会退化成「无条件放行」。
//
// ⚠️ 第一版守卫有三个洞，本轮补上（都是它自己暴露的）：
//   ① 正则要求 `…request_id::text AS request_id`，**无别名**的投影漏掉
//      （credential_monitor.go 的 `SELECT rdl.ts, rdl.request_id::text, …`）；
//   ② **join / 比较**形态漏掉（analytics.go 的 `probe.request_id = routing_decision_log.request_id::text`
//      —— 那一条原本恒不成立，等于把一个业务过滤静默架空，比投影错更隐蔽）；
//   ③ 只扫 `admin` 目录，**看不见 `db/`**。
//
// 离线读源码，不连库、不发网络请求。
package admin

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// uuidTextRe 匹配 uuid 列经 ::text 后的带连字符表示。
var uuidTextRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// canonicalRequestIDRe 匹配 32 位无连字符 hex（text 列的规范表示）。
var canonicalRequestIDRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

// requestIDCastRe 匹配任意 `request_id::text`（含 `x.request_id::text`）。
var requestIDCastRe = regexp.MustCompile(`\w+\.request_id::text`)

// normalizedRe 匹配「已经被 replace(…, '-', '') 规范化过」的写法。
var normalizedRe = regexp.MustCompile(`replace\(\s*[\w.]*request_id::text\s*,\s*'-'\s*,\s*''\s*\)`)

// noopTextCasts 白名单：**text 列 → text 的空操作**，不是 uuid::text，不需要规范化。
// 登记它们不是为了放行，而是为了让「白名单项消失了/变了」也能被发现。
// ⚠️ key 是 `<相对本包路径>:<行内特征>`，行内特征必须仍能在该文件里 grep 到。
var noopTextCasts = map[string]string{
	"analytics.go:request_id::text = ANY($1::text[])": "request_logs 是 text 列；上一段 request_logs_hot 分支连 ::text 都不加，两边等价。别改成 replace(...)",
}

// noopTextCastsDB 同上，但在 db 包。
var noopTextCastsDB = map[string]string{
	"db.go:request_id::text AS request_id": "routing_analytics_source 从 request_logs_hot / request_logs 取数，两张表 request_id 都是 text ⇒ 空操作",
}

func readSource(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// TestCrossTableRequestIDRepresentation 防「uuid 列的 ::text 未规范化」重新长出来。
func TestCrossTableRequestIDRepresentation(t *testing.T) {
	t.Run("schema: 三张表的列类型是已知的两种", func(t *testing.T) {
		schema, err := os.ReadFile("../sql/schema/01-schema.sql")
		if err != nil {
			t.Skipf("读不到 schema 快照（%v），跳过类型事实断言", err)
		}
		s := string(schema)
		if !strings.Contains(s, "request_id uuid NOT NULL,") {
			t.Error("schema 里已找不到 `request_id uuid NOT NULL,`：" +
				"若 routing_decision_log.request_id 已改为 text，D-RL-01 的前提消失，" +
				"请把本守卫的规范化要求与 §4.6.70 一并复核")
		}
		if !strings.Contains(s, "request_id text NOT NULL,") {
			t.Error("schema 里已找不到 `request_id text NOT NULL,`：" +
				"request_logs 侧的列类型变了，请复核跨表表示一致性")
		}
	})

	t.Run("形态自证：两种表示确实互不相等", func(t *testing.T) {
		uuidForm, textForm := "249b28ac-4bd2-a898-a44c-9365343f4045", "249b28ac4bd2a898a44c9365343f4045"
		if !uuidTextRe.MatchString(uuidForm) {
			t.Fatalf("量具自证失败：%q 不被识别为 uuid 形式", uuidForm)
		}
		if !canonicalRequestIDRe.MatchString(textForm) {
			t.Fatalf("量具自证失败：%q 不被识别为 32-hex 形式", textForm)
		}
		if strings.ReplaceAll(uuidForm, "-", "") != textForm {
			t.Fatal("量具自证失败：两种形式的规范化结果不相等")
		}
	})

	t.Run("正向：admin 包内不得有未规范化的 request_id::text", func(t *testing.T) {
		entries, err := os.ReadDir(".")
		if err != nil {
			t.Fatalf("readdir: %v", err)
		}
		hits := 0
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			body := readSource(t, name)
			for i, line := range strings.Split(body, "\n") {
				if !requestIDCastRe.MatchString(line) {
					continue
				}
				hits++
				if normalizedRe.MatchString(line) {
					continue
				}
				// 白名单：必须是「text 列 → text」的空操作，且特征仍能对上。
				if reason, ok := noopTextCasts[name+":"+strings.TrimSpace(line)]; ok {
					_ = reason
					continue
				}
				t.Errorf("%s:%d 的 `request_id::text` 未被 replace(…,'-','') 规范化：\n"+
					"    %s\n"+
					"    uuid 列的 ::text 带连字符、text 列不带 ⇒ 跨表比较恒不相等（§4.6.70）。\n"+
					"    若这一行确实来自 **text 列**（空操作、不需要规范化），"+
					"请把它登记进 noopTextCasts 并写明理由。",
					name, i+1, strings.TrimSpace(line))
			}
		}
		if hits == 0 {
			t.Fatal("admin 包里一处 `request_id::text` 都没匹配到 —— " +
				"要么全被改成了别的写法（请同步本守卫），要么正则已过期")
		}
	})

	t.Run("反向：noopTextCasts 白名单的每一项都仍存在", func(t *testing.T) {
		// 白名单项若被删掉/改写而没人更新，本守卫会静默失去「反证」能力。
		for key := range noopTextCasts {
			file, _, ok := strings.Cut(key, ":")
			if !ok {
				t.Fatalf("白名单键格式错误（应为 file:特征）：%q", key)
			}
			if !strings.Contains(readSource(t, file), strings.SplitN(key, ":", 2)[1]) {
				t.Errorf("白名单项 %q 已不存在：请删除它，或更新特征（否则它是一条恒绿断言）", key)
			}
		}
	})

	t.Run("扩散防护：db 包内的空操作白名单仍成立", func(t *testing.T) {
		body := readSource(t, filepath.Join("..", "db", "db.go"))
		for key := range noopTextCastsDB {
			if !strings.Contains(body, strings.SplitN(key, ":", 2)[1]) {
				t.Errorf("db 包白名单项 %q 已不存在：请删除或更新（否则恒绿）", key)
			}
		}
		// db 包的 routing_analytics_source 从 request_logs* 取数，两张表都是 text 列
		// ⇒ 这里的 ::text 是空操作。**只登记，不禁止** —— 真正的禁止在 admin 包那个子测试里。
		if !strings.Contains(body, "FROM request_logs_hot") {
			t.Error("db.go 里已找不到 `FROM request_logs_hot`：" +
				"routing_analytics_source 的数据来源变了，请复核 db 包的空操作判断")
		}
	})
}
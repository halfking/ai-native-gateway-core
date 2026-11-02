package admin

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

// ★★ 2026-11-02 第十七轮（F-16 / F-17）：prompt-injection 管理面的
// 「可空列加固」与「写载荷分离」两道门。
//
// ── 为什么要有这道门 ──────────────────────────────────────────────────
//
// 《12小时内修订审计-2026-11-02-0350.md》§三 登记了两条：
//
//	F16  admin/prompt_injection_handler.go:795,910
//	     `blocked boolean DEFAULT false` **可空**却扫进非指针 `bool`；
//	     探测端点当时已被加固但**这一列被漏掉**。潜伏（唯一写者总是赋值）。
//	F17  admin/prompt_injection_handler.go:299 vs :1355
//	     写载荷与响应结构体只分离了 policy、没分离 canary ⇒ 写路径仍用
//	     响应结构体解码 ⇒ 第三方/curl 客户端送 PG 风格时间戳现在 400。
//
// ★ F16 与 domains/promptinjection 的 R4（12h 审计轮 R4）是**同一族**：
//   「可空列 + 无 NOT NULL + DEFAULT」扫进 Go 的**非指针** string/int/bool
//   ⇒ **一行 NULL 就让整条读路径报错**。
//   ⇒ 同一语义在仓内出现两次就只转了一次（LESSONS §606）。
//   ★ 本轮把**管理面这一侧**补齐，并把这道门**锚在 schema 上**
//     （分母从 schema 派生 ⇒ 新增可空列 ⇒ 门红 ⇒ 必须显式裁决）。
//
// ── 三条处置律 ────────────────────────────────────────────────────────
//
// ① 可空列 ⇒ 投影里 COALESCE 成该列的 schema DEFAULT
//    （或：扫进 Go 的**指针**字段 ⇒ NULL 给 nil）。
// ② **不被投影**的可空列 ⇒ 天然安全，但必须**显式登记**
//    （否则「漏投影」与「忘了 COALESCE」在门里长得一样）。
// ③ 写载荷 ≠ 响应结构体：写路径只碰**可写**的列，
//    且**可写**的时间戳列不能因为「分离」而丢掉文本写法的兼容。

// detectionsTableSchemaPath 是检测表的**事实源**（schema）。
//
// ★ 分母锚在这里（LESSONS §559）：门里那张「可空列 → 处置」的表必须
//
//	**覆盖 schema 的每一个可空列** ⇒ schema 加了列 ⇒ 门红 ⇒
//	逼着人显式裁决（COALESCE / 指针 / 不投影），而不是让它悄悄变成 500。
const detectionsTableSchemaPath = "../sql/objects/tables/prompt_injection_detections.sql"

// detectionNullableColumnPlan：可空列 → 处置。
//
//	coalesce 非空 ⇒ 投影里必须出现这个 COALESCE 形态；
//	field + isPointer ⇒ 该列扫进这个字段，且该字段必须是**指针**；
//	两者都空 ⇒ 该列**不被投影**（天然安全，但必须显式登记）。
type detectionColumnPlan struct {
	coalesce  string
	field     string
	isPointer bool
}

var detectionNullableColumnPlan = map[string]detectionColumnPlan{
	"session_key":         {coalesce: "session_key, ''"},
	"detected_at":         {field: "DetectedAt", isPointer: true},
	"rule_name":           {coalesce: "rule_name, ''"},
	"blocked":             {coalesce: "blocked, false"},
	"evidence_text":       {coalesce: "evidence_text, ''"},
	"client_ip":           {coalesce: "client_ip, ''"},
	"user_agent":          {coalesce: "user_agent, ''"},
	"llm_confidence":      {field: "LLMConfidence", isPointer: true},
	"llm_reason":          {coalesce: "llm_reason, ''"},
	"categories":          {coalesce: "categories::text[], '{}'::text[]"},
	"canary_token_leaked": {coalesce: "canary_token_leaked, ''"},
	"approval_id":         {coalesce: "approval_id, ''"},
	"replaced_content":    {coalesce: "replaced_content, ''"},
	// ↓ 不被投影的可空列（显式登记：它们不进查询 ⇒ 不会炸）
	"rule_id":               {},
	"category":              {},
	"matched_pattern":       {},
	"input_sample":          {},
	"input_hash":            {},
	"llm_engine_id":         {},
	"similar_attack_id":     {},
	"original_content_hash": {},
}

// TestDetectionsNullableColumnsAreAccountedFor 是门 Q1 的主体：
// schema 的**每一个可空列**都必须有显式处置，且处置必须真的落地。
func TestDetectionsNullableColumnsAreAccountedFor(t *testing.T) {
	nullable := schemaNullableColumns(t)

	// ① 闭集：schema 的可空列集合 == 门里登记的集合。
	for col := range nullable {
		if _, ok := detectionNullableColumnPlan[col]; !ok {
			t.Errorf("可空列 %q 没有在门里登记 ⇒ 必须显式裁决（COALESCE / 扫进指针 / 不投影）——"+
				"否则它就是下一条「一行 NULL ⇒ 整页 500」的引信", col)
		}
	}
	for col := range detectionNullableColumnPlan {
		if !nullable[col] {
			t.Errorf("门里登记的 %q 在 schema 里**不是可空列**（或已不存在）⇒ 登记表该更新了", col)
		}
	}

	// ② 处置真的落地。
	projected := projectedBaseColumns(t, promptInjectionDetectionsSelectCols)
	for col, plan := range detectionNullableColumnPlan {
		if plan.coalesce != "" {
			want := "COALESCE(" + plan.coalesce + ")"
			if !strings.Contains(promptInjectionDetectionsSelectCols, want) {
				t.Errorf("可空列 %q 的投影里没有 %q ⇒ 一行 NULL 就让整条检测列表读路径报错", col, want)
			}
			continue
		}
		if plan.field != "" {
			// 指针列：字段必须是**指针**（否则 NULL 仍会炸）。
			typ := reflect.TypeOf(PromptInjectionDetection{})
			f, ok := typ.FieldByName(plan.field)
			if !ok {
				t.Errorf("PromptInjectionDetection 缺字段 %s", plan.field)
				continue
			}
			if f.Type.Kind() != reflect.Ptr {
				t.Errorf("可空列 %q 扫进 PromptInjectionDetection.%s，但它是**非指针**（%s）⇒ NULL 会报 "+
					"\"cannot scan NULL into *%s\"", col, plan.field, f.Type, f.Type.Kind())
			}
			continue
		}
		// 不投影的列：确认它真的不在投影里（否则登记表与投影脱节）。
		if projected[col] {
			t.Errorf("可空列 %q 在门里登记为「不投影」，但它**出现在**投影里 ⇒ 登记表该更新了", col)
		}
	}
}

// schemaNullableColumns 从 schema 文件里解析出**可空列**。
func schemaNullableColumns(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(detectionsTableSchemaPath)
	if err != nil {
		t.Fatalf("读取 schema 失败：%v", err)
	}
	out := map[string]bool{}
	inBody := false
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if !inBody {
			if strings.HasPrefix(trimmed, "CREATE TABLE") && strings.Contains(trimmed, "prompt_injection_detections") {
				inBody = true
			}
			continue
		}
		if strings.HasPrefix(trimmed, ");") {
			break
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "CONSTRAINT") {
			continue
		}
		name := strings.Fields(trimmed)[0]
		name = strings.TrimSuffix(name, ",")
		if !regexp.MustCompile(`^[a-z_][a-z0-9_]*$`).MatchString(name) {
			continue
		}
		if !strings.Contains(strings.ToUpper(trimmed), "NOT NULL") {
			out[name] = true
		}
	}
	if len(out) == 0 {
		t.Fatalf("从 %s 解析出的可空列是 0 个 ⇒ 解析口径写错了（门会恒绿）", detectionsTableSchemaPath)
	}
	return out
}

// projectedBaseColumns 把投影串拆成「基础列名 → 是否出现」。
func projectedBaseColumns(t *testing.T, proj string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, expr := range splitTopLevelColumns(proj) {
		e := strings.TrimSpace(expr)
		if strings.HasPrefix(strings.ToUpper(e), "COALESCE(") {
			inner := e[len("COALESCE("):]
			if idx := strings.Index(inner, ","); idx >= 0 {
				e = inner[:idx]
			}
		}
		if idx := strings.Index(strings.ToUpper(e), " AS "); idx >= 0 {
			e = e[:idx]
		}
		e = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(e), ","))
		fields := strings.Fields(e)
		if len(fields) == 0 {
			continue
		}
		out[fields[0]] = true
	}
	if len(out) == 0 {
		t.Fatal("投影串解析出 0 列 ⇒ 解析口径写错了")
	}
	return out
}

// splitTopLevelColumns 按**顶层**逗号切分投影串（忽略括号内的逗号）。
func splitTopLevelColumns(proj string) []string {
	var (
		out   []string
		cur   strings.Builder
		depth int
	)
	for _, r := range proj {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, cur.String())
				cur.Reset()
				continue
			}
		}
		cur.WriteRune(r)
	}
	if strings.TrimSpace(cur.String()) != "" {
		out = append(out, cur.String())
	}
	return out
}

// canaryWritePayloadFields 是 canary token 写载荷**允许**的字段集合。
//
// ★ 读投影 ≠ 写载荷：响应体里的 id / times_injected / times_leaked /
//
//	last_leaked_at / created_at 是**只读**的，写路径一个都不用；
//	其中三个是 timestamptz ⇒ JSON 解码只认 RFC3339 ⇒
//	「客户端把 GET 响应原样 POST 回来」会在**解码期**整包 400（F17）。
var canaryWritePayloadFields = map[string]bool{
	"token_value":    true,
	"token_type":     true,
	"token_name":     true,
	"description":    true,
	"leak_action":    true,
	"notify_on_leak": true,
	"active":         true,
	"expires_at":     true,
}

// TestCanaryWritePayloadIsSeparatedFromResponseStruct 是门 Q2：
// 写载荷必须只含**可写**列，且不得含只读列。
func TestCanaryWritePayloadIsSeparatedFromResponseStruct(t *testing.T) {
	typ := reflect.TypeOf(CanaryTokenUpdate{})
	got := map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		tag := strings.TrimSuffix(strings.TrimPrefix(f.Tag.Get("json"), ","), "")
		if tag == "" || tag == "-" {
			continue
		}
		got[tag] = true
	}

	for name := range got {
		if !canaryWritePayloadFields[name] {
			t.Errorf("写载荷 CanaryTokenUpdate 含字段 %q —— 它不在可写列集合里 ⇒ "+
				"要么是只读列（写路径用不着，却会让「GET 原样 POST 回来」在解码期 400），"+
				"要么是拼错了", name)
		}
	}
	for name := range canaryWritePayloadFields {
		if !got[name] {
			t.Errorf("可写列 %q 不在写载荷 CanaryTokenUpdate 里 ⇒ 客户端改不了这一列", name)
		}
	}

	// 只读列必须**不在**写载荷里（这是 F17 的核心）。
	responseOnly := []string{"id", "times_injected", "times_leaked", "last_leaked_at", "created_at", "tenant_id"}
	for _, name := range responseOnly {
		if got[name] {
			t.Errorf("写载荷 CanaryTokenUpdate 含只读字段 %q ⇒ 它是**响应**结构体的字段，不是可写列", name)
		}
	}
}

// TestCanaryWritePayloadAcceptsFlexibleExpiresAt 是门 Q3：**客户端契约**。
//
// ★ 写路径此前由 PG 侧做文本转型；写载荷与响应结构体分离之后，
//
//	不能把这份兼容弄丢 ⇒ 这道门钉住「PG 风格时间戳仍然收」。
func TestCanaryWritePayloadAcceptsFlexibleExpiresAt(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string // RFC3339
	}{
		{"RFC3339", `"2026-12-31T23:59:59Z"`, "2026-12-31T23:59:59Z"},
		{"PG 日期", `"2026-12-31"`, "2026-12-31T00:00:00Z"},
		{"PG 时间戳带偏移", `"2026-12-31 23:59:59+08"`, "2026-12-31T15:59:59Z"},
		{"PG 时间戳带微秒与偏移", `"2026-12-31 23:59:59.836952+08"`, "2026-12-31T15:59:59.836952Z"},
		{"PG 时间戳不带时区", `"2026-12-31 23:59:59"`, "2026-12-31T23:59:59Z"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var payload CanaryTokenUpdate
			body := `{"token_name":"t","expires_at":` + tc.raw + `}`
			if err := json.Unmarshal([]byte(body), &payload); err != nil {
				t.Fatalf("解码失败（客户端会拿到 400）：%v", err)
			}
			if payload.ExpiresAt == nil {
				t.Fatal("expires_at 解成了 nil ⇒ 过期时间被悄悄丢掉")
			}
			got, err := expiresAtOrNil(payload.ExpiresAt)
			if err != nil {
				t.Fatalf("解析失败：%v", err)
			}
			if got == nil {
				t.Fatal("expiresAtOrNil 返回 nil ⇒ 过期时间被悄悄丢掉")
			}
			// ★ 比的是**时刻**（.Equal），不是渲染出来的字符串：
			// PG 的 "+08" 写法解析出来是一个 +08:00 固定区的时刻，
			// 它与 UTC 同一时刻但渲染不同 ⇒ 比字符串会把对的判成错的。
			want, err := time.Parse(time.RFC3339Nano, tc.want)
			if err != nil {
				t.Fatalf("期望值本身解析失败：%v", err)
			}
			if !got.Equal(want) {
				t.Errorf("解析结果 = %s（UTC %s），want %s",
					got.Format(time.RFC3339Nano), got.UTC().Format(time.RFC3339Nano), tc.want)
			}
		})
	}
}

// TestCanaryWritePayloadRejectsGarbageExpiresAt 是门 Q3 的另一半：
// 解析不出来必须**明确报错**，不能悄悄落一个错的时刻。
func TestCanaryWritePayloadRejectsGarbageExpiresAt(t *testing.T) {
	var payload CanaryTokenUpdate
	if err := json.Unmarshal([]byte(`{"expires_at":"下周二"}`), &payload); err != nil {
		// JSON 层就拒了也算通过（两种拒法都可以，但不能默默接受）。
		return
	}
	if _, err := expiresAtOrNil(payload.ExpiresAt); err == nil {
		t.Fatal("无法解析的时间戳被默默接受了 ⇒ 库里会落一个错的时刻")
	}
}

// TestCanaryWritePayloadEmptyExpiresAtMeansNoExpiry 钉住空串的语义：
// 空串 / 缺省 ⇒ **不过期**（落库 NULL），不是错误，也不是「1970」。
func TestCanaryWritePayloadEmptyExpiresAtMeansNoExpiry(t *testing.T) {
	var payload CanaryTokenUpdate
	if err := json.Unmarshal([]byte(`{"expires_at":""}`), &payload); err != nil {
		t.Fatalf("解码失败：%v", err)
	}
	got, err := expiresAtOrNil(payload.ExpiresAt)
	if err != nil {
		t.Fatalf("空串不应是错误：%v", err)
	}
	if got != nil {
		t.Fatalf("空串 ⇒ %v, want nil（不过期）", got)
	}

	var missing CanaryTokenUpdate
	if err := json.Unmarshal([]byte(`{}`), &missing); err != nil {
		t.Fatalf("解码失败：%v", err)
	}
	got, err = expiresAtOrNil(missing.ExpiresAt)
	if err != nil {
		t.Fatalf("缺省不应是错误：%v", err)
	}
	if got != nil {
		t.Fatalf("缺省 ⇒ %v, want nil（不过期）", got)
	}
}

// TestCanaryWriteHandlersUseWritePayload 是门 Q4：**接线**门。
//
// ★ 为什么结构体的门不够：F-17 的根因是「写路径用**响应结构体**解码」，
//
//	而响应结构体本身没有任何问题（它就该有那些只读字段）⇒ 上一道门
//	（写载荷的字段集合）压根管不到「handler 到底解到了哪个结构体」。
//	⇒ 必须有一道门直接看**两个写处理器**解的是什么。
func TestCanaryWriteHandlersUseWritePayload(t *testing.T) {
	want := map[string]bool{
		"createCanaryToken": true,
		"updateCanaryToken": true,
	}
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, 0)
	if err != nil {
		t.Fatalf("解析 admin 包失败：%v", err)
	}

	seen := map[string]bool{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil || !want[fn.Name.Name] {
					continue
				}
				seen[fn.Name.Name] = true
				usedWritePayload := false
				usedResponseStruct := false
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					spec, ok := n.(*ast.ValueSpec)
					if !ok {
						return true
					}
					ident, ok := spec.Type.(*ast.Ident)
					if !ok {
						return true
					}
					switch ident.Name {
					case "CanaryTokenUpdate":
						usedWritePayload = true
					case "CanaryToken":
						usedResponseStruct = true
					}
					return true
				})
				if usedResponseStruct {
					t.Errorf("%s 解的是 **CanaryToken（响应结构体）** ⇒ F-17 复发："+
						"写路径会带着 5 个只读字段去解码，其中三个是 timestamptz ⇒ "+
						"客户端把 GET 响应原样 POST 回来时整包 400", fn.Name.Name)
				}
				if !usedWritePayload {
					t.Errorf("%s 没有用写载荷 CanaryTokenUpdate", fn.Name.Name)
				}
			}
		}
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("admin 包里找不到 %s ⇒ 门覆盖不到它（函数改名/重构了？）", name)
		}
	}
}

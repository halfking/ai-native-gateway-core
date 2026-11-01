package admin

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// prompt_injection_timestamp_scan_test.go — 2026-11-01 生产 500 的**静态**判据。
//
// ## 缺陷（生产实读，211 / llm-gateway-211-b）
//
//	ERROR admin handler internal error op="Failed to get policy"
//	  caller=prompt_injection_handler.go:241
//	  err="can't scan into dest[31] (col: created_at): cannot scan timestamptz
//	       (OID 1184) in binary format into *string"
//	ERROR admin handler internal error op="Failed to scan rule"
//	  caller=prompt_injection_handler.go:503
//	  err="can't scan into dest[14] (col: created_at): cannot scan timestamptz
//	       (OID 1184) in binary format into *string"
//
// ⇒ 网页 /admin/prompt-injection 打开即报 500「Failed to get policy」。
//
// ## 根因（不是 SQL 形状，是 pgx 的扫描计划）
//
// pgx v5 的 TimestamptzCodec.PreferredFormat() == BinaryFormatCode，而
// pgtype.Map.planScan 对 **BinaryFormatCode** 的 `*string` 目标**只放行
// TextOID / VarcharOID** ⇒ 把 timestamptz 扫进 Go string 必然报
// "cannot scan timestamptz (OID 1184) in binary format into *string"。
//
// ⇒ ⇒ **凡是把 timestamptz 列投影成 Go string 的读路径都会 500**，
// 与「列存不存在」无关；只要**有行且该列非 NULL** 就炸。
// 空表时命中 ErrNoRows 分支（返回默认对象）⇒ 看起来是好的 —— 这正是
// canary-tokens / engines / attack-vectors 这几条读路径长期没被发现的原因
// （生产库里它们都是 0 行）。
//
// ## 为什么本文件**只**有静态门
//
// 真库腿在 admin/prompt_injection_timestamp_scan_realdb_test.go（隔离 schema，
// 建后即删，从不写 public）。但真库腿**有一个结构性盲区**：
// 「把字段类型从 *time.Time 改回 string」这个动作在**空库上不改变任何
// 可见行为**（ErrNoRows 分支照样返回 200）⇒ ⇒ CI 会绿。
// ⇒ 必须有一道**不依赖数据**的门，把「时间戳字段必须是 *time.Time」
// 与「时间戳列不许 ::text 变通」钉死。
//
// 配套的写路径判据见 TestPromptInjectionPolicyWritePayloadHasNoTimestampFields。

// TestPromptInjectionTimestampFieldsAreTimePointers 静态门：响应结构体的
// 时间戳字段**必须**是 *time.Time。
//
// ★ 为什么必须是 *time.Time 而不是 string：
//
//	· string ⇒ pgx 二进制格式拒绝 ⇒ 端点 500（生产实证）；
//	· 非指针 time.Time ⇒ 列可空时 NULL 会报 "converting NULL to time.Time
//	  is unsupported" ⇒ 仍是 500；
//	· *time.Time ⇒ pgx 的 TryPointerPointerScanPlan 拆成 *time.Time 交给
//	  TimestamptzCodec ⇒ NULL 给 nil、非 NULL 给值 ⇒ 永不报错，
//	  且 JSON 侧仍是字符串（RFC3339Nano）⇒ 线上契约不变。
func TestPromptInjectionTimestampFieldsAreTimePointers(t *testing.T) {
	want := reflect.TypeOf((*time.Time)(nil))

	cases := []struct {
		structName string
		value      any
		fields     []string
	}{
		{"PromptInjectionPolicy", PromptInjectionPolicy{},
			[]string{"LastDetectionAt", "CreatedAt", "UpdatedAt"}},
		{"PromptInjectionRule", PromptInjectionRule{},
			[]string{"CreatedAt", "UpdatedAt"}},
		{"LLMEngine", LLMEngine{},
			[]string{"LastCalledAt", "CreatedAt", "UpdatedAt"}},
		{"CanaryToken", CanaryToken{},
			[]string{"ExpiresAt", "LastLeakedAt", "CreatedAt"}},
		{"AttackVector", AttackVector{},
			[]string{"DetectedAt", "CreatedAt"}},
	}

	for _, c := range cases {
		typ := reflect.TypeOf(c.value)
		for _, name := range c.fields {
			f, ok := typ.FieldByName(name)
			if !ok {
				t.Errorf("%s: 缺字段 %s", c.structName, name)
				continue
			}
			if f.Type != want {
				t.Errorf("%s.%s 的类型是 %s，必须是 *time.Time —— "+
					"pgx 以**二进制**格式回 timestamptz，扫进 string 会报 "+
					"\"cannot scan timestamptz (OID 1184) in binary format into *string\" ⇒ 端点 500",
					c.structName, name, f.Type)
			}
		}
	}
}

// TestPromptInjectionTimestampJSONIsRFC3339 静态门：**线上契约**不许漂移。
//
// ★ 前端声明（web/src/api/promptInjection.ts）：
//
//	created_at: string
//	last_detection_at: string | null
//
// ⇒ JSON 里必须是**字符串**（不是数字、不是对象），且**可被 new Date() 解析**
// ⇒ 即 RFC3339。PG 的 text 输出（"2026-07-19 21:08:25.836952+08"，空格分隔、
// 没有 'T'）**不是** RFC3339 ⇒ Safari/iOS 的 Date.parse 返回 NaN ⇒
// 移动端的 relativeTime 会退化成显示原始文本。
func TestPromptInjectionTimestampJSONIsRFC3339(t *testing.T) {
	ts := time.Date(2026, 7, 19, 21, 8, 25, 836952000, time.FixedZone("CST", 8*3600))

	policy := PromptInjectionPolicy{CreatedAt: &ts, UpdatedAt: &ts}
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatalf("marshal policy: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal policy: %v", err)
	}
	got, ok := decoded["created_at"].(string)
	if !ok {
		t.Fatalf("created_at 在 JSON 里必须是**字符串**（前端声明 created_at: string），实际 %T：%s",
			decoded["created_at"], raw)
	}
	parsed, err := time.Parse(time.RFC3339Nano, got)
	if err != nil {
		t.Fatalf("created_at 必须是 RFC3339（前端 new Date() 可解析），实际 %q：%v", got, err)
	}
	if !parsed.Equal(ts) {
		t.Fatalf("时间戳往返失真：want %s got %s", ts, parsed)
	}

	// 可空列：nil 指针 ⇒ JSON null（不编造 1970，也不给空串）。
	empty := PromptInjectionPolicy{}
	rawEmpty, err := json.Marshal(empty)
	if err != nil {
		t.Fatalf("marshal empty policy: %v", err)
	}
	var decodedEmpty map[string]any
	if err := json.Unmarshal(rawEmpty, &decodedEmpty); err != nil {
		t.Fatalf("unmarshal empty policy: %v", err)
	}
	if v, present := decodedEmpty["last_detection_at"]; !present || v != nil {
		t.Fatalf("last_detection_at 必须如实给 null（键存在、值为 null），实际 %#v", v)
	}
}

// TestPromptInjectionPolicyWritePayloadHasNoTimestampFields 静态门：
// **写载荷**里不许有时间戳字段。
//
// ★ 理由（写路径的解码脆弱性）：
//
//	写载荷曾复用响应结构体 ⇒ 响应体里的 created_at / updated_at /
//	last_detection_at 会被 JSON 解码 ⇒ `time.Time.UnmarshalJSON` **只认
//	RFC3339** ⇒ 客户端把 GET 响应原样 PUT 回来时，只要时间戳不是
//	RFC3339（比如 PG 的文本格式 "2026-07-19 21:08:25.836952+08"），
//	整个 PUT 就在**解码期**整包 400。
//	⇒ 写载荷与响应结构体**分离**（PromptInjectionPolicyUpdate）⇒
//	  写路径压根不碰时间戳 ⇒ 零解码脆弱性。
func TestPromptInjectionPolicyWritePayloadHasNoTimestampFields(t *testing.T) {
	typ := reflect.TypeOf(PromptInjectionPolicyUpdate{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		tag := f.Tag.Get("json")
		name := strings.TrimSuffix(strings.TrimPrefix(tag, ","), "")
		if strings.HasSuffix(name, "_at") || strings.HasSuffix(name, "_time") {
			t.Errorf("写载荷 PromptInjectionPolicyUpdate.%s (%q) 是时间戳字段 —— "+
				"写路径不碰时间戳（JSON 解码只认 RFC3339 ⇒ 客户端回传非 RFC3339 "+
				"时间戳会整包 400）", f.Name, name)
		}
	}
}

// TestPromptInjectionHandlerHasNoTextCastOnTimestampColumns 静态门：
// 本 handler 里**不许**再出现「时间戳列 ::text」这种变通写法。
//
// ★ 为什么它是缺陷而不只是「写法不同」：
//
//	① PG 的 text 输出是 "2026-07-19 21:08:25.836952+08"——**空格分隔、
//	   没有 'T'**，**不是 RFC3339** ⇒ 移动端 Date.parse（web-mobile 的
//	   relativeTime 就用它）在 Safari/iOS 上返回 NaN；
//	② 可空列 ::text 得到 NULL ⇒ 扫进 Go 的非指针 string 会报
//	   "cannot scan NULL into *string" ⇒ 又是一条 500。
func TestPromptInjectionHandlerHasNoTextCastOnTimestampColumns(t *testing.T) {
	path := "prompt_injection_handler.go"
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	timestampCols := []string{
		"created_at", "updated_at", "detected_at", "last_detection_at",
		"last_called_at", "last_leaked_at", "expires_at",
	}
	bad := map[string]bool{}

	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		val, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		lower := strings.ToLower(val)
		for _, col := range timestampCols {
			if strings.Contains(lower, col+"::text") {
				bad[col+"::text"] = true
			}
		}
		return true
	})

	for pattern := range bad {
		t.Errorf("handler 里出现了「时间戳列 %s」变通写法 —— "+
			"它产出的是 PG 文本格式（不是 RFC3339，移动端解析不了），"+
			"且可空列会撞 \"cannot scan NULL into *string\" ⇒ 500", pattern)
	}
}

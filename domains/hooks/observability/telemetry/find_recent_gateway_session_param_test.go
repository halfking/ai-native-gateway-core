package telemetry

import (
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// find_recent_gateway_session_param_test.go — 2026-11-11（ZCode 事故轮 ·
// 会话粘滞查询修复）：**参数类型 ↔ 占位符 cast** 的不变量必须有离线守卫。
//
// 事故本体（211 生产实证）：
//
//	POST /v1/messages（≥2 条消息、无客户端 session id）
//	⇒ 503「session store unavailable」（stage=ingress，请求从未派发）
//	⇒ 容器日志里的真实成因（在被丢弃之前）：
//
//	failed to encode args[1]: unable to encode 2 into text format for
//	text (OID 25): cannot find encode plan
//
//	⇒ 根因：findRecentGatewaySessionSQL 的 $2 是 `$2::text`（且
//	   session_turns_hot.api_key_id 列本身就是 text），而绑定却传了 Go 的
//	   **int** ⇒ pgx 在**编码期**找不到「int → text」的编码计划 ⇒
//	   **每次真正走到这条查询的请求都必然失败**。
//
// ★ 为什么这条查询不是每次都跑：它只在「最近会话索引 miss」时才被走到
//   （该 api key 空闲超过 5 分钟、或设备种子不匹配）⇒ 表现为间歇性 503，
//   极易被误读成「偶发上游问题」。
//
// ★ 为什么需要**离线**门（而不是只靠真库测试）：运行期才炸的错误，必须有
//   一道不用连库的守卫。真库测试在没设 DSN 时会 skip —— 而一道「结构性
//   沉睡却仍报 ok」的门等于没有门（仓内 case_when_param_type_context_test.go
//   的判据：门禁对沉睡文件不作证）。
//
// 本门按 **pgx 自己的编码规则**判定（vendored pgx v5.9.2
// pgtype/text.go：TextCodec.PlanEncode 只接受 string / []byte /
// TextValuer），不依赖连库。

// placeholderCastRe 抓出 SQL 里带显式 cast 的占位符：$N::type。
var placeholderCastRe = regexp.MustCompile(`\$(\d+)::([A-Za-z_][A-Za-z0-9_ ]*)`)

// placeholderCasts returns the explicit casts of the SQL's placeholders,
// keyed by the 1-based placeholder number.
func placeholderCasts(sql string) map[int]string {
	out := map[int]string{}
	for _, m := range placeholderCastRe.FindAllStringSubmatch(sql, -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		out[n] = m[2]
	}
	return out
}

// encodableAs reports whether pgx can encode value into the given
// PostgreSQL type. The rules mirror the vendored pgx codecs for the types
// this query uses; anything not listed is rejected (fail-closed: an unknown
// type must not silently pass the gate).
func encodableAs(pgType string, value any) bool {
	switch pgType {
	case "text", "varchar", "character varying", "char", "bpchar", "name":
		switch value.(type) {
		case string, []byte:
			return true
		}
		// pgx 的 TextCodec 还接受 pgtype.TextValuer（TextValue() string）。
		if _, ok := value.(pgtype.TextValuer); ok {
			return true
		}
		return false
	case "int2", "smallint", "int4", "integer", "int", "int8", "bigint":
		switch value.(type) {
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			return true
		}
		return false
	case "float4", "real", "float8", "double precision":
		switch value.(type) {
		case float32, float64:
			return true
		}
		return false
	case "bool", "boolean":
		_, ok := value.(bool)
		return ok
	default:
		return false
	}
}

// TestFindRecentGatewaySessionArgs_MatchPlaceholderTypes is the gate: every
// placeholder that carries an explicit cast must receive a value pgx can
// encode as that type.
func TestFindRecentGatewaySessionArgs_MatchPlaceholderTypes(t *testing.T) {
	sql, args := findRecentGatewaySessionArgs("tenant-x", 42, "hash-x", 5*time.Minute)

	casts := placeholderCasts(sql)
	if len(casts) == 0 {
		t.Fatal("no placeholder with an explicit cast found — the gate would be asleep")
	}
	for ph, cast := range casts {
		if ph > len(args) {
			t.Fatalf("$%d has no bound argument (%d args bound)", ph, len(args))
		}
		if !encodableAs(cast, args[ph-1]) {
			t.Errorf("$%d::%s is bound to a %T — pgx has no encode plan for it and the query will fail at encode time: "+
				"\"unable to encode %v into %s format (%s): cannot find encode plan\"",
				ph, cast, args[ph-1], args[ph-1], cast, cast)
		}
	}
}

// TestEncodableAs_RejectsIntForText is the negative control for the gate
// itself: the exact defect shape must be judged "not encodable", otherwise
// the gate above would be toothless (it would pass while production burns).
func TestEncodableAs_RejectsIntForText(t *testing.T) {
	if encodableAs("text", 2) {
		t.Fatal("an int must NOT be judged encodable as text — that is the production defect")
	}
	if !encodableAs("text", "2") {
		t.Fatal("a string must be judged encodable as text")
	}
	if !encodableAs("text", []byte("2")) {
		t.Fatal("a byte slice must be judged encodable as text")
	}
	if encodableAs("integer", "2") {
		t.Fatal("a string must NOT be judged encodable as integer")
	}
	if !encodableAs("integer", 2) {
		t.Fatal("an int must be judged encodable as integer")
	}
	// Unknown types are rejected (fail-closed).
	if encodableAs("some_unknown_type", "x") {
		t.Fatal("an unknown type must be rejected so the gate cannot pass silently")
	}
}

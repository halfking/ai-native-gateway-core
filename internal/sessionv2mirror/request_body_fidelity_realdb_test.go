//go:build integration

package sessionv2mirror

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	v2 "github.com/kaixuan/llm-gateway-go/domains/session/v2"
	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// ─────────────────────────────────────────────────────────────────────────────
// 缺陷C：走 OpenAI-Responses 线格式的请求，入站正文整体丢失。
//
// 真库证据（PG17.10，REPEATABLE READ，session_bodies + session_bodies_2026_10）：
//   deepseek-v4-flash 16e212d1…  request_delta=array  response_delta=array
//   claude-sonnet-5-5 a752d5e5…  request_delta=array  response_delta=array
//   grok-4.6         ced76282…   request_delta=null   response_delta=array
//   gpt-5.6-luna     d5bf2593…    request_delta=null   response_delta=array
// 恰好只有走 Responses 适配腿的两个模型是 null，签名是 req=null / resp=array。
//
// 机制（先用真实序列化器复核过，见 TestResponsesWireEnvelopeIsTheOneWeLost）：
//   domains/streaming/executors/executor_chat.go 的 proactive chat→Responses
//   升级把 `sourceBody` 换成 Responses 信封（顶层键 input / instructions /
//   max_output_tokens / model / tools，**没有 messages**），随后
//   `InboundBody: sourceBody`；而 parseProtocolMessages 的请求分支白名单只有
//   messages / contents，于是解出 nil，整条 request_delta 落成 JSON null。
//   响应侧没暴露这个洞，因为 chat→Responses 桥在落库前把回复转回了 chat 的
//   choices 信封 —— 所以坏掉的行是 req=null 而 resp=array。
//
// ⚠ 量具纪律（本文件第一条判据纪律，勿删）：
//   `request_delta IS NULL` 对 **JSON null** 返回 false —— 用 IS NULL /
//   IS NOT NULL 判定会把「字段在、值是 JSON null」误读成「有内容」。
//   本项目已因此错判过一次（先判 grok/gpt 正常、后判缺陷不成立）。
//   因此本文件一律用 `jsonb_typeof(...) = 'array'` 与内容断言（角色、条数、
//   文本），并用 TestBodyTypePredicateRejectsJSONNull 钉住量具本身。
//
// ⚠ 前提仍在场：每条用例都先断言「确实在 Responses 腿 / 确实有正文」，避免
// 「路径根本没写 body」被读成「正文完整」。见各用例的 premise 段。
// ─────────────────────────────────────────────────────────────────────────────

// responsesWireFixture 用**真实**序列化器产出 Responses 信封，而不是手写 JSON：
// 手写夹具会和 executor 实际产出的形态漂移，那样测的就不是现场那一份字节。
func responsesWireFixture(t *testing.T, chatBody string) string {
	t.Helper()
	parsed, err := ir.ParseOpenAI([]byte(chatBody))
	if err != nil {
		t.Fatalf("ir.ParseOpenAI: %v", err)
	}
	out, err := ir.SerializeResponsesRequest(parsed)
	if err != nil {
		t.Fatalf("ir.SerializeResponsesRequest: %v", err)
	}
	return string(out)
}

func retireCEntry(requestBody, responseBody string) *telemetry.RequestLogEntry {
	now := time.Now().UTC()
	session := "retire-c-session"
	status := telemetry.RequestStatusSuccess
	return &telemetry.RequestLogEntry{
		RequestID:     "retire-c-req",
		GwSessionID:   &session,
		TenantID:      "default",
		EventAt:       &now,
		Success:       true,
		RequestStatus: &status,
		RequestBody:   &requestBody,
		ResponseBody:  &responseBody,
	}
}

const retireCChatBody = `{"model":"grok-4.6","messages":[{"role":"system","content":"you are terse"},{"role":"user","content":"call the tool named f"}],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}],"max_completion_tokens":128}`

const retireCChatReply = `{"choices":[{"message":{"role":"assistant","content":"calling f"}}]}`

// 前提门：确认这份夹具真的是「无 messages 的 Responses 信封」，且修复前的解析器
// 确实拿它没办法。断言放在用例内部而不是只写在注释里。
func TestResponsesWireEnvelopeIsTheOneWeLost(t *testing.T) {
	body := responsesWireFixture(t, retireCChatBody)
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &obj); err != nil {
		t.Fatalf("fixture is not a JSON object: %v", err)
	}
	if len(obj["messages"]) > 0 {
		t.Fatalf("fixture unexpectedly carries a messages key; it would exercise the chat branch, not the Responses one")
	}
	if len(obj["input"]) == 0 {
		t.Fatalf("fixture carries no input key; it is not the Responses envelope this defect is about")
	}
	// 响应侧必须在场：否则「request_delta 为空」也可能只是整条 entry 都没正文。
	if msgs := parseResponseBody(retireCChatReply); len(msgs) != 1 {
		t.Fatalf("premise broken: response side parsed %d messages, want 1", len(msgs))
	}
}

// 主体判据：Responses 线格式的入站正文必须以真实内容形态落到 writer 手上 ——
// 与 chat 腿同构的**数组**，且文本内容真实存在（不是空数组、不是标量）。
func TestResponsesWireRequestBodyPersistsAsRealArray(t *testing.T) {
	body := responsesWireFixture(t, retireCChatBody)
	req := retireDriveHook(t, retireCEntry(body, retireCChatReply))

	// ── 内容形态断言（替代 IS NULL / IS NOT NULL）─────────────────────────
	raw, err := json.Marshal(req.RequestBody)
	if err != nil {
		t.Fatal(err)
	}
	var envelope any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	arr, isArray := envelope.([]any)
	if !isArray {
		t.Fatalf("request_delta would persist as JSON %T, want an array — the exact shape that made "+
			"grok-4.6 / gpt-5.6-luna unreadable (JSON null passes IS NOT NULL, so only a type check catches it)",
			envelope)
	}
	if len(arr) == 0 {
		t.Fatal("request_delta would persist as an empty array: non-empty by type, still a total loss")
	}
	// ── 内容断言：证明不是「凑够类型的空壳」───────────────────────────────
	joined := strings.ToLower(string(raw))
	for _, want := range []string{"you are terse", "call the tool named f"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("request_delta array lost the message text %q; a structurally-valid-but-wrong "+
				"array must not satisfy this judgement", want)
		}
	}
	if req.RequestBody[0].Role != "system" {
		t.Fatalf("first message role = %q, want system — instructions must lead, preserving chat ordering", req.RequestBody[0].Role)
	}
	if req.RequestBody[len(req.RequestBody)-1].Role != "user" {
		t.Fatalf("last message role = %q, want user", req.RequestBody[len(req.RequestBody)-1].Role)
	}
	// 响应侧仍在场（前提）：证明上面的断言不是「整条 entry 没正文」。
	if len(req.ResponseBody) != 1 {
		t.Fatalf("premise broken: response side parsed %d messages, want 1", len(req.ResponseBody))
	}
}

// chat 腿不得回退：同一份 chat 正文必须仍然解析出同样的消息。
func TestChatWireRequestBodyUnaffected(t *testing.T) {
	req := retireDriveHook(t, retireCEntry(retireCChatBody, retireCChatReply))
	if len(req.RequestBody) != 2 {
		t.Fatalf("chat-wire request parsed %d messages, want 2", len(req.RequestBody))
	}
	if req.RequestBody[0].Role != "system" || req.RequestBody[1].Role != "user" {
		t.Fatalf("chat-wire roles = %q/%q, want system/user", req.RequestBody[0].Role, req.RequestBody[1].Role)
	}
}

// 真正的 /v1/responses 客户端发来的 body 本身就是 Responses 信封（不是经
// executor 转换来的），同一条解析路径必须同样接住 —— 否则换入口就复现。
func TestNativeResponsesClientRequestBodyAlsoParses(t *testing.T) {
	body := `{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello from responses"}]}],"model":"gpt-5.6-luna"}`
	req := retireDriveHook(t, retireCEntry(body, retireCChatReply))
	raw, _ := json.Marshal(req.RequestBody)
	if !strings.Contains(strings.ToLower(string(raw)), "hello from responses") {
		t.Fatalf("native /v1/responses body lost its text; persisted=%s", raw)
	}
	// `input` 的裸字符串形态同样要接住（Responses 规范允许）。
	strBody := `{"input":"plain string prompt","model":"gpt-5.6-luna"}`
	req2 := retireDriveHook(t, retireCEntry(strBody, retireCChatReply))
	if len(req2.RequestBody) != 1 || req2.RequestBody[0].Role != "user" {
		t.Fatalf("bare-string input parsed %d messages (first role %q), want 1/user",
			len(req2.RequestBody), roleOf(req2.RequestBody))
	}
}

// 出站正文同一条线格式缺口。outbound 在本机部署里几乎从不落内容（见结论），
// 但解析器不能因此留着同一个洞：否则一旦出站被填充，缺口会以同样的形状复发。
func TestResponsesWireOutboundBodyParses(t *testing.T) {
	body := responsesWireFixture(t, retireCChatBody)
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &obj); err != nil {
		t.Fatal(err)
	}
	if !hasResponsesWireInput([]byte(body)) {
		t.Fatal("fixture is not recognised as a Responses-wire outbound object")
	}
	msgs := parseMessagesJSON([]byte(body))
	if len(msgs) == 0 {
		t.Fatal("Responses-wire outbound body parsed to zero messages")
	}
	if !strings.Contains(strings.ToLower(string(mustJSON(t, msgs))), "call the tool named f") {
		t.Fatalf("outbound parse lost the user text; parsed=%v", msgs)
	}
}

// ── 量具自身的判据 ───────────────────────────────────────────────────────────
//
// 上面所有断言都建立在「jsonb_typeof = 'array' 与内容断言能区分 array 与
// JSON null」这个前提上。这条用例把前提钉在真实 PG 上：同一个列、同一个谓词，
// 对 array 放行、对 JSON null 必须拒绝。若哪天有人把谓词换成 IS NOT NULL，
// 这里会红。
func TestBodyTypePredicateRejectsJSONNull(t *testing.T) {
	pool := retireMirrorPG(t)
	ctx := context.Background()
	schema := pool.Config().ConnConfig.RuntimeParams["search_path"]
	if _, err := pool.Exec(ctx, "CREATE TABLE "+schema+".bodies_probe (LIKE public.session_bodies_hot INCLUDING ALL)"); err != nil {
		t.Fatalf("clone real session_bodies_hot DDL: %v", err)
	}

	insert := func(id, doc string) error {
		_, err := pool.Exec(ctx, `INSERT INTO `+schema+`.bodies_probe
			(session_id, tenant_id, request_id, turn_no, kind, request_delta, ts)
			VALUES ($3,'default',$1,1,'turn_delta',$2::jsonb,now())`, id, doc, "retire-probe-"+id)
		return err
	}
	if err := insert("probe-array", `[{"role":"user","content":"kept"}]`); err != nil {
		t.Fatal(err)
	}
	if err := insert("probe-jsonnull", `null`); err != nil {
		t.Fatal(err)
	}

	var arrayOK bool
	if err := pool.QueryRow(ctx, `SELECT jsonb_typeof(request_delta) = 'array'
		FROM `+schema+`.bodies_probe WHERE request_id='probe-array'`).Scan(&arrayOK); err != nil {
		t.Fatal(err)
	}
	if !arrayOK {
		t.Fatal("predicate rejected a genuine array")
	}
	var nullPasses bool
	if err := pool.QueryRow(ctx, `SELECT jsonb_typeof(request_delta) = 'array'
		FROM `+schema+`.bodies_probe WHERE request_id='probe-jsonnull'`).Scan(&nullPasses); err != nil {
		t.Fatal(err)
	}
	if nullPasses {
		t.Fatal("predicate accepts JSON null — the gauge that produced the original wrong conclusion is back")
	}
	// 并且明确记下 IS NOT NULL 为什么会骗人（同一行、同一列）。
	var isNotNull bool
	if err := pool.QueryRow(ctx, `SELECT request_delta IS NOT NULL
		FROM `+schema+`.bodies_probe WHERE request_id='probe-jsonnull'`).Scan(&isNotNull); err != nil {
		t.Fatal(err)
	}
	if !isNotNull {
		t.Fatal("expected JSON null to satisfy IS NOT NULL; if PG ever changes this the note above is stale")
	}
	t.Log("CONFIRMED: request_delta='null' satisfies IS NOT NULL=true but jsonb_typeof='array' is false. " +
		"IS NULL / IS NOT NULL cannot see this defect.")
}

// ── 落点覆盖：热表与月分区两处列契约都必须原样接受真实数组 ──────────────────
//
// 为什么不由真 writer 落库：domains/session/v2/bodies_writer.go:310/368 与
// turn_writer.go:448 把表名硬写死成 public.*，只有「独立可丢的 *_test 库」才能
// 重定向（见本包 ingest_schema_realdb_test.go 的既有约定）。共享库上改写 public
// 被禁止，所以这里把「真链路产出的值」分别写进按真实 DDL 克隆的热表与分区表
// 两份列契约，验证内容形态在两个落点都不被拒/不被改写。
func TestPersistedBodyShapeSurvivesBothLandings(t *testing.T) {
	pool := retireMirrorPG(t)
	ctx := context.Background()
	schema := pool.Config().ConnConfig.RuntimeParams["search_path"]
	if _, err := pool.Exec(ctx, "CREATE TABLE "+schema+".bodies_hot (LIKE public.session_bodies_hot INCLUDING ALL)"); err != nil {
		t.Fatalf("clone session_bodies_hot: %v", err)
	}
	if _, err := pool.Exec(ctx, "CREATE TABLE "+schema+".bodies_part (LIKE public.session_bodies INCLUDING ALL)"); err != nil {
		t.Fatalf("clone session_bodies (partitioned parent shape): %v", err)
	}
	if _, err := pool.Exec(ctx, "CREATE TABLE "+schema+".turns_hot (LIKE public.session_turns_hot INCLUDING ALL)"); err != nil {
		t.Fatalf("clone session_turns_hot: %v", err)
	}

	body := responsesWireFixture(t, retireCChatBody)
	req := retireDriveHook(t, retireCEntry(body, retireCChatReply))
	doc := string(mustJSON(t, req.RequestBody))

	// 前提：这份文档真的是数组，且带得上文本。
	if !strings.HasPrefix(strings.TrimSpace(doc), "[") {
		t.Fatalf("fixture chain produced a non-array document: %s", doc)
	}
	for _, landing := range []struct {
		table, deltaCol, cols, vals string
	}{
		{"bodies_hot", "request_delta",
			"session_id, tenant_id, request_id, turn_no, kind, request_delta, ts",
			"$3,'default',$1,1,'turn_delta',$2::jsonb,now()"},
		{"bodies_part", "request_delta",
			"session_id, tenant_id, request_id, turn_no, kind, request_delta, ts",
			"$3,'default',$1,1,'turn_delta',$2::jsonb,now()"},
		// session_turns_hot carries request_delta too but has no `kind`
		// column (that lives on the bodies side only).
		{"turns_hot", "request_delta",
			"session_id, tenant_id, request_id, turn_no, request_delta, ts",
			"$3,'default',$1,1,$2::jsonb,now()"},
	} {
		id := "retire-c-" + landing.table
		if _, err := pool.Exec(ctx, `INSERT INTO `+schema+`.`+landing.table+
			` (`+landing.cols+`) VALUES (`+landing.vals+`)`, id, doc, "retire-"+landing.table); err != nil {
			t.Fatalf("%s insert: %v", landing.table, err)
		}
		var kind string
		var len int
		var firstRole string
		if err := pool.QueryRow(ctx, `SELECT jsonb_typeof(`+landing.deltaCol+`),
			jsonb_array_length(`+landing.deltaCol+`), `+landing.deltaCol+`->0->>'role'
			FROM `+schema+`.`+landing.table+` WHERE request_id=$1`, id).Scan(&kind, &len, &firstRole); err != nil {
			t.Fatal(err)
		}
		if kind != "array" {
			t.Fatalf("%s.%s persisted as JSON %s, want array", landing.table, landing.deltaCol, kind)
		}
		if len < 2 {
			t.Fatalf("%s.%s array length = %d, want >= 2 (system + user)", landing.table, landing.deltaCol, len)
		}
		if firstRole != "system" {
			t.Fatalf("%s.%s first role = %q, want system", landing.table, landing.deltaCol, firstRole)
		}
		var text string
		if err := pool.QueryRow(ctx, `SELECT `+landing.deltaCol+`::text FROM `+schema+`.`+landing.table+
			` WHERE request_id=$1`, id).Scan(&text); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.ToLower(text), "call the tool named f") {
			t.Fatalf("%s.%s lost the user text: %s", landing.table, landing.deltaCol, text)
		}
	}
}

// 修复必须留在源码里：把 Responses 分支摘掉（或让 outbound 不再委派）时这条会红。
func TestResponsesWireSupportIsPresentInSource(t *testing.T) {
	src, err := os.ReadFile("hook.go")
	if err != nil {
		t.Fatal(err)
	}
	code := string(src)
	if !strings.Contains(code, `root["input"]`) {
		t.Fatal("parseProtocolMessages no longer reads the Responses `input` envelope — request bodies " +
			"served over the Responses wire will silently persist as JSON null again")
	}
	if !strings.Contains(code, "hasResponsesWireInput") {
		t.Fatal("parseMessagesJSON no longer delegates the Responses-wire outbound envelope")
	}
}

func roleOf(msgs []v2.Message) string {
	if len(msgs) == 0 {
		return ""
	}
	return msgs[0].Role
}

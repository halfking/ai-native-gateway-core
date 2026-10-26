package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
)

// TestLoadV1TurnsReportsV1FamilyRetired 钉住 v1 腿退役后的**失败方式**。
//
// ★ 2026-10-20（迁移 890，v1 族整体退役）：原门
// `TestLoadV1TurnsJoinsBodiesByRequestIDAndTimestampAcrossStores` 钉的是
// 「v1 源按 request_id + ts 跨两个存储面 JOIN 出 bodies」——那个源**已经被
// DROP**，那句话描述的形状永远不会再存在。
//
// 本门替代它，钉的是**退役这件事必须被说出来**：
//
//   - LoadV1Turns 返回 errV1FamilyRetired（而不是 0 行、而不是 nil error）；
//   - **一条查询都不发**（mock 池上没有登记任何 expectation，pgxmock 对
//     意料之外的查询会直接报错）。
//
// 为什么「不发查询」也要钉：v1 族被 DROP 之后，任何提到它的语句都 42P01。
// 若哪天有人把 LoadV1Turns 改回「发一条查询、捕获 42P01、返回 0 行」，
// 那么**每一次**对账运行都会拿到「两侧都空」⇒ 报告读起来像「零漂移」，
// 而事实是「什么都没量」。本仓为这件事付过两次学费（§9.222 截断窗口、
// §9.235 真空为绿）：**「没量」与「量了，没有」必须长得不一样**。
func TestLoadV1TurnsReportsV1FamilyRetired(t *testing.T) {
	mockDB, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("new mock pool: %v", err)
	}
	defer mockDB.Close()

	loader := NewSessionLoader(mockDB)
	turns, err := loader.LoadV1Turns(context.Background(), "tenant_fixture", "gw_fixture")
	if !errors.Is(err, errV1FamilyRetired) {
		t.Fatalf("LoadV1Turns err = %v, want errV1FamilyRetired", err)
	}
	if turns != nil {
		t.Errorf("LoadV1Turns 返回了 %d 行；退役态必须返回 nil（不能返回 0 行 —— "+
			"0 行会被读成「两侧一致」）", len(turns))
	}
	// 一条查询都不该发：mock 池上没有登记任何 expectation，
	// 若实现发了查询，pgxmock 会在 ExpectationsWereMet 里点名它。
	if err := mockDB.ExpectationsWereMet(); err != nil {
		t.Fatalf("退役态不应发出任何查询：%v", err)
	}
}

func TestV1TurnStructure(t *testing.T) {
	// Test that V1Turn struct can be instantiated

	turn := V1Turn{
		RequestID:    "req_123",
		SessionID:    "gw_abc",
		TenantID:     "tenant_1",
		ClientModel:  "gpt-4",
		ProviderID:   "openai",
		CredentialID: "cred_1",
		CostUSD:      0.05,
		Success:      true,
	}

	if turn.RequestID != "req_123" {
		t.Errorf("RequestID = %v, want req_123", turn.RequestID)
	}
	if turn.CostUSD != 0.05 {
		t.Errorf("CostUSD = %v, want 0.05", turn.CostUSD)
	}
}

func TestV2TurnStructure(t *testing.T) {
	// Test that V2Turn struct can be instantiated
	turn := V2Turn{
		RequestID:        "req_123",
		TurnNo:           1,
		SessionID:        "gw_abc",
		TenantID:         "tenant_1",
		SubmitMode:       "full",
		Model:            "gpt-4",
		Provider:         "openai",
		CredentialID:     "cred_1",
		PromptTokens:     100,
		CompletionTokens: 50,
		CostUSD:          0.05,
		InjectionVerdict: "pass",
		OutputVerdict:    "pass",
		Success:          true,
		SourceKind:       "live",
		Quality:          "verified",
	}

	if turn.TurnNo != 1 {
		t.Errorf("TurnNo = %v, want 1", turn.TurnNo)
	}
	if turn.PromptTokens != 100 {
		t.Errorf("PromptTokens = %v, want 100", turn.PromptTokens)
	}
	if turn.SubmitMode != "full" {
		t.Errorf("SubmitMode = %v, want full", turn.SubmitMode)
	}
}

func TestV2BodyStructure(t *testing.T) {
	// Test that V2Body struct can be instantiated
	body := V2Body{
		SessionID: "gw_abc",
		TurnNo:    1,
		TenantID:  "tenant_1",
		RequestID: "req_123",
	}

	if body.TurnNo != 1 {
		t.Errorf("TurnNo = %v, want 1", body.TurnNo)
	}
	if body.SessionID != "gw_abc" {
		t.Errorf("SessionID = %v, want gw_abc", body.SessionID)
	}
}

func TestV2SessionStructure(t *testing.T) {
	// Test that V2Session struct can be instantiated
	session := V2Session{
		SessionID:        "gw_abc",
		TenantID:         "tenant_1",
		Status:           "active",
		TotalTurns:       10,
		TotalTokens:      1500,
		TotalCostUSD:     0.50,
		LastTurnNo:       10,
		LastModel:        "gpt-4",
		LastProvider:     "openai",
		PrimaryRequestID: "req_001",
	}

	if session.TotalTurns != 10 {
		t.Errorf("TotalTurns = %v, want 10", session.TotalTurns)
	}
	if session.TotalTokens != 1500 {
		t.Errorf("TotalTokens = %v, want 1500", session.TotalTokens)
	}
	if session.Status != "active" {
		t.Errorf("Status = %v, want active", session.Status)
	}
}

// TestCanonicalQueryContracts 钉住 loader 读的是**哪些关系**。
//
// ★ 2026-10-20（迁移 890，v1 族整体退役）：契约表里 `gw_session_id` 换成
// `session_id`。
//
// 为什么：`gw_session_id` 是 **v1 族**的会话内谓词列（loader 里
// `provider_id::text` 那个转换就是同一代形状的证据）。v1 族被 DROP 之后，
// loader 里不再有任何读 v1 的语句 ⇒ 那个列名自然也不出现了。
// 会话族的会话内谓词列是 `session_id`（`db.SessionFamilyTurnsForSessionSQL`
// 的内推谓词用的就是它）。
//
// ⇒ 「v1 的会话键不见了」本身就是退役的一条证据，所以这道门现在**反过来**
// 钉它：v1 族的列名/关系名不得再出现。
//
// 本包在仓库级 `internal/sqlreadguard` 的「无裸 v1 读」门下（该工具的
// loader.go 不在白名单里），所以这里**不重复**造一道更弱的副本；这道门只
// 守**读源形状**这一件本工具特有的事。
func TestCanonicalQueryContracts(t *testing.T) {
	data, err := os.ReadFile("loader.go")
	if err != nil {
		t.Fatalf("read loader.go: %v", err)
	}
	source := string(data)
	for _, want := range []string{
		"public.session_turns_with_current_month",
		// 2026-10-02（§9.31）：原值是 public.session_bodies_with_current_month ——
		// 该关系在真库不存在（只作为 UNIQUE 约束名存在于迁移 614/645），
		// parity 门一直产出零证据。改指真实存在的合并视图。
		"public.session_bodies_unified",
		// 2026-10-20（890）：会话族的会话内谓词列。
		"session_id",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("loader source missing canonical contract %q", want)
		}
	}
	if strings.Contains(source, "FROM session_turns") || strings.Contains(source, "FROM session_bodies") {
		t.Fatal("validator must not read V2 parent tables directly")
	}
	// v1 族的会话键列名不得再出现（它是「v1 已退役」的可观测证据）。
	// ⚠ loader.go 的**注释**里仍然提到 v1 族（那段注记解释的是退役本身），
	// 而这里是**源文本**检查 ⇒ 必须先把注释剥掉，否则注释里的历史名词
	// 会被当成活代码判成假阳性。
	if code := stripGoComments(source); strings.Contains(code, "gw_session_id") {
		t.Error("loader 的活代码里仍出现 v1 族的会话键列 gw_session_id —— " +
			"它属于已被 DROP 的 v1 族，出现在可执行语句里必然 42P01")
	}
}

// stripGoComments 去掉 Go 源码里的行注释与块注释，保留字符串字面量。
//
// 为什么需要它：本仓多处「源文本里不许出现某个名字」的门，都必须先把
// 注释剥掉——因为本仓的注记文化是**大量**在注释里讲历史（讲清楚
// 「为什么这样改」），而那些历史名词恰好就是要禁的那些。
// 用一个粗糙但可预测的剥法（不解析字符串字面量）会不会误伤？
// 会——但只影响**含斜杠的行**，而本文件里没有那种形状的字符串字面量。
// 宁可粗糙且可预测，也不要一个「聪明」的解析器在别的文件里误判。
func stripGoComments(src string) string {
	var out strings.Builder
	lines := strings.Split(src, "\n")
	inBlock := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if inBlock {
			if idx := strings.Index(trimmed, "*/"); idx >= 0 {
				inBlock = false
				trimmed = trimmed[idx+2:]
			} else {
				continue
			}
		}
		if !inBlock && strings.HasPrefix(trimmed, "//") {
			continue
		}
		if idx := strings.Index(line, "/*"); idx >= 0 && !inBlock {
			rest := line[idx+2:]
			if end := strings.Index(rest, "*/"); end >= 0 {
				line = line[:idx] + rest[end+2:]
			} else {
				inBlock = true
				line = line[:idx]
			}
		}
		out.WriteString(line)
		out.WriteString("\n")
	}
	return out.String()
}

// Note: Integration tests for LoadV1Turns, LoadV2Turns, etc. require a real database
// and are better suited for integration test suite. These unit tests verify struct
// definitions, basic type safety, and the read-only query contract.

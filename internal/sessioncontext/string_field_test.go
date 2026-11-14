// string_field_test.go — P2-2（2026-11-14 审计接力轮）钉测：裸字符串
// prompt/input 的缩减路径。修复前两例必 ErrProtectedContext（>1MB 纯文本
// prompt 必 422 的根因），修复后必须产出 ≤ 预算、形态保持字符串、带显式
// 披露标记的摘录。
package sessioncontext

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/largebody"
)

func bigStringPromptBody(head, mid, tail string) io.Reader {
	// 读取器生成输入，不构造巨型源字节切片（file_test.go 同款纪律）。
	return io.MultiReader(
		strings.NewReader(head),
		io.LimitReader(repeatingText(`unrelated line\n`), 999_996),
		strings.NewReader(mid),
		io.LimitReader(repeatingText(`unrelated line\n`), 999_996),
		strings.NewReader(tail),
	)
}

func TestReduceStringPromptFitsBudget(t *testing.T) {
	store, err := largebody.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	// 事实放尾部（head+tail 摘录语义；空 query 无关键词命中，中段省略
	// 由披露标记声明——数组路径的"中段事实保留"依赖 retained 消息构建
	// query，裸字符串无 query 可用）。
	src := bigStringPromptBody(
		`{"model":"gpt-3.5-turbo-instruct","prompt":"Summarize the following book.\n`,
		`\n`,
		`Billing database port 6432, /srv/billing/db.yaml"}`)
	ref, err := store.Capture(context.Background(), largebody.Identity{TenantID: "t", SessionID: "s"}, src)
	if err != nil {
		t.Fatal(err)
	}
	got, report, err := Reduce(context.Background(), store, ref, 64<<10)
	if err != nil {
		t.Fatalf("string prompt must reduce, not reject: %v", err)
	}
	if !json.Valid(got) || len(got) > 64<<10 || !report.Reduced {
		t.Fatalf("bad result: %+v %d", report, len(got))
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(got, &root) != nil {
		t.Fatal("output must stay an object")
	}
	var prompt string
	if json.Unmarshal(root["prompt"], &prompt) != nil {
		// 形态必须保持字符串：IR parse 依赖 `prompt` 为 string 兜底分支。
		t.Fatalf("prompt must remain a JSON string, got %s", string(root["prompt"])[:40])
	}
	for _, want := range []string{"Summarize the following book.", "6432", stringExcerptMarker[:40]} {
		if !bytes.Contains([]byte(prompt), []byte(want)) {
			t.Errorf("excerpt lost %q", want)
		}
	}
}

func TestFitStringPromptFitsBudget(t *testing.T) {
	body := []byte(`{"model":"gpt-3.5-turbo-instruct","prompt":"` +
		strings.Repeat("historical filler text. ", 60_000) + `END FACT 6432"}`)
	got, err := Fit(body, 64<<10)
	if err != nil {
		t.Fatalf("string prompt must fit, not reject: %v", err)
	}
	if len(got) > 64<<10 {
		t.Fatalf("output %d over budget", len(got))
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(got, &root) != nil {
		t.Fatal("output must stay an object")
	}
	var prompt string
	if json.Unmarshal(root["prompt"], &prompt) != nil {
		t.Fatal("prompt must remain a JSON string")
	}
	if !bytes.Contains(root["prompt"], []byte("END FACT 6432")) || !bytes.Contains(root["prompt"], []byte("[... omitted ...]")) {
		t.Error("excerpt must keep tail fact and disclosure marker")
	}
}

func TestFitResponsesInputStringFitsBudget(t *testing.T) {
	body := []byte(`{"model":"gpt-5","input":"` + strings.Repeat("filler ", 200_000) + `TAIL 6432"}`)
	got, err := Fit(body, 64<<10)
	if err != nil {
		t.Fatalf("string input must fit, not reject: %v", err)
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(got, &root) != nil {
		t.Fatal("output must stay an object")
	}
	var input string
	if json.Unmarshal(root["input"], &input) != nil {
		t.Fatal("input must remain a JSON string")
	}
	if !bytes.Contains(root["input"], []byte("TAIL 6432")) {
		t.Error("tail fact lost")
	}
}

func TestStringPromptTooSmallBudgetStillRejected(t *testing.T) {
	// 连最小摘录都放不下时保持 protected-task 合同：拒绝，不产出空壳。
	body := []byte(`{"model":"m","prompt":"` + strings.Repeat("x", 1_000_000) + `"}`)
	if _, err := Fit(body, 160); err != ErrProtectedContext {
		t.Fatalf("want ErrProtectedContext, got %v", err)
	}
}

func TestPromptArrayFormStillRejected(t *testing.T) {
	// string 数组形态（legacy 多补全）不在本修复范围内：维持拒绝。
	body := []byte(`{"model":"m","prompt":[` +
		`"` + strings.Repeat("x", 40_000) + `","` + strings.Repeat("y", 40_000) + `"]}`)
	if _, err := Fit(body, 64<<10); err != ErrProtectedContext {
		t.Fatalf("array-of-strings form must stay rejected, got %v", err)
	}
}

func TestFitArrayFormsUnchangedRegression(t *testing.T) {
	// 数组形态回归：修复不得改变既有 messages 行为。
	body := []byte(`{"messages":[{"role":"user","content":"first task"},{"role":"user","content":"` + strings.Repeat("old ", 30_000) + `"},{"role":"user","content":"current task 6432"}],"model":"m"}`)
	got, err := Fit(body, 8<<10)
	if err != nil {
		t.Fatalf("array fit must keep working: %v", err)
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(got, &root) != nil {
		t.Fatal("bad json")
	}
	if !bytes.Contains(root["messages"], []byte("current task 6432")) {
		t.Error("latest message must stay exact")
	}
}

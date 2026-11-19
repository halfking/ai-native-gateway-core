package admin

import (
	"net/http"
	"net/url"
	"testing"
	"time"
)

// 2026-11-19 时间范围统一轮：?model= 逗号分隔多值的 WHERE 形状钉测。
// 契约（与前端 ModelPicker 多选配套）：
//   - 缺省/单值：保持 `ft.model = $N`（等值谓词，不惊动既有查询计划）；
//   - 多值（含空白容忍）：`ft.model = ANY($N)`，参数为 []string；
//   - 会话级与轮次级（loadTurnsForSessions）两处口径一致。
// ★ 形状门 + 参数门都要钉：只有形状没有参数类型，ANY 数组串错层照样过编译。
func TestTurnsSessionsModelMultiValueWhere(t *testing.T) {
	cases := []struct {
		name       string
		rawQuery   string
		wantSub    string // WHERE 片段必须包含
		wantNoSub  string // WHERE 片段不得包含
		wantArgLen int
	}{
		{
			name:       "单值保持等值谓词",
			rawQuery:   "model=gpt-4o",
			wantSub:    "ft.model = $2", // tenant 先占 $1（s.tenant_id = $1 AND (...ft.model = $2)）
			wantNoSub:  "ANY(",
			wantArgLen: 2, // tenant + model
		},
		{
			name:       "多值转 ANY 数组",
			rawQuery:   "model=gpt-4o,claude-3.5",
			wantSub:    "ft.model = ANY($2)",
			wantNoSub:  "ft.model = $2)",
			wantArgLen: 2, // tenant + model
		},
		{
			name:       "空白与尾逗号容忍",
			rawQuery:   url.Values{"model": []string{" gpt-4o , claude-3.5 , "}}.Encode(),
			wantSub:    "ft.model = ANY($2)",
			wantArgLen: 2, // tenant + model
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := &http.Request{URL: &url.URL{RawQuery: c.rawQuery}}
			where, args, _ := buildTurnsSessionWhere(r, "t0", time.Time{}, time.Time{}, time.Time{}, "", 1)
			if !containsStr(where, c.wantSub) {
				t.Errorf("where = %q, want substring %q", where, c.wantSub)
			}
			if c.wantNoSub != "" && containsStr(where, c.wantNoSub) {
				t.Errorf("where = %q, must NOT contain %q", where, c.wantNoSub)
			}
			if len(args) != c.wantArgLen {
				t.Fatalf("len(args) = %d, want %d (args=%v)", len(args), c.wantArgLen, args)
			}
		})
	}
}

// 多值时参数必须是 []string 且元素与输入顺序一致（去空 + trim）。
// tenant 子句恒在（tenantID="t0"）⇒ args = [tenant, model]，model 在末位。
func TestTurnsSessionsModelMultiValueArgShape(t *testing.T) {
	r := &http.Request{URL: &url.URL{RawQuery: "model=gpt-4o,claude-3.5"}}
	_, args, _ := buildTurnsSessionWhere(r, "t0", time.Time{}, time.Time{}, time.Time{}, "", 1)
	if len(args) != 2 {
		t.Fatalf("len(args) = %d, want 2 (tenant+model)", len(args))
	}
	models, ok := args[1].([]string)
	if !ok {
		t.Fatalf("args[1] type = %T, want []string", args[1])
	}
	if len(models) != 2 || models[0] != "gpt-4o" || models[1] != "claude-3.5" {
		t.Errorf("models = %v, want [gpt-4o claude-3.5]", models)
	}
}

// 单值时参数仍是 string（ANY 化只发生在 len>1）。tenant 子句恒在
// （tenantID="t0"），故 args = [tenant, model]，model 是末参。
func TestTurnsSessionsModelSingleValueArgShape(t *testing.T) {
	r := &http.Request{URL: &url.URL{RawQuery: "model=gpt-4o"}}
	where, args, _ := buildTurnsSessionWhere(r, "t0", time.Time{}, time.Time{}, time.Time{}, "", 1)
	if len(args) != 2 {
		t.Fatalf("len(args) = %d, want 2 (tenant+model), where=%q", len(args), where)
	}
	if s, ok := args[1].(string); !ok || s != "gpt-4o" {
		t.Errorf("args[1] = %#v, want string \"gpt-4o\"", args[1])
	}
}

// splitCommaList 纯函数契约。
func TestSplitCommaList(t *testing.T) {
	if got := splitCommaList(""); len(got) != 0 {
		t.Errorf("splitCommaList(\"\") = %v, want empty", got)
	}
	if got := splitCommaList("a"); len(got) != 1 || got[0] != "a" {
		t.Errorf("splitCommaList(\"a\") = %v", got)
	}
	got := splitCommaList(" a , b ,, c ")
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("splitCommaList(\" a , b ,, c \") = %v, want [a b c]", got)
	}
}

func containsStr(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

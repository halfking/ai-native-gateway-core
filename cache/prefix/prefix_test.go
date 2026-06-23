package prefix

import (
	"encoding/json"
	"strings"
	"testing"
)

func body(t *testing.T, msgs ...[2]string) []byte {
	t.Helper()
	out := map[string]any{"model": "x", "messages": []map[string]any{}}
	list := out["messages"].([]map[string]any)
	for _, m := range msgs {
		list = append(list, map[string]any{"role": m[0], "content": m[1]})
	}
	out["messages"] = list
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func roles(t *testing.T, b []byte) []string {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(b, &obj); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var msgs []map[string]any
	if err := json.Unmarshal(obj["messages"], &msgs); err != nil {
		t.Fatalf("unmarshal msgs: %v", err)
	}
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i], _ = m["role"].(string)
	}
	return out
}

func TestStabilize_MovesSystemToFront(t *testing.T) {
	in := body(t,
		[2]string{"user", "q1"},
		[2]string{"assistant", "a1"},
		[2]string{"user", "q2"},
		[2]string{"system", "you are helpful"},
	)
	out, rep, err := Stabilize(in, Options{})
	if err != nil {
		t.Fatalf("Stabilize: %v", err)
	}
	if !rep.Changed {
		t.Fatal("expected Changed=true for buried system msg")
	}
	got := roles(t, out)
	want := []string{"system", "user", "assistant", "user"}
	if len(got) != len(want) {
		t.Fatalf("roles = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("roles[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

func TestStabilize_Idempotent(t *testing.T) {
	in := body(t,
		[2]string{"system", "s"},
		[2]string{"user", "q1"},
		[2]string{"assistant", "a1"},
		[2]string{"user", "q2"},
	)
	out1, _, _ := Stabilize(in, Options{})
	out2, rep2, _ := Stabilize(out1, Options{})
	if rep2.Changed {
		t.Fatal("second stabilize must be no-op (idempotent)")
	}
	if string(out1) != string(out2) {
		t.Error("idempotent stabilize produced different bytes")
	}
}

func TestStabilize_AlreadyStable(t *testing.T) {
	in := body(t,
		[2]string{"system", "s"},
		[2]string{"user", "q1"},
		[2]string{"assistant", "a1"},
		[2]string{"user", "q2"},
	)
	_, rep, err := Stabilize(in, Options{})
	if err != nil {
		t.Fatalf("Stabilize: %v", err)
	}
	if rep.Changed {
		t.Errorf("expected no change for already-stable body, reason=%q", rep.Reason)
	}
}

func TestStabilize_SingleMessage(t *testing.T) {
	in := body(t, [2]string{"user", "only"})
	out, rep, err := Stabilize(in, Options{})
	if err != nil {
		t.Fatalf("Stabilize: %v", err)
	}
	if rep.Changed {
		t.Error("single message should not change")
	}
	if string(out) != string(in) {
		t.Error("single message bytes should be identical")
	}
}

func TestStabilize_EmptyBody(t *testing.T) {
	out, rep, err := Stabilize(nil, Options{})
	if err != nil {
		t.Fatalf("Stabilize(nil): %v", err)
	}
	if rep != nil && rep.Changed {
		t.Error("nil body must not change")
	}
	if len(out) != 0 {
		t.Error("nil body must return empty")
	}
}

func TestStabilize_NotJSON(t *testing.T) {
	in := []byte("this is not json at all")
	out, rep, err := Stabilize(in, Options{})
	if err != nil {
		t.Fatalf("Stabilize(non-json): %v", err)
	}
	if rep == nil || rep.Changed {
		t.Error("non-json must pass through unchanged")
	}
	if string(out) != string(in) {
		t.Error("non-json bytes must be identical")
	}
}

func TestStabilize_NoMessagesField(t *testing.T) {
	in := []byte(`{"model":"x","input":"hello"}`)
	out, rep, err := Stabilize(in, Options{})
	if err != nil {
		t.Fatalf("Stabilize: %v", err)
	}
	if rep == nil || rep.Changed {
		t.Error("non-chat body must pass through")
	}
	if string(out) != string(in) {
		t.Error("non-chat bytes must be identical")
	}
}

func TestStabilize_PreservesHistoryOrder(t *testing.T) {
	in := body(t,
		[2]string{"user", "first"},
		[2]string{"assistant", "1st"},
		[2]string{"user", "second"},
		[2]string{"assistant", "2nd"},
		[2]string{"user", "third"},
		[2]string{"system", "sys"},
	)
	out, _, _ := Stabilize(in, Options{})
	got := roles(t, out)
	asstCount := 0
	for _, r := range got {
		if r == "assistant" {
			asstCount++
		}
	}
	if asstCount != 2 {
		t.Errorf("expected 2 assistant turns preserved, got %d (roles: %v)", asstCount, got)
	}
}

func TestStabilize_TailTurnsOption(t *testing.T) {
	in := body(t,
		[2]string{"system", "s"},
		[2]string{"user", "q1"},
		[2]string{"assistant", "a1"},
		[2]string{"user", "q2"},
		[2]string{"assistant", "a2"},
	)
	_, rep, _ := Stabilize(in, Options{TailTurns: 2})
	tailCount := 0
	for _, c := range rep.Classes {
		if c == TailClass {
			tailCount++
		}
	}
	if tailCount != 2 {
		t.Errorf("TailTurns=2 should classify 2 tail msgs, got %d", tailCount)
	}
}

func TestStability_String_Stable(t *testing.T) {
	cases := []struct {
		s   Stability
		tag string
	}{
		{SystemClass, "system"},
		{ToolClass, "tool"},
		{HistoryClass, "history"},
		{TailClass, "tail"},
	}
	for _, c := range cases {
		if got := c.s.String(); got != c.tag {
			t.Errorf("Stability(%d).String() = %q, want %q", c.s, got, c.tag)
		}
	}
}

func TestStabilizeStrict_InvalidJSON(t *testing.T) {
	_, _, err := StabilizeStrict([]byte("not json"), Options{})
	if err != ErrInvalidBody {
		t.Errorf("err = %v, want ErrInvalidBody", err)
	}
}

func TestStabilize_DeveloperRole(t *testing.T) {
	in := body(t,
		[2]string{"user", "q"},
		[2]string{"developer", "instructions"},
	)
	out, rep, _ := Stabilize(in, Options{})
	if !rep.Changed {
		t.Fatal("developer role should be hoisted like system")
	}
	got := roles(t, out)
	if got[0] != "developer" {
		t.Errorf("developer should lead, got order %v", got)
	}
}

func TestStabilize_PreservesToolsField(t *testing.T) {
	in := []byte(`{
		"model": "x",
		"messages": [{"role":"user","content":"q"},{"role":"system","content":"s"}],
		"tools": [{"type":"function","function":{"name":"foo"}}]
	}`)
	out, _, err := Stabilize(in, Options{})
	if err != nil {
		t.Fatalf("Stabilize: %v", err)
	}
	var obj map[string]json.RawMessage
	_ = json.Unmarshal(out, &obj)
	if string(obj["tools"]) == "" {
		t.Error("tools field must be preserved")
	}
	if !strings.Contains(string(obj["tools"]), "foo") {
		t.Error("tools content must be preserved verbatim")
	}
}

func TestMustStabilize_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("MustStabilize panicked: %v", r)
		}
	}()
	in := body(t, [2]string{"user", "q"}, [2]string{"system", "s"})
	_ = MustStabilize(in, Options{})
}

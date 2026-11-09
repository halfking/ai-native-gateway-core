package logging

import (
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// assertNoDuplicateKeys 断言一行 JSON 里**没有重复键**。
//
// 为什么必须手写检测：Go 的 json.Unmarshal 对重复键**取最后一个且不报错**，
// Python json / jq 同样取最后一个——但另一些工具取第一个或直接报 parse error。
// 同一份日志在不同解析器里「服务版本」可能变成业务版本 ⇒ AI 侧解析有歧义。
//
// 产出端（规范层）必须杜绝重复键；门禁侧也应能检出（见 vibe-coding
// scripts/verify-log-standard.py 的重复键检测）。
func assertNoDuplicateKeys(t *testing.T, line string) {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(line))
	tok, err := dec.Token()
	if err != nil {
		t.Fatalf("不是合法 JSON: %q -- %v", line, err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		t.Fatalf("顶层不是 JSON 对象: %q", line)
	}
	seen := map[string]bool{}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			t.Fatalf("读键失败: %q -- %v", line, err)
		}
		key, ok := kt.(string)
		if !ok {
			t.Fatalf("键不是字符串: %q", line)
		}
		if seen[key] {
			t.Errorf("重复键 %q: %s", key, line)
		}
		seen[key] = true
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			t.Fatalf("读值失败: %q -- %v", line, err)
		}
	}
}

// firstJSONLine 取输出的第一行并解析成 map。
func firstJSONLine(t *testing.T, out []byte) (map[string]any, string) {
	t.Helper()
	line := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
	var obj map[string]any
	if err := json.Unmarshal([]byte(line), &obj); err != nil {
		t.Fatalf("不是合法 JSON: %q -- %v", line, err)
	}
	return obj, line
}

// TestReservedNameStrippedFromRecord 锁住「业务侧占用保留名会被剥离」。
//
// 回归背景：cmd/gateway 的 loadHardBlockPolicy() 打的是
// `slog.Info("hard block allowlist loaded", "env", "LLM_GATEWAY_HARD_BLOCK_RULES", ...)`
// —— "env" 既是 rule 71 的**部署环境名**保留名，又是这里的环境变量名。
// 规范层注入 "env":"local" 后，同一行 JSON 里出现两个 "env" 键。
func TestReservedNameStrippedFromRecord(t *testing.T) {
	read := captureStderr(t)
	InstallEarly(slog.LevelInfo)
	slog.Info("hard block allowlist loaded",
		"env", "LLM_GATEWAY_HARD_BLOCK_RULES",
		"allows", []string{"input:*"},
	)
	out := read()
	obj, line := firstJSONLine(t, out)

	assertNoDuplicateKeys(t, line)

	// 规范层的权威值必须生效。
	if obj["env"] != ServiceEnv() {
		t.Errorf("env = %v, want %v", obj["env"], ServiceEnv())
	}
	// 业务侧的同名值不得残留在顶层（它已改用 env_var 表达）。
	if strings.Contains(line, "LLM_GATEWAY_HARD_BLOCK_RULES") {
		t.Errorf("业务侧 env 值仍在顶层（应被剥离并由 env_var 承载）: %s", line)
	}
	// 冲突必须可见——不能静默丢弃。
	conflicts, ok := obj["reserved_name_conflicts"].([]any)
	if !ok {
		t.Fatalf("缺 reserved_name_conflicts 诊断字段: %s", line)
	}
	found := false
	for _, c := range conflicts {
		if c == "env" {
			found = true
		}
	}
	if !found {
		t.Errorf("reserved_name_conflicts = %v, want 含 env", conflicts)
	}
}

// TestReservedNameStrippedFromWith 锁住「With() 传的保留名也会被剥离」。
//
// 为什么：With 传的 attr 会落进**内层** handler 的预格式化区，
// Handle 阶段看不到它们 ⇒ 不在 WithAttrs 里剥离就会绕过规范层，
// 直接与规范层注入的字段撞键。
func TestReservedNameStrippedFromWith(t *testing.T) {
	read := captureStderr(t)
	InstallEarly(slog.LevelInfo)
	slog.Default().With("instance_id", "business-value").Info("with-attrs")
	out := read()
	obj, line := firstJSONLine(t, out)

	assertNoDuplicateKeys(t, line)
	if obj["instance_id"] != InstanceID() {
		t.Errorf("instance_id = %v, want %v", obj["instance_id"], InstanceID())
	}
	if strings.Contains(line, "business-value") {
		t.Errorf("With 传的业务 instance_id 残留: %s", line)
	}
}

// TestReservedNameStrippedFromGroup 锁住「group 内的保留名也会被剥离」。
//
// slog.Group 是嵌套对象，规范要求扁平单层 ⇒ 规范层会展开它；
// 展开后 group 内的 "env" 会撞上顶层规范字段。
func TestReservedNameStrippedFromGroup(t *testing.T) {
	read := captureStderr(t)
	InstallEarly(slog.LevelInfo)
	slog.Info("group-attrs", slog.Group("ctx", "env", "GROUP_VALUE", "keep", "yes"))
	out := read()
	obj, line := firstJSONLine(t, out)

	assertNoDuplicateKeys(t, line)
	if obj["env"] != ServiceEnv() {
		t.Errorf("env = %v, want %v", obj["env"], ServiceEnv())
	}
	if strings.Contains(line, "GROUP_VALUE") {
		t.Errorf("group 内的 env 值残留: %s", line)
	}
	// 非保留名的 group 成员要保留（不能连坐丢数据）。
	if obj["keep"] != "yes" {
		t.Errorf("group 内的普通字段被误删: %s", line)
	}
}

// TestInstanceIDIsProcessConstant 锁住「instance_id 是进程级常量」。
//
// 回归背景：规范层在一个进程里会被装多次（早装 → 全装 → 热加载），
// 此前每次装都重新生成 instance_id ⇒ 同一进程不同日志行带不同 instance_id，
// 「按 instance_id 聚合一个实例的全部日志」这条查询就不可靠了。
func TestInstanceIDIsProcessConstant(t *testing.T) {
	first := InstanceID()
	if first == "" {
		t.Fatal("InstanceID 为空")
	}
	for i := 0; i < 5; i++ {
		if got := InstanceID(); got != first {
			t.Fatalf("InstanceID 第 %d 次变化: %q != %q", i, got, first)
		}
	}
}

// TestReservedNamesAreNonEmpty 锁住保留名集合非空且含关键字段。
func TestReservedNamesAreNonEmpty(t *testing.T) {
	names := ReservedNames()
	if len(names) == 0 {
		t.Fatal("保留名集合为空")
	}
	want := map[string]bool{
		"service": true, "version": true, "env": true, "instance_id": true,
		"timestamp": true, "severity_text": true, "severity_number": true,
		"level": true, "msg": true,
	}
	for _, n := range names {
		delete(want, n)
	}
	if len(want) > 0 {
		t.Errorf("保留名集合缺少: %v", want)
	}
}

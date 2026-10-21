package ursm_test

// 252 部署脚本的 **密钥不轮换** 守卫（2026-10-21，§143.53 轮新增）
//
// # 背景：一次部署 = 一次全量鉴权失效
//
// `scripts/deploy-252-gateway.sh` 原本在每次部署时**无条件重新生成**
// `LLM_GATEWAY_SECRET_KEY` 与 `LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY`：
//
//	GEN_KEY=$(python3 -c "import secrets;print(secrets.token_urlsafe(33))")
//	ENC_KEY=$(python3 -c "import secrets;print(secrets.token_urlsafe(33))")
//
// ★★ 而这两个值都不是「随便换一下」的东西：
//
//  1. `LLM_GATEWAY_SECRET_KEY` 是库内 `api_keys.key_hash` 的 **HMAC 密钥**
//     （`domains/authentication/verifier.go:623` `HashAPIKey(secretKey, key)`
//     = HMAC-SHA256）⇒ ⇒ **换掉它 ⇒ 库里每一条 api_key 的哈希都再也算不出
//     同样的值 ⇒ ⇒ 全部 API key 一次性失效**；
//  2. `LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY` 换了 ⇒ 库里用旧钥加密的凭据
//     解不开 ⇒ ⇒ 凭据面全量 decrypt_failed。
//
// ⇒ ⇒ 「dev 实例无所谓」**不成立**：DB 是共享的 172.16.2.210 库，`api_keys`
// 对全实例可见 ⇒ ⇒ **每次部署都打断现网鉴权**。
// ⇒ ⇒ 修法：**先从远端现有 `.env.dev` 读回这两个值，存在就沿用**；只有远端
// 还没有（首次部署）才生成新的。
//
// # 本门的形状
//
// ★ **断言前必须剥注释**（R65 §M6 教训：同一段文字可能同时出现在注释里 ⇒
// 删掉代码只留注释，门照样绿）。所以本门先做 shell 注释剥离，再对**剥离后的
// 文本**断言；并用一条「合成样本」自证剥离器有效（**门自己有牙**）。

import (
	"os"
	"strings"
	"testing"
)

const deploy252File = "../../scripts/deploy-252-gateway.sh"

// stripShellCommentsAndInline 去掉 shell 注释：
//   - 整行注释（首个非空白字符是 `#`）整行丢弃；
//   - 行内注释：`#` 前面有空白、且不在单引号/双引号内 ⇒ 从该处截断。
//
// ★ 不剥注释的门 = 会被注释喂饱的门 ⇒ 本函数是本门的**前置条件**，
// 不是可选的礼貌。
//
// ★★ 为什么不复用同包的 `stripShellComments`（health_topology_free_verdict_test.go）：
// 那条只剥**整行**注释；本门要防的形态是「保证写在**行内**注释里」
// （`GEN_KEY=$(...)  # [[ -n "$GEN_KEY" ]] || GEN_KEY=`）⇒ ⇒ 需要行内剥离
// ⇒ ⇒ **同名会撞编译**（同包内重复声明）⇒ ⇒ 故用**不同名**的增强版，
// 且**不动**他人那条函数（复制必漂 + 同文件不要两个写者）。
func stripShellCommentsAndInline(src string) string {
	var out []string
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		var b strings.Builder
		var quote byte
		for i := 0; i < len(line); i++ {
			c := line[i]
			if quote != 0 {
				b.WriteByte(c)
				if c == quote {
					quote = 0
				}
				continue
			}
			if c == '\'' || c == '"' {
				quote = c
				b.WriteByte(c)
				continue
			}
			if c == '#' && i > 0 && (line[i-1] == ' ' || line[i-1] == '\t') {
				break // 行内注释 → 截断
			}
			b.WriteByte(c)
		}
		out = append(out, b.String())
	}
	return strings.Join(out, "\n")
}

// keyPreservationGaps 返回「缺哪些断言」的清单（空 = 满足）。
// ★ 抽成函数 ⇒ 正例与合成反例共用**同一把尺**（否则反例证明不了尺的有效性）。
func keyPreservationGaps(stripped string) []string {
	need := []struct {
		what string
		want string
	}{
		{"读回远端既有 .env.dev", "$REMOTE_DIR/.env.dev"},
		{"从远端 env 提取 SECRET_KEY", "s/^LLM_GATEWAY_SECRET_KEY=//p"},
		{"从远端 env 提取 CREDENTIAL_ENCRYPTION_KEY", "s/^LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY=//p"},
		{"SECRET_KEY 的生成必须是【兜底】而非无条件", `[[ -n "$GEN_KEY" ]] || GEN_KEY=`},
		{"CREDENTIAL_ENCRYPTION_KEY 的生成必须是【兜底】而非无条件", `[[ -n "$ENC_KEY" ]] || ENC_KEY=`},
		{"两把密钥必须互不相同（R37 D14 的原意）", `[[ "$GEN_KEY" == "$ENC_KEY" ]]`},
		{"读回的临时副本必须被 shred", "shred -u"},
		{"shred 的目标必须含读回的临时文件", "remote.env.prev"},
	}
	var gaps []string
	for _, n := range need {
		if !strings.Contains(stripped, n.want) {
			gaps = append(gaps, n.what+"（缺："+n.want+"）")
		}
	}
	return gaps
}

func readDeploy252(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(deploy252File)
	if err != nil {
		t.Fatalf("read %s: %v", deploy252File, err)
	}
	return string(b)
}

func TestDeploy252PreservesExistingSecretKeys(t *testing.T) {
	stripped := stripShellCommentsAndInline(readDeploy252(t))
	if gaps := keyPreservationGaps(stripped); len(gaps) > 0 {
		for _, g := range gaps {
			t.Errorf("252 部署脚本缺少【密钥不轮换】的保证：%s", g)
		}
		t.Fatalf("共 %d 处缺口 ⇒ ★ 一次部署 = 一次全量 API key 失效 + 凭据全量解不开", len(gaps))
	}
}

// TestDeploy252KeyPreservationGateHasTeeth —— **门自己有牙**。
//
// 合成一份「把保证全部写进注释、代码里什么都不做」的脚本 ⇒
// 同一把尺（keyPreservationGaps）必须**报出缺口** ⇒
// ⇒ 证明本门不会被注释喂饱（否则真实缺陷存在时门也会绿）。
func TestDeploy252KeyPreservationGateHasTeeth(t *testing.T) {
	commentOnly := strings.Join([]string{
		"#!/usr/bin/env bash",
		"# 这里假装读回 $REMOTE_DIR/.env.dev",
		"# s/^LLM_GATEWAY_SECRET_KEY=//p",
		"# s/^LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY=//p",
		"# [[ -n \"$GEN_KEY\" ]] || GEN_KEY=",
		"# [[ -n \"$ENC_KEY\" ]] || ENC_KEY=",
		"# [[ \"$GEN_KEY\" == \"$ENC_KEY\" ]]",
		"# shred -u remote.env.prev",
		"GEN_KEY=stub",
		"ENC_KEY=stub",
	}, "\n")
	gaps := keyPreservationGaps(stripShellCommentsAndInline(commentOnly))
	if len(gaps) == 0 {
		t.Fatal("门无牙：合成样本把保证全写在注释里，尺子却报『满足』⇒ 剥离器失效")
	}
	t.Logf("尺子按预期报出 %d 处缺口（首条：%s）", len(gaps), gaps[0])
}

// TestStripShellCommentsKeepsCode —— 剥离器的最小自检：
// 行内注释被去掉，同一行里的代码必须原样保留。
func TestStripShellCommentsAndInlineKeepsCode(t *testing.T) {
	in := "GEN_KEY=$(python3 -c 'x')  # 这里会轮换密钥\n# 整行注释\nENC_KEY=\"y\"\n"
	out := stripShellCommentsAndInline(in)
	if strings.Contains(out, "会轮换密钥") {
		t.Error("剥离器没有去掉行内注释")
	}
	if !strings.Contains(out, "GEN_KEY=$(python3 -c 'x')") {
		t.Error("剥离器把代码一起删了")
	}
	if !strings.Contains(out, `ENC_KEY="y"`) {
		t.Error("剥离器把正常行删了")
	}
}

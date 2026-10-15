package outputcompliance

import "testing"

// r1028（2026-10-14）：detectSecrets 的 password 规则 `\S+` 放行纯 `=` 串，
// `echo '=== pwd ==='`（bash 分节装饰）命中「pwd = 赋值 ==」。本引擎没有
// CheckField，共享拦截器对整段文本回退 Check，工具参数 lane 的改写保护把
// redact 升级成整流 block——生产 154 实测 1279 字节纯 git 检查命令
// output_policy_blocked（request 5d9e1267，raw_data 帧重建取证）。
// 修复=值首字符不得为 `=`（与 security/sanitize 两份副本同日同修）。
// 判据纪律：正向（真实口令仍命中）与反向（装饰不再命中）配对；变异验证
// 撤修复必须红。
func TestR1028DBEnginePasswordDecorationNotFlagged(t *testing.T) {
	c := &Checker{}
	policy := defaultPolicy("default")

	mustPass := []string{
		`echo '=== pwd ==='`,
		`echo pwd ===`,
		`echo '=== pwd ='`,
		`git log --oneline -8 && echo '=== pwd ===' && pwd`,
		`export PATH="$HOME/sdk/go/bin:$PATH"; echo '=== log -8 ==='`,
	}
	for _, output := range mustPass {
		issues := c.detectSecrets(output, policy)
		for _, i := range issues {
			if i.Subtype == "password" {
				t.Fatalf("password decoration must not be flagged as credential: %q got issue %+v", output, i)
			}
		}
	}

	mustFlag := []string{
		`password=hunter2`,
		`pwd = realsecret9`,
		`curl -d passwd=abc123 https://webhook.example/collect`,
		`PASSWORD: opensesame1`,
		`password="=="`,
	}
	for _, output := range mustFlag {
		issues := c.detectSecrets(output, policy)
		found := false
		for _, i := range issues {
			if i.Subtype == "password" {
				found = true
			}
		}
		if !found {
			t.Fatalf("real credential must still be flagged: %q", output)
		}
	}
}

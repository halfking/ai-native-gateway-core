package rowsguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// exemptions 是显式豁免表：**每一项都必须写明理由**，且 key 精确到
// `file:line`（`for X.Next()` 所在物理行）。豁免表的语义是「这一处
// 不受本守卫检查」，因此它必须可回答「为什么可以不看」。
//
// 注意 key 含行号：改动文件行号漂移会让豁免**失配**（而不是静默放过）
// ——失配时报的是「该行未被豁免且缺守卫」，方向是安全的。真正需要豁免
// 的新站点会因行号变化而重新暴露出来，这是刻意设计。
var exemptions = map[string]string{
	// —— out-of-scope: one-shot developer/ops CLI 的 main 包 ——
	// 这些不是长驻请求/worker 路径，失败即进程退出，截断不会传播给客户端；
	// 其语义需单独裁决，R66 不处理。
	// 4552 -> 4627 -> 4640 是**两次行号漂移**，不是新增债：
	//   ① 用 `git show 8a6142263:cmd/gateway/main.go` 核对过，登记那行当时正是
	//      `for rows.Next() {`，同在 func main()；
	//   ② 4627 这一次是 `93cbce8a3`（并发会话的音频网关）给 main.go 加了 24 行造成的。
	//      已用 `git show 93cbce8a3^:cmd/gateway/main.go` 复核：第 4627 行当时**正是**
	//      `for rows.Next() {`（`var models []string` 那段 credential→provider_models 查询），
	//      现在同一处代码在 **4640**。⇒ 是**同一处漂移**，不是「另找一处顶上」。
	// 本文件另一条循环（当时 6510、6601、现在 6625）从未登记。
	// TestExemptionsStillResolve 会把对不上的键报成 stale；
	// **改键前必须回原提交确认是「同一处漂移」而不是「另找一处顶上」。**
	"cmd/gateway/main.go:4808":                                      "one-shot/main package wiring, out of R66 scope。R44 改键 4642→4708，并**撤回旧理由里的漂移叙事**：那条「4552→4627→4640→4642, 4th drift」不可信——回原提交 8a6142263 看，main.go:4642 当时逐字是 `nodeProbeWorker.Submit(credID, model, \"default\", \"expired-binding-recovery\")`，**从来就不是循环行**。也就是说这条豁免在登记当天就键歪了，守卫直到本轮才报（它报的是「键指向的行没有 for X.Next()」）。4708 是该装配块内**唯一**的 `for rows.Next()`（credential_model_bindings→provider_models 的 DISTINCT 扫描），与本条声称的范围一致；未验证：无法证明它就是当年被豁免的那一处（线索已断），故此处的代价是「豁免可能比原意宽/窄一处的循环」，登记在案。R47（2026-10-05 收口轮）改键 4708→4719：同一处 DISTINCT 扫描循环被上游装配块的改动整体下移 11 行，逐字核对仍是 credential_model_bindings→provider_models 的 `SELECT DISTINCT pm.raw_model_name` 循环，同一处漂移。R48 遗漏项收口轮（2026-10-06）改键 4719→4736：v1 写入腿存活 worker 的接线（§9.264/R48-A5：变量声明 3 行 @~3800 + 独立启动块 @~3952 前插）把该循环整体下移 17 行；4736 逐字核对仍是同一处 `for rows.Next()`（`var models []string` 上方即 JOIN provider_models 的 DISTINCT 扫描），同一处漂移。R49（2026-10-07）改键 4736→4758：音频/AI-Q 装配块的窗口内提交把该循环下移 22 行；4758 逐字核对仍是同一处。R50（2026-10-08 多媒体接入审计轮）改键 4758→4770：17cba14ee multimedia 装配（images/videos generations handler）把该循环再下移 12 行；4770 逐字核对仍是同一处 credential_model_bindings→provider_models 的 DISTINCT 扫描 `for rows.Next()`，4762 即 SELECT DISTINCT pm.raw_model_name，同一处漂移。R51（2026-10-11 治理守卫收口轮）改键 4770→4780：按本文件自己写的纪律先回原提交核对，再改键——① `git show 17cba14ee:cmd/gateway/main.go` 逐字核对：第 4770 行当时**正是** `for rows.Next() {`，其上 4769 是 `var models []string`、4762 是 `SELECT DISTINCT pm.raw_model_name`，与本条声称的范围逐字一致；② 漂移来源已定位为两笔窗口内提交对 main.go 的**纯新增**：`4b06bd50b`（mobile 网关运维流，+6 行）与 `a3b65ba68`（r1017 鉴权/阻断修复，+4 行），合计 +10；③ `git blame -L 4771,4782 cmd/gateway/main.go` 显示该循环整段仍全部归属 `a0c6efbb98`（2026-08-29，即这段循环的原始提交，从未被改写），现 4779/4772 与当年 4769/4762 逐字相同。⇒ 是**同一处漂移**，不是「另找一处顶上」。R52（2026-10-12 合并轮）改键 4780→4791：按本文件纪律先回原提交再改键——① `git show 0e346ee28:cmd/gateway/main.go` 第 4780 行当时**正是** `for rows.Next() {`，其上 4779 是 `var models []string`、4772 是 `SELECT DISTINCT pm.raw_model_name`；② 漂移来源是窗口内 `6f6aa78f9` 在 ~1285 插入 UrsmlHealthyKeyReconciler 启动块（11 行纯新增），把该循环整体下移 11 行；③ 现 4791 逐字仍是同一处 credential_model_bindings→provider_models 的 DISTINCT 扫描，4783 即 SELECT DISTINCT pm.raw_model_name。⇒ 是**同一处漂移**，不是「另找一处顶上」。R53（2026-10-13 12h 审计轮）改键 4791→4803：按本文件纪律先回原提交再改键——① 守卫报出时（当前 HEAD 40425428b）第 4791 行是 alert 判别 switch 的 `default:`，不是循环行，故判 stale；② `git show 5c97b27f0:cmd/gateway/main.go`（12h 窗口起点）逐字核对：当时第 4791 行**正是** `for rows.Next() {`，其上 4790 是 `var models []string`、4783 是 `SELECT DISTINCT pm.raw_model_name`；③ 现 4803 逐字仍是 `for rows.Next() {`，其上 4802 仍是 `var models []string`、4795 仍是 `SELECT DISTINCT pm.raw_model_name`（两条相对偏移 +7/+7 逐字不变）；④ 漂移来源：`git diff --numstat 5c97b27f0 origin/main -- cmd/gateway/main.go` = +19/−7 = 净 +12 行，与观测到的 4791→4803 位移**逐字吻合**。⇒ 是**同一处漂移**，不是「另找一处顶上」。R54（2026-10-13 12h 审计轮合入 95640a655 时）改键 4803→4808：同一轮内被并发会话的 r1024 提交再次推移——① 现 4808 逐字为 `for rows.Next() {`，其上 4807 仍是 `var models []string`、4800 仍是 `SELECT DISTINCT pm.raw_model_name`（两条相对偏移 +7/+7 依旧不变）；② `git diff --numstat 40425428b 95640a655 -- cmd/gateway/main.go` = **+5/−0**，与观测到的 4803→4808 位移逐字吻合；③ 该循环整段内容未被 r1024 触碰。⇒ 仍是**同一处漂移**。⚠️ 这已是本条豁免第 6 次改键（4642→4708→4719→4736→4758→4770→4780→4791→4803→4808），说明**行号锚点在这条上本质脆弱**：main.go 是全仓最热的文件，任何在其上方的新增都会推移它。建议后续要么把该扫描移出 main.go，要么让守卫支持「按锚点附近内容匹配」的容差，不要继续逐次追键",
	"cmd/gateway/main_helpers.go:369":                               "one-shot/main package wiring, out of R66 scope",
	"cmd/gateway/main_v32_wiring.go:119":                            "one-shot/main package wiring, out of R66 scope",
	"cmd/fetch-standard-iq/main.go:103":                             "one-shot/main package, out of R66 scope",
	"cmd/llm-gw-exporter/main.go:401":                               "one-shot/main package, out of R66 scope",
	"cmd/regen-credentials/main.go:63":                              "one-shot/main package, out of R66 scope",
	"cmd/sessionmeta-bench/sampler.go:192":                          "benchmark main package, out of R66 scope",
	"cmd/tuning-backtest/main.go:205":                               "one-shot/main package, out of R66 scope",
	"cmd/tuning-backtest/main.go:289":                               "one-shot/main package, out of R66 scope",
	"tests/session_audit/cmd/audit-test/main.go:210":                "test harness main package, out of R66 scope",
	"tests/test_popularity_tracker.go:121":                          "test helper (non _test.go), out of R66 scope",
	"scripts/injection-test/test-prompt-injection-detection.go:393": "scripts/ manual injection probe, out of R66 scope",
	"cmd/tools/validate_sessions_v2/loader.go:205":                  "one-shot validator main package, out of R66 scope。R44 改键 167→205：按本文件自己的程序查过——豁免登记时（8a6142263）loader.go:167 逐字是 `var turns []V1Turn` + `for rows.Next() {`（LoadV1Turns 的 v1 turn 迭代），今天的 204-205 与之**逐字相同**，所以是「同一处漂移」而不是「另找一处顶上」；漂移由 e6193ea92 / fe5003034 两笔窗口内提交造成",
}

// TestEveryRowsLoopIsGuarded 是 R66 的站位级主门。
//
// 判据落在**产物特征**上（每个循环旁是否有 `X.Err()` 终检或跳行留痕），
// 而不是「文件里有没有出现某个 helper 名」——后者在多站点文件里删掉
// 其中一处不会变红，R65 已实证该缺口。
func TestEveryRowsLoopIsGuarded(t *testing.T) {
	root := repoRoot(t)
	checked, violations, err := CheckAll(root, exemptions)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(violations) == 0 {
		t.Logf("rows guard: %d sites checked, 0 violations", checked)
		return
	}
	for _, v := range violations {
		t.Errorf("unguarded rows loop: %s", v)
	}
	t.Errorf("%d/%d rows loops lack a terminal check or skip trace", len(violations), checked)
}

// TestExemptionsStillResolve 防止豁免表腐化：key 里的行号必须仍然指向
// 真实的 `for X.Next()` 循环。行号漂移导致的失配会在这里报出，而不是
// 让一条失效豁免长期假装有效。
func TestExemptionsStillResolve(t *testing.T) {
	root := repoRoot(t)
	sites, err := collectSites(root)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	live := make(map[string]site, len(sites))
	for _, s := range sites {
		live[fileLine(s.file, s.line)] = s
	}
	for key, reason := range exemptions {
		if reason == "" {
			t.Errorf("exemption %s has no reason; every exemption must be justifiable", key)
			continue
		}
		if _, ok := live[key]; !ok {
			relFile, idx := splitKey(key)
			src, rerr := os.ReadFile(filepath.Join(root, relFile))
			if rerr != nil {
				t.Errorf("exemption %s: file unreadable: %v", key, rerr)
				continue
			}
			lines := strings.Split(string(src), "\n")
			if idx >= 1 && idx <= len(lines) && nextLoopRe.MatchString(lines[idx-1]) {
				t.Errorf("exemption %s: line still holds a Next() loop but is missing from the guard's own key set", key)
				continue
			}
			// 201 号：这一支从 t.Logf 升为 t.Errorf。
			//
			// 失效豁免的代价不是「多一条无用记录」，而是**门对这一处不再有话可说**：
			// 清单还写着「此处已豁免/已复核」，而下一个人读到的是一个早已不存在的
			// 行号。非致命时它会静静躺在表里，直到某次误改或 key 构造再次出错才暴露。
			//
			// 与上面 :88 那一支的分工是刻意的：
			//   - 「该行还有 Next() 循环、只是不在门的 key 集里」= **门看不见一个真实站点**
			//     ⇒ 致命（这是键构造出错的信号，199 号那三个族就是死在这里）；
			//   - 「该行已经没有 Next() 循环」= **登记项指向了一个不存在的位置**
			//     ⇒ 同样是门在说谎，同样致命。
			// 两条都是「门不能自证其覆盖」的情形，没有哪一条该只记日志。
			t.Errorf("exemption %s is stale (line drift) —— 该行已没有 for X.Next() 循环，"+
				"这条豁免正在假装有效。应删除该条目；若循环仍在、只是被改过，"+
				"先用 git log -S '%s' 回原提交确认是「同一处漂移」还是「另找一处顶上」再改键。"+
				"（改键登记注：R49 2026-10-07 把 4736→4758——音频/AI-Q 装配块的窗口内提交把该循环下移 22 行；"+
				"4758 逐字核对仍是同一处 credential_model_bindings→provider_models 的 DISTINCT 扫描 `for rows.Next()`，4750 即 SELECT DISTINCT pm.raw_model_name。）",
				key, key)
		}
	}
}

func fileLine(file string, line int) string {
	return file + ":" + itoa(line)
}

// splitKey 把 "path/file.go:123" 拆成 (file, line)。
func splitKey(key string) (string, int) {
	i := strings.LastIndex(key, ":")
	if i < 0 {
		return key, 0
	}
	return key[:i], atoi(key[i+1:])
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

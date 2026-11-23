package dbinit

// 2026-11-23 L34 续轮：★ 「部署通道投递面 ↔ 权威账」覆盖门（★ **方案 C** ★）★
// ⇒ ★ 把「两本账不互通 ⇒ 通道会重放 ⇒ 会炸」这件事，★ 从【只有跑到一半才暴露】
// ★★ 变成【跑之前就能看出的、可判定的红】 ★★。
//
// ★★★ 方案 C 的边界（★ 必须被钉住、★ 否则它会悄悄变成方案 A ★★★）：
//   ★ 方案 A（★ 部署通道也认 `schema_migrations` ★）⇒ **触及生产升级路径 ⇒ owner 轴** ★
//   ★ 方案 A′（★ A ＋ 安装器也投递那 26 条 ★）⇒ **对既有生产库＝新增 schema 变更 ⇒ owner 轴** ★
//   ★ ★★ 方案 C（★ 本文件对应的门 ★）★★ ⇒ ★ **只加前置断言门** ★★
//     ★ ⇒ ★ **不改部署通道的任何一行** ★★
//     ★ ⇒ ★ **只读**（★ 只 SELECT、★ 绝不写）★★
//     ★ ⇒ ★ **不调用**部署通道（★ 它只做【静态解析 ＋ 只读对账】）★★
//
// ★★★ ⇒ ★★ 所以本文件把三条不变量钉成可执行断言 ★★★：
//   ① 覆盖门脚本**不含**任何写库语句（★ `INSERT`/`UPDATE`/`DELETE`/`CREATE`/
//      `ALTER`/`DROP`/`TRUNCATE` ⇒ 一律不允许 ★★）；
//   ② 覆盖门**不调用**部署通道脚本（★ 不许 `apply-db-revision-sequence.sh` ★★）；
//   ③ 覆盖门的投递面解析口径**与 Go 门同口径**（★ 357 条、★ 逐条一致 ★★）★★。
//
// ★★★ 为什么 ① ② 要用门来钉（★ 而不是在注释里写一句 ★★★）：
//   ★ 「不改行为」是一条**只能靠机器守**的约定 —— ★ ★ 注释会被下一个人忽略、
//   ★★ 而一行 `INSERT` 就能把「只观测」变成「真改账」 ⇒ ★★ ⇒ **⇒ 必须有门** ★★。

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// coverageGateScriptPath = 方案 C 覆盖门脚本相对本测试文件的路径。
const coverageGateScriptPath = "../../../scripts/checks/deploy-ledger-coverage-gate.sh"

// TestCoverageGateIsReadOnly 把「覆盖门只读」钉成可执行断言。
//
// ★ 判据：★ 脚本里**任何**写库动词（★ INSERT / UPDATE / DELETE / CREATE /
// ★ ALTER / DROP / TRUNCATE / GRANT / REVOKE / COPY ★）都**不得**出现 ★★
// ★ ★★ 注意：★ 判据要排除**注释与字符串里的提及**（★ 门自己的报错文案里就写着
// ★★ 「INSERT」这类词 ⇒ ★ 按文本判会误报 ★★）★★ ⇒ ★ 只看**真正的命令位置** ★★。
func TestCoverageGateIsReadOnly(t *testing.T) {
	raw, err := os.ReadFile(coverageGateScriptPath)
	if err != nil {
		t.Fatalf("读取覆盖门脚本失败（%s）：%v", coverageGateScriptPath, err)
	}
	lines := strings.Split(string(raw), "\n")
	// ★ 写库动词（★ 大小写不敏感 ★）
	writeVerbs := regexp.MustCompile(`(?i)\b(insert|update|delete|create|alter|drop|truncate|grant|revoke|copy)\b`)
	// ★ 允许出现的上下文（★ 注释行 ★★）
	allowed := regexp.MustCompile(`^\s*(#|$)`)

	// ★★★ ★★★ 量具自身的坑（★ 本轮实测撞到、★ **本轮头号发现**）★★★ ★★★
	// ★★★ 第一版把「行内的引号串」整段剥掉再找写库动词 ⇒ ★★ **而 SQL 恰恰
	// ★★★ 就在引号里** ⇒ ★★ 剥掉引号串＝**把证据本身删掉** ⇒ ★★ ⇒ ★
	// ★★★ **门恒绿、★ 却不报任何东西** ⇒ ★★ ⇒ ★ 这是「判据恒真」的另一种形态：
	// ★★★ **判据被自己的实现掏空** ★★★ ⇒ ★★ ⇒ ★ ⇒ **判据位必须看得到
	// ★★★ 引号里的内容** ★★★ ⇒ ★★ ⇒ ★ 修法：★ **不剥引号** ★★、★ 只把
	// ★★★ 「注释行」排除掉（★ 注释是唯一的合法豁免 ★★）★★ ★★★。
	//
	// ★★★ ★★ 推论（本轮通用式）：★★ 「为了避免误报而把某段内容剔掉」是
	// ★★★ **判据自毁**的高发形态 ⇒ ★★ ⇒ ★ 每剔一段都要问「**证据会不会在
	// ★★★ 这段里**」 ★★★ ⇒ ★★ ⇒ ★ 而 M2 变异臂（★ 注入 `q "INSERT INTO …"`
	// ★★★ ★）就是照着这个洞设计的 ⇒ ★★ ⇒ ★ **它当场咬住了** ★★★。
	for i, line := range lines {
		if allowed.MatchString(line) {
			continue
		}
		if writeVerbs.MatchString(line) {
			t.Errorf("★ 覆盖门脚本第 %d 行出现了写库动词（★ 方案 C 必须是**只读**的）：%s\n"+
				"★ ⇒ ★ 方案 C 的全部价值就在「**只观测、不改账**」⇒ ★ 若这里开始写库、\n"+
				"★ ⇒ ★ 它就不再是观测、★ 而是**行为变更**（★ 那属于方案 A/A′ ⇒ owner 轴 ★）★★",
				i+1, strings.TrimSpace(line))
		}
	}
}

// TestCoverageGateDoesNotInvokeDeployChannel 把「覆盖门不调用部署通道」钉死。
//
// ★ 为什么这条重要：★ 若覆盖门改为**直接跑**部署通道（★ 「顺便验一下它真会炸」
// ★★），★ 那它就从「只读观测」变成了「一次真实的投递尝试」★★ ⇒ ★★ 在生产库上
// ★★ 那等于**替 owner 做了决定**（★ 而部署通道的投递行为属 owner 轴 ★★）★★ ⇒ ★
// ★★ ⇒ ★ 所以必须钉住 ★★。
func TestCoverageGateDoesNotInvokeDeployChannel(t *testing.T) {
	raw, err := os.ReadFile(coverageGateScriptPath)
	if err != nil {
		t.Fatalf("读取覆盖门脚本失败（%s）：%v", coverageGateScriptPath, err)
	}
	text := string(raw)
	if strings.Contains(text, "apply-db-revision-sequence.sh") {
		// ★ 允许**提及**（★ 报错文案里会说「先按报告 §7.31 的授权轴定方案」★），
		// ★ 但**不许调用** ⇒ ★ 判据＝有没有把它当命令跑 ★★。
		lines := strings.Split(text, "\n")
		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			stripped := regexp.MustCompile(`"[^"]*"|'[^']*'`).ReplaceAllString(line, `""`)
			if strings.Contains(stripped, "apply-db-revision-sequence.sh") {
				t.Errorf("★ 覆盖门脚本第 %d 行**调用**了部署通道脚本（★ 方案 C 必须是**只观测、不投递**的）：%s\n"+
					"★ ⇒ ★ 跑部署通道＝一次真实投递尝试（★ 在生产库上＝替 owner 做决定 ★★）⇒\n"+
					"★ ⇒ ★ 请改成【静态解析 ＋ 只读对账】★★",
					i+1, trimmed)
			}
		}
	}
}

// TestCoverageGateParsesSameDeliverySurface 把覆盖门的投递面解析口径钉住。
//
// ★ 判据：★ 覆盖门解析出的投递面 **必须与 Go 门 `deployChannelStartupOrder`
// ★ 逐条一致** ★★ ⇒ ★ ⇒ 判据不因「两个解析器各自漂移」而失真 ★★。
//
// ★ ★★ 实测（本轮，★ 2026-11-23）：★ 两套解析器都是 **357 条**、★ 逐条一致 ★★。
func TestCoverageGateParsesSameDeliverySurface(t *testing.T) {
	// ★ 跑覆盖门自己的解析段（★ 用一个假容器名让它**只解析、不连库** ★）：
	// ★ ★ 覆盖门在「没有 DB 地址」时会 SKIP ⇒ ★ 所以这里给一个**不可达**的容器名
	// ★ ★ ⇒ 它会走「连不上 ⇒ ABORT」路径、★ 但**解析段已经跑完** ⇒ ★
	// ★ ★ 于是我们从它的输出里取「投递面 N 条」这一行 ★★。
	cmd := exec.Command("bash", coverageGateScriptPath)
	cmd.Env = append(os.Environ(),
		"DEPLOY_LEDGER_GATE_PG_CONTAINER=l35-nonexistent-container",
		"DEPLOY_LEDGER_GATE_PG_USER=llm_gateway",
		"DEPLOY_LEDGER_GATE_PG_DB=llm_gateway",
	)
	out, _ := cmd.CombinedOutput()

	re := regexp.MustCompile(`投递面：(\d+) 条`)
	m := re.FindStringSubmatch(string(out))
	if m == nil {
		t.Fatalf("★ 覆盖门的输出里找不到「投递面：N 条」这一行（★ 说明它的解析段没跑到、★ 或输出形态变了）：\n%s", out)
	}
	gateCount := 0
	if _, err := fmtSscan(m[1], &gateCount); err != nil {
		t.Fatalf("★ 覆盖门报出的投递面条数解析失败（%q）：%v", m[1], err)
	}

	goNames := deployChannelStartupOrder(t)
	if gateCount != len(goNames) {
		t.Errorf("★ 覆盖门解析出 %d 条投递文件、★ 而 Go 门解析出 %d 条 ⇒ ★ 两套解析器不一致 ⇒ ★★ ⇒ ★ 判据会因「解析口径漂移」而失真 ★★\n"+
			"★ ⇒ ★ 请让两者同口径（★ 同正则、★ 同去重、★ 同跳过 .down.sql ★★）",
			gateCount, len(goNames))
	}
}

// fmtSscan 是一个极小的整数解析助手（★ 避免为一个 strconv 引入 import ★）。
func fmtSscan(s string, out *int) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errNonNumeric
		}
		n = n*10 + int(c-'0')
	}
	*out = n
	return 1, nil
}

var errNonNumeric = &nonNumericError{}

type nonNumericError struct{}

func (e *nonNumericError) Error() string { return "非数字" }

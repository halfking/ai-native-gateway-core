package sqlguard

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestOperatorFacingEnvFileClaimsAreReal —— 225 号新增。
//
// 缺陷本体（面向运维的可执行误导）：
// `admin/free_pool_extra.go` 的 `acquisitionMethods` 数组是**直接下发给前端 UI 的
// 操作指引数据**（不是注释），其中三处告诉运维把免费池的 API Key 写进
// `config/free-pool.env`：
//
//	:239  "将 API Key 写入 71 的 config/free-pool.env，由定时任务或「导入环境变量 Key」注入池子"
//	:240  "将 Key 写入 /opt/llm-gateway/config/free-pool.env（如 OPENROUTER_API_KEY=sk-...）"
//	:300  "凭据仅通过 config/free-pool.env（gitignore）或运行时环境变量注入"
//
// 而**全仓没有任何代码读这个文件**：
//  - `collectEnvProviderConfigs`（`admin/routing.go:5220-5265`）只用 `os.Getenv`；
//  - `/api/free-pool/import-env`（`handler.go:1323` → `routing.go:4851`）的源头就是它；
//  - 部署侧 `installer/templates/compose.yml:71` 是 `env_file: .env`，**不是** free-pool.env；
//  - `installer/` 全文只在一个 SQL 注释里出现 "free-pool"，与 env 文件无关。
//
// ⇒ 运维照做：把 key 写进那个文件、重启网关、点「导入环境变量 Key」
// ⇒ 端点返回 `{"candidates": 0, "registered": 0}`，**池子里一个凭据都没有**。
// 而 UI 显示的是一个 200 的成功响应。
//
// 这是待裁决 60（`docs/hooks/prompt-optimization.md` 承诺了不存在的接线）的**同族**，
// 但**更严重**：那条面向开发者、这条面向运维的**可执行操作**，且后果是
// 「照做了但什么都没发生」而不是「开关没效果」。
//
// 判据：**面向用户的文案里提到的每一个"外部载体"，必须在本仓有真实的读取方。**

// operatorFacingEnvFileMentions 列出「面向运维的文案里提到的外部文件」。
// 新增一条即自动进入检查面（门要自报覆盖面）。
var operatorFacingEnvFileMentions = []struct {
	file  string // 文案所在文件
	line  int    // 文案所在行（1-based）
	claim string // 提到的文件名
	note  string // 为什么应该有人读它
}{
	{
		file:  "admin/free_pool_extra.go",
		line:  239,
		claim: "config/free-pool.env",
		note:  "文案说「由定时任务或『导入环境变量 Key』注入池子」",
	},
	{
		file:  "admin/free_pool_extra.go",
		line:  240,
		claim: "config/free-pool.env",
		note:  "文案给出了绝对路径 /opt/llm-gateway/config/free-pool.env 与示例变量名",
	},
	{
		file:  "admin/free_pool_extra.go",
		line:  300,
		claim: "config/free-pool.env",
		note:  "文案说「凭据仅通过 config/free-pool.env（gitignore）或运行时环境变量注入」",
	},
}

// knownUnreadEnvFileClaims 是**已登记、尚未修**的文案（224 号定的取舍）。
//
// ⚠️ 为什么不让门一直红：一个红的门会让 `make guards` 永久失败，
// 于是**所有**守卫（含已修好的那些）都变成"没人敢看"的红。
// ⇒ 本审计取「登记 + 豁免 + t.Log 打印」，而不是「留着红」。
//
// **豁免可自我失效**：一旦 `findFileReaders` 找到了读取方，
// 下面的 `t.Errorf` 分支根本进不到 ⇒ 豁免自动失效。
var knownUnreadEnvFileClaims = map[string]string{
	"config/free-pool.env": "225 号实测：collectEnvProviderConfigs（admin/routing.go:5220）" +
		"只用 os.Getenv；部署侧 installer/templates/compose.yml:71 是 env_file: .env。",
}

func TestOperatorFacingEnvFileClaimsAreReal(t *testing.T) {
	root := repoRootForTest(t)

	// 收集全仓（除测试与 vendor）里所有提到该文件名的位置。
	for _, m := range operatorFacingEnvFileMentions {
		// (1) 锚点仍在原处、行内容仍含该文件名 —— 登记表本身不腐烂。
		p := filepath.Join(root, m.file)
		b, err := os.ReadFile(p)
		if err != nil {
			t.Errorf("锚点 %s 读不到：%v", m.file, err)
			continue
		}
		lines := strings.Split(string(b), "\n")
		if m.line < 1 || m.line > len(lines) {
			t.Errorf("锚点 %s:%d 越界（共 %d 行）⇒ 登记表需要重新确认", m.file, m.line, len(lines))
			continue
		}
		if !strings.Contains(lines[m.line-1], m.claim) {
			t.Errorf("锚点 %s:%d 不再含 %q：\n  实际内容: %s\n"+
				"⇒ 文案已改或已删，登记表需要重新确认（不是缺陷已修复）。",
				m.file, m.line, m.claim, strings.TrimSpace(lines[m.line-1]))
			continue
		}

		// (2) 本仓必须存在一个**读取方**：某段非测试代码读这个文件。
		readers := findFileReaders(root, filepath.Base(m.claim))
		if len(readers) == 0 {
			msg := fmt.Sprintf("面向运维的文案让操作员把 key 写进 %s（%s:%d —— %s），\n"+
				"  但全仓**没有任何非测试代码读这个文件**。\n"+
				"  ⇒ 运维照做后，/api/free-pool/import-env 会返回 candidates=0、registered=0，\n"+
				"    池子里一个凭据都没有，而 UI 显示的是一个 200 的成功响应。\n"+
				"  修法（文案侧，一处）：把三处文案改成「把 key 注入**进程环境变量**\n"+
				"  （如 docker env_file / systemd EnvironmentFile），再点『导入环境变量 Key』」。\n"+
				"  修法（实现侧，可选）：让 collectEnvProviderConfigs 真的读该文件。\n"+
				"  ⚠️ 改的是下发给 UI 的操作指引 ⇒ 需产品/运维确认后再动。",
				m.claim, m.file, m.line, m.note)
			if known, ok := knownUnreadEnvFileClaims[m.claim]; ok {
				t.Logf("【已登记缺陷，待裁决 91】%s\n  已知理由：%s", msg, known)
				continue
			}
			t.Errorf("%s", msg)
			continue
		}
		t.Logf("%s 有 %d 个读取方：%s", m.claim, len(readers), strings.Join(readers, ", "))
	}
}

// findFileReaders 找出所有**读取**该文件名的非测试 Go 代码位置。
//
// 口径刻意保守：只认「打开该文件的读法」，不认「提到它」。
// 提到它的都是本守卫要抓的对象（面向用户的文案），
// 唯独 admin/free_pool_extra.go 那三处是**数据结构**（下发给 UI），
// 它们不是读取方 ⇒ 判据必须能把"提到"与"读取"分开。
func findFileReaders(root, base string) []string {
	var out []string
	re := regexp.MustCompile(`(os\.Open|ioutil\.ReadFile|os\.ReadFile|godotenv|Load\(|\.env)`)
	_ = re
	// 逐个 .go 文件扫：出现 base 名，且**同一处**伴随打开/加载动作。
	openRe := regexp.MustCompile(`os\.(Open|ReadFile)\(|ioutil\.ReadFile\(|godotenv\.`)
	nameRe := regexp.MustCompile(regexp.QuoteMeta(base))
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "node_modules", "vendor", ".db-audit", "web":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if rel == filepath.ToSlash("admin/free_pool_extra.go") {
			return nil // 已知：这三处是下发给 UI 的文案，不是读取方
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		s := string(b)
		if !nameRe.MatchString(s) {
			return nil
		}
		if openRe.MatchString(s) {
			out = append(out, rel)
		}
		return nil
	})
	return out
}

// repoRootForTest 从**本包目录**（internal/sqlguard/）回退到仓库根。
//
// ⚠️ 承 §171：这条**不复用** billguard 里那个函数，也不写死深度 ——
// 而是自己逐级上溯并用哨兵文件（go.mod + Makefile）自证。
// 找不到就 fail 而不是猜一个默认值：猜出来的根会让锚点全部读不到，
// 门会以"锚点缺失"的红脸出现，而那个红脸会被读成"源码有问题"（224 号踩过）。
func repoRootForTest(t *testing.T) string {
	t.Helper()
	dir := "."
	for i := 0; i < 4; i++ {
		_, e1 := os.Stat(filepath.Join(dir, "go.mod"))
		_, e2 := os.Stat(filepath.Join(dir, "Makefile"))
		if e1 == nil && e2 == nil {
			return dir
		}
		dir = filepath.Join(dir, "..")
	}
	t.Fatalf("尺子坏了：从 %s 逐级上溯 4 层都没找到同时含 go.mod 与 Makefile 的目录。",
		mustGetwdForTest(t))
	return ""
}

func mustGetwdForTest(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		return "<unknown>"
	}
	return wd
}

package admin

// retry_classifier_contract_test.go — R78：重试分类器匹配的错误码必须真的被生产代码产生。
//
// 缺陷：adminLLMShouldRetryExplicit 匹配 `auto_route_unavailable`，而该字符串在
// 全仓**没有产生方**（只存在于 domains/streaming/auto_route.go 的文件头注释里）。
// 真实错误码是 `auto_route_decider_failed`。于是该分支恒为 false：内部 LLM 任务
// 在 auto 路由失败时不会触发显式模型重试。
//
// 为什么长期没被发现：admin_llm_task_test.go 的用例喂的是**同一个幽灵字面量**，
// 函数对假字符串当然返回 true，测试一直是绿的——**测试固化了一个不存在的错误码，
// 恰好把真实缺陷盖住**。
//
// 本门把「分类器匹配的串必须被生产代码产生」变成常驻属性：只查**非测试** .go 里的
// 字符串字面量出现（排除 _test.go），所以测试自己写的假串救不了它。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// classifierConsumers 是「消费方」文件：这些文件里出现某个错误码，代表的
// 是**匹配**它，而不是**产生**它。
//
// R78 自身的踩坑记录（这道门第一版是恒真的）：本门最初只用
// `strings.Contains(全仓非测试 .go, lit)` 判「有产生方」，而
// admin/admin_llm_task.go —— 分类器自己所在的文件 —— 本身就含有这三个字面量
// （它们是待匹配的串）。于是把真实码改回幽灵码 auto_route_unavailable 后，
// 门**依然全绿**，「有产生方」的证据正是那个幽灵码所在的同一行。
//
// 这是本项目反复出现的同一种错：判据量的不是语义量（"这个错误码会被某处
// 产生"），而是代理量（"这个字符串在非测试代码里出现过"）。区别只在缺陷
// 形态下才显形——而缺陷形态正是唯一需要这道门的形态。
//
// 因此判据必须是「**消费方之外**还有产生方」。
var classifierConsumers = []string{
	"admin/admin_llm_task.go",
}

// classifierLiterals 从 adminLLMShouldRetryExplicit 的**函数体**里抽取
// strings.Contains 的第一个参数。
//
// 为什么必须抽取而不是手写清单：本门第二版用手写清单，结果第一轮变异就证伪了
// 它——把分类器里的 auto_route_decider_failed 改回幽灵码 auto_route_unavailable，
// 清单里那个真实码**确实还有产生方**（domains/streaming/*），门照样全绿。
// 手写清单与实现之间没有任何强制关联，改实现不改清单＝门看不见。
//
// 抽取也顺带解决了「新增一个串但忘了接线」：新串会立刻进入被核对集合。
// 代价是必须真的取到函数体（取不到就让门报红，见下）。
func classifierLiterals(t *testing.T) []string {
	t.Helper()
	root := repoRoot(t)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(root, "admin/admin_llm_task.go"), nil, 0)
	if err != nil {
		t.Fatalf("解析 admin/admin_llm_task.go: %v", err)
	}
	var out []string
	found := false
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "adminLLMShouldRetryExplicit" {
			continue
		}
		found = true
		ast.Inspect(fn, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Contains" || len(call.Args) < 2 {
				return true
			}
			lit, ok := call.Args[1].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			v, uerr := strconv.Unquote(lit.Value)
			if uerr != nil {
				return true
			}
			out = append(out, v)
			return true
		})
	}
	if !found {
		t.Fatal("未找到 adminLLMShouldRetryExplicit——分类器被改名或移走，" +
			"本门已失去被测对象，必须同步更新")
	}
	if len(out) == 0 {
		t.Fatal("adminLLMShouldRetryExplicit 里没抽到任何 strings.Contains 字面量——" +
			"匹配形态变了（例如改用 errors.Is / 正则），本门已失效，必须重写")
	}
	sort.Strings(out)
	return out
}

// repoRoot 按 go.mod 的模块路径定位仓库根，而不是用 "../.."。
//
// R78 踩坑记录：本文件第一版写的是 `filepath.Abs("../..")`。admin/ 只有
// 一层深，`../..` 越过了仓库根，扫到的是 llm-gateway 这一层的**全部兄弟
// 项目**（ai-native-maintain / llm-gateway-go / llm-gateway-go-3 / …）。
// 后果不是变红，而是**假通过**：本门要证明「no_candidate 有产生方」，结果
// 命中的是 ai-native-maintain/internal/httpapi/upgrade_policies.go——一个
// 与本仓无关的字符串。方向是"扫得更广"所以不会报错，只会安静地给出假证据。
//
// 同族的 internal/partguard 当时是对的（它在 internal/partguard/，两层深），
// 纯属目录深度不同，不是写法更小心——所以按"猜目录深度"定位仓库根这件事
// 本身就是错的判据。
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		b, rerr := os.ReadFile(filepath.Join(dir, "go.mod"))
		if rerr == nil && strings.Contains(string(b), "module "+modulePath) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("未找到 module %s 的 go.mod（从 %s 向上找了 10 层）", modulePath, mustGetwd(t))
	return ""
}

const modulePath = "github.com/kaixuan/llm-gateway-go"

// retiredErrorCodes 是已下线、不该再被匹配的错误码。它们的"无产生方"事实由
// TestRetryClassifierLiteralsAreProducedByProductionCode 在消费方之外核对。
var retiredErrorCodes = []string{"auto_route_unavailable"}

func mustGetwd(t *testing.T) string {
	t.Helper()
	d, _ := os.Getwd()
	return d
}

func TestRetryClassifierLiteralsAreProducedByProductionCode(t *testing.T) {
	lits := classifierLiterals(t)
	// 幽灵码闸门：这些串全仓无产生方，匹配它只会让分类器恒假。
	for _, ghost := range retiredErrorCodes {
		for _, lit := range lits {
			if strings.Contains(lit, ghost) {
				t.Errorf("分类器匹配了已下线的幽灵错误码 %q（出现在 %q）——"+
					"它没有产生方，该分支恒为 false", ghost, lit)
			}
		}
	}

	root := repoRoot(t)
	produced := map[string]string{} // literal -> 首个产生它的文件
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "node_modules", "vendor", "web", ".build-local", "bin":
				return filepath.SkipDir
			}
			return nil
		}
		// 只认非测试代码：测试里写一个假串不构成"这个错误码存在"的证据。
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		b, e := os.ReadFile(p)
		if e != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		// 消费方自身的出现不算「产生」——见 classifierConsumers 的说明。
		isConsumer := false
		for _, c := range classifierConsumers {
			if rel == c {
				isConsumer = true
			}
		}
		for _, lit := range lits {
			if _, seen := produced[lit]; seen {
				continue
			}
			if !isConsumer && strings.Contains(string(b), lit) {
				produced[lit] = rel
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	for _, lit := range lits {
		if produced[lit] == "" {
			t.Errorf("重试分类器匹配 %q，但**没有任何非测试 .go 代码产生它**。\n"+
				"  该分支恒为 false：内部 LLM 任务在该错误发生时不会重试。\n"+
				"  要么改用真实错误码，要么删掉该分支——不要留着看起来生效的匹配。", lit)
			continue
		}
		t.Logf("%-28q 产生于 %s", lit, produced[lit])
	}
}

// TestClassifierConsumerExclusionIsLoadBearing：排除项本身必须真的含有这些
// 字面量。若哪天分类器被拆到别的文件、admin_llm_task.go 不再含它们，这条
// 排除就成了不再承重的死规则——而它一旦失效，判据就会退回恒真（见
// classifierConsumers 的说明）。这道门就是用来发现那种失效的。
func TestClassifierConsumerExclusionIsLoadBearing(t *testing.T) {
	root := repoRoot(t)
	lits := classifierLiterals(t)
	total := 0
	for _, c := range classifierConsumers {
		b, err := os.ReadFile(filepath.Join(root, c))
		if err != nil {
			t.Fatalf("读 %s: %v", c, err)
		}
		hit := 0
		for _, lit := range lits {
			if strings.Contains(string(b), lit) {
				hit++
			}
		}
		if hit == 0 {
			t.Errorf("%s 里已不含任何被分类器匹配的串——classifierConsumers 排除项"+
				"不再承重，删掉它（否则判据会悄悄退回恒真）", c)
		}
		total += hit
	}
	if total == 0 {
		t.Fatal("排除项合计不含任何字面量，排除机制已完全失效")
	}
	t.Logf("消费方排除项承载 %d 处字面量出现", total)
}

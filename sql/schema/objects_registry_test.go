// Round 43: sql/objects/ 的定位与依赖守卫。
//
// 调查问题（连续两轮挂在待办里）：sql/objects/ 到底是 SSOT、还是历史归档？
// 结论是**两者都是，且这个双重身份正是风险来源**：
//
//  1. 对**静态分析门**它是 SSOT（承重）。
//     internal/dbx/jsonb_param_static_test.go 用正则解析
//     sql/objects/tables/*.sql 构建「每表已知 jsonb 列」映射，
//     驱动整个包的静态 lint；admin/providers_schema_contract_test.go 直接
//     读 sql/objects/tables/providers.sql 当契约。
//     若该目录消失，jsonbReCreateTable 匹配不到任何表，列映射退化为空，
//     **静态 lint 会静默退化成「什么都检查不到」而依然全绿** —— 典型假绿。
//
//  2. 对**部署**它是惰性的。
//     全仓唯一引用是 deploy/sql/sync-objects.sh，它只做单向文件拷贝
//     （sql/objects → deploy/sql/objects）并写 README 让人手动 psql -f。
//     没有任何流水线会 apply 它；而且 deploy/sql/objects/ 目录根本不存在，
//     说明该脚本从未被运行过。
//
//  3. 它还是**部分基线对象的唯一出处**：本轮对账里那 5 个
//     「不在源库、无迁移可重建」的对象，在本目录里都有逐对象 DDL。
//
// 于是 schema 在仓里有**四种表示**（迁移目录 / 01-schema 三份副本 /
// sql/objects / deploy/sql/objects 镜像），而只有前两种有功能路径。
// 本守卫至少守住第 1 条（承重那条），不让它被当成可清理的冗余目录删掉。
package schema

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const objectsRoot = "../../sql/objects"

// TestSqlObjectsIsPresentForStaticAnalysisGates pins the load-bearing role.
// The failure mode this prevents is subtle and silent: with the directory gone,
// the jsonb column map is empty, the static lint checks nothing, and the suite
// still reports ok.
func TestSqlObjectsIsPresentForStaticAnalysisGates(t *testing.T) {
	for _, sub := range []string{"tables", "indexes", "constraints", "views", "functions"} {
		dir := filepath.Join(objectsRoot, sub)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Errorf("sql/objects/%s 不可读：%v\n"+
				"  该目录是 internal/dbx 静态 jsonb lint 的列映射来源；"+
				"删除它会让该 lint 静默退化成「零检查」而依然全绿", sub, err)
			continue
		}
		if len(entries) == 0 {
			t.Errorf("sql/objects/%s 为空", sub)
		}
	}
}

// TestJsonbLintParsesObjectTables binds the dependency explicitly: the lint
// must keep reading sql/objects/tables/*.sql. If someone "cleans up" the
// static analysis to read the baseline or the live database instead, this
// fires so the change is deliberate — a different column source changes what
// the lint can see.
func TestJsonbLintParsesObjectTables(t *testing.T) {
	b, err := os.ReadFile("../../internal/dbx/jsonb_param_static_test.go")
	if err != nil {
		t.Fatalf("read jsonb static test: %v", err)
	}
	src := string(b)
	if !strings.Contains(src, "sql/objects/tables/") {
		t.Error("internal/dbx 的静态 jsonb lint 不再从 sql/objects/tables/*.sql 取列映射；" +
			"若这是有意改用别的来源，请同步更新 TestSqlObjectsIsPresentForStaticAnalysisGates " +
			"并复核该 lint 的检查面是否变窄")
	}
	// The map must be built by actually reading those files, not hardcoded.
	//
	// This used to be a t.Log "please go look" note. That is a no-op: it never
	// fails, it prints nothing when the pattern does match (which it did), and
	// the surrounding report then counted it as a passing guard. A note is not
	// a guard. The assertion below is on the concrete thing that matters — the
	// directory the map is built from — and it has a real discriminator: a
	// sibling object directory is rejected.
	if m := regexp.MustCompile(`filepath\.Join\([^)]*"sql"[^)]*"objects"[^)]*"tables"`).
		FindString(src); m == "" {
		t.Errorf("internal/dbx 未用 filepath.Join(objectsRoot, \"tables\") 确定列映射目录；\n" +
			"  jsonb 列映射必须由 sql/objects/tables/*.sql 的文件内容解析而来。\n" +
			"  若这是有意改用别的来源，请同步更新本文件顶部的定位结论并复核检查面是否变窄。\n" +
			"  （旧版本这里只有一句 t.Log 提示，从不失败。）")
	}
}

// TestObjectsDirIsNotAppliedAnywhere records the negative fact that matters for
// baseline work: sql/objects/ is NOT part of any deployment path. The only
// reference is a one-way file copy whose target directory does not exist.
//
// This matters because it is easy to assume "the object files must be applied
// somewhere" and to treat them as a migration source. They are not. A baseline
// rebuilt from them would carry objects that no pipeline has ever applied.
func TestObjectsDirIsNotAppliedAnywhere(t *testing.T) {
	b, err := os.ReadFile("../../deploy/sql/sync-objects.sh")
	if err != nil {
		t.Skipf("sync-objects.sh 不可读，跳过；这不构成「sql/objects 未被应用」的证据: %v", err)
	}
	src := string(b)
	if !strings.Contains(src, "cp -f") && !strings.Contains(src, "rsync") {
		t.Error("sync-objects.sh 不再是纯拷贝；若它开始 apply 对象，" +
			"本守卫的结论（objects 对部署惰性）需重估")
	}
	// And the copy target is absent, i.e. the mirror was never materialised.
	if _, err := os.Stat("../../deploy/sql/objects"); err == nil {
		t.Log("deploy/sql/objects 现已存在；该镜像与 sql/objects 的漂移需要单独守卫")
	} else {
		t.Log("deploy/sql/objects 不存在 —— 同步脚本从未产出过镜像（与「objects 对部署惰性」一致）")
	}
}

// TestReconcileToolSupportsThirdRepresentation runs the reconcile tool in its
// three-way mode against the real directories.
//
// This is an execution test rather than a text test on purpose: the tool
// previously only handled two file arguments, and the third-representation
// path was added late. A text assertion would have passed while the tool
// crashed on the first real run — the same shape as the audit-script
// ERRFILE bug. Its exit code and report content are what matter.
// pythonInterp 是一个**已通过活性探针**的解释器。
type pythonInterp struct {
	name string
	args []string // `py -3` 这类需要前置参数的形式
}

// pythonCandidates 按优先级排列。`python3` 排第一是为了兼容 Linux/macOS 与
// 虚拟环境（那里它才是真解释器）；在 Windows 上它通常是 App Execution Alias
// 桩，会被活性探针否掉并顺延到 `python`。
var pythonCandidates = []pythonInterp{
	{name: "python3"},
	{name: "python"},
	{name: "py", args: []string{"-3"}},
}

// firstLivePython 逐个候选做**活性**探针，返回第一个真能执行 `-c pass` 的。
//
// 关键点：探针**不查磁盘上有没有这个文件**，只问「能不能跑起来」。
// 208 号那一版的教训正是这两件事在 Windows 上会分叉（App Execution Alias 桩
// 两者都满足前者、不满足后者）。
func firstLivePython(cands []pythonInterp) (pythonInterp, error) {
	var tried []string
	for _, c := range cands {
		argv := append(append([]string{}, c.args...), "-c", "pass")
		err := exec.Command(c.name, argv...).Run()
		if err == nil {
			return c, nil
		}
		tried = append(tried, fmt.Sprintf("%s(%v)", c.name, err))
	}
	return pythonInterp{}, fmt.Errorf("无一可执行：%v", tried)
}

// TestFirstLivePythonControls 是上面那道活性判据的反向对照。
//
// 为什么要单独钉：判据一旦写错，方向有两个 ——
//
//	① **过度跳过**：把活的解释器判成死的 ⇒ 门永远不跑，静默失效（本轮要消灭的正是这个）；
//	② **过度接受**：把桩/坏命令判成活的 ⇒ 回到 208 号的硬红，且报错形态是
//	   「工具坏了」，把人引向错误方向。
//
// 只测 ② 会让修复看起来有效；只测 ① 会让门自己躺平。两条都要。
func TestFirstLivePythonControls(t *testing.T) {
	// 负控 1（过度跳过方向）：候选里**混进**一个必然不存在的命令，
	// 但只要有一个活的，就必须被选中，而不是因为前面失败就整体放弃。
	//
	// 正样本**不能硬编码名字**：最初写死的是 `python`，它在作者机
	// （Windows，`python` = 真解释器 3.13.9）上是活的，但在只有 `python3`
	// 的 macOS/Linux 上不存在 ⇒ 这条对照在那类机器上是**永久硬红**，
	// 红得只能被当环境噪音忽略——正是 R89-DV 批判过的「把门变成噪音」。
	// 活样本改由生产候选清单现场探针取得（与 TestReconcileToolSupports
	// ThirdRepresentation 同一判据源）：本机一个活解释器都没有时响亮跳过，
	// 并声明该跳过不构成任何「firstLivePython 正确」的证据。
	live, err := firstLivePython(pythonCandidates)
	if err != nil {
		t.Skipf("本机没有任何可执行的 Python（%v）——负控 1 需要一个活解释器当正样本。"+
			"⚠️ 该跳过**不是**「死候选不毒化列表」已获验证的证据。", err)
	}
	got, err := firstLivePython([]pythonInterp{
		{name: "definitely-not-a-real-python-xyz"},
		live,
	})
	if err != nil {
		t.Fatalf("候选里明明有可用解释器却整体报错：%v", err)
	}
	if got.name != live.name {
		t.Errorf("选中 %q，期望跳过死候选后选中 %q", got.name, live.name)
	}

	// 负控 2（过度接受方向）：全部候选都死 ⇒ 必须返回错误，**不得**返回一个「可用」解释器。
	// 这条钉住「不存在 / 不可执行的名字绝不能被当成活的」。
	if _, err := firstLivePython([]pythonInterp{
		{name: "definitely-not-a-real-python-xyz"},
		{name: "also-not-real-abc"},
	}); err == nil {
		t.Error("全部候选不可执行时却返回了 nil error：会把死命令当成活解释器用")
	}

	// 负控 3：空候选列表必须报错，不得 panic 也不得返回零值解释器。
	if _, err := firstLivePython(nil); err == nil {
		t.Error("空候选列表返回了 nil error")
	}

	// 负控 4：`py -3` 这类**带前置参数**的形式，探针必须把参数一起带上，
	// 否则探针自己会因缺参失败、把一个可用解释器误判成死的（就是①）。
	// 本机 `py` 可用（`py -3 --version` 成功），用它做这条对照。
	if got, err := firstLivePython([]pythonInterp{{name: "py", args: []string{"-3"}}}); err != nil {
		t.Logf("本机无 `py -3`，跳过负控 4（不影响前三条）：%v", err)
	} else if got.name != "py" {
		t.Errorf("选中 %q，期望 py", got.name)
	}
}

func TestReconcileToolSupportsThirdRepresentation(t *testing.T) {
	// R89-DT（208 号）：原来这里用 `exec.LookPath("python3")` 判「工具可用」。
	// 那是**存在性**判据，不是**可用性**判据，而它在 Windows 上恰好最不可靠：
	// 系统自带 App Execution Alias 桩 `python3.exe` **在磁盘上存在** ⇒
	// LookPath 成功 ⇒ 不走下面的跳过分支 ⇒ 随后 exec 以 **exit 9009**
	// （命令未真正安装）失败 ⇒ 本门以「工具坏了」的形态硬红。
	//
	// 也就是说：**作者写下的优雅降级（工具不可用就跳过，且明确声明
	// 「本跳过不构成该工具可用的证据」）在 Windows 上被完全击穿。**
	//
	// R89-DV（209 号）再进一步：208 号把硬红改成了跳过，但**只换成了
	// 一个硬编码的 `python3`**。在本机实测：那个 `python3` 是桩（exit 9009），
	// 而**真实解释器存在且可用**（`python` = Python 3.13.9）。
	// ⇒ 208 号那道门在本机与 Windows CI 上是**永久静默跳过**的：
	// 红变成不红了，但它同时也永远不会绿着跑——比硬红更坏，因为硬红会被人看见。
	//
	// 改法：按候选列表逐个做**活性**探针（判据与被测量对象同源 ——
	// 我们要的是「这个解释器能执行」，不是「有个叫 python3 的文件」），
	// 取第一个真的能跑起来的。判据与被测量对象同源，也与 CI 的 windows 矩阵对齐。
	interp, err := firstLivePython(pythonCandidates)
	if err != nil {
		t.Skipf("候选 %v 里没有可执行的 Python（%v）；跳过。"+
			"⚠️ 这**不是**「三方对账无漂移」的证据 —— 本门在本机从未真正运行过。",
			pythonCandidates, err)
	}
	tool := "../../scripts/audit/baseline-reconcile.py"
	if _, err := os.Stat(tool); err != nil {
		t.Fatalf("reconcile tool missing: %v", err)
	}

	argv := append([]string{interp.name, tool,
		"--committed", "../../sql/schema/01-schema.sql",
		"--generated", "../../sql/schema/01-schema.sql", // self-compare: must be a clean zero-drift run
		"--objects-dir", objectsRoot,
	}, interp.args...)
	cmd := exec.Command(argv[0], argv[1:]...)
	// R89-DV（209 号）：必须钉死子进程 stdout 编码。
	//
	// 实测：本工具的中文小节标题（`## 第三份表示：…`）随**区域设置**编码输出 ——
	// 在中文 Windows 上 stdout 走 cp936(GBK)。而下面的断言是 **UTF-8 字面量**。
	// ⇒ GBK 字节永远匹配不上 UTF-8 字面量 ⇒ 本门以
	//    「未输出第三份表示（sql/objects）的对比小节」**硬红**。
	//
	// 这个报错形态极具误导性：它把「输出被按 GBK 编码」说成
	// 「工具根本没统计第三份表示」，于是读者会去查对账逻辑、查 objects 目录，
	// 而**真实原因只是控制台代码页**。（实际输出里那行标题一直都在。）
	// ⇒ 这与 208 号 F4 是同一类：**报错形态把人引向错误的根因**。
	//
	// 判据必须与环境无关：显式设 PYTHONIOENCODING，不依赖「这台机器的
	// 默认编码恰好是 UTF-8」。
	cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("reconcile tool failed: %v\n%s", err, out)
	}
	s := string(out)
	// Self-comparison must report zero in every category, otherwise the
	// comparison itself is unsound.
	for _, want := range []string{
		"ADDED             : 0",
		"MISSING           : 0",
		"REORDERED         : 0",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("同一文件自比应为零漂移，缺 %q；实际输出：\n%s", want, s)
		}
	}
	// And the third representation must actually be counted, not silently 0.
	if !strings.Contains(s, "第三份表示") {
		t.Error("未输出第三份表示（sql/objects）的对比小节")
	}
}

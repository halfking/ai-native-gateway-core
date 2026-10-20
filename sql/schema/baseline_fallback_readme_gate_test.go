package schema

// baseline_fallback_readme_gate_test.go —— 「基线形态对象不得被 README 指示 apply」门
//
// # 这道门补的是什么
//
// `sql/objects/views/` 里有 5 个对象是**基线形态**（v1 fallback 体）：
// 基线 schema 是**逐语句**应用的，而 `CREATE VIEW` 会**急切校验**它引用的关系
// ⇒ 基线时点 `public.session_turns_hot` 还不存在 ⇒ 源文件**只能**写 v1 臂；
// 部署形态由启动序列的**切换迁移**（883 / 884）换上。
//
// ⇒ 这些文件**单独 apply 必然 42P01**（v1 族已退役）。
// 而 `deploy/sql/sync-objects.sh` 生成的 `deploy/sql/objects/views/README.md`
// 里有一段**通用**的 `psql "$DATABASE_URL" -f .../<name>.sql` 应用指令
// ⇒ **照文档操作就会炸**（2026-10-21 R65 轮实测）。
//
// # 为什么是**执行门**而不是文本门
//
// 这道门**真跑** `sync-objects.sh`（在隔离临时树里），再**真跑**生成的 README 里
// 那段 apply-all 循环（`psql` 换成只回显的桩），最后断言两件事：
//
//	① 那 5 个基线形态对象**一次都没有被 apply**（跑出来的命令里不含它们）；
//	② **非**基线形态的对象**确实被 apply 了**（非空下限）——
//	   否则「循环什么都没做」也会是绿的（恒真空转）。
//
// ★ 文本断言（「README 里必须出现某个字符串」）挡不住「字符串在，但循环照样 apply 它们」
//   这种形态；而本门能挡：它看的是**命令实际作用到哪些文件**。
//
// ★ 门还断言**双向**：没带标记的对象**不得**出现在「基线形态对象」小节里
//   （否则「所有对象都被标成基线形态」也是一种绿）。

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// baselineFallbackMarkerRE 匹配「本对象是基线形态 fallback 体」这一行声明。
//
// ⚠ 必须与 deploy/sql/sync-objects.sh 里的识别口径**一致**：
//
//	标记后必须跟空白或行尾（否则会误匹配 `...-fallback-switch:` 那一行）。
var baselineFallbackMarkerRE = regexp.MustCompile(`(?m)^--[ \t]*dbinit:baseline-v1-fallback(?:[ \t]|$)`)

// baselineFallbackSwitchRE 匹配「哪个迁移把它换成部署形态」。
var baselineFallbackSwitchRE = regexp.MustCompile(`(?m)^--[ \t]*dbinit:baseline-v1-fallback-switch:[ \t]*(\S+)`)

// objectTypes 与 sync-objects.sh 的同步顺序一致。
var objectTypes = []string{
	"tables", "views", "functions", "sequences", "triggers",
	"indexes", "constraints", "policies", "other",
}

// markedObjects 返回「声明了基线形态 fallback 体」的对象：类型 → 文件名集合。
func markedObjects(t *testing.T, root string) map[string]map[string]bool {
	t.Helper()
	out := map[string]map[string]bool{}
	for _, ot := range objectTypes {
		dir := filepath.Join(root, "sql", "objects", ot)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("读不到 %s：%v", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
				continue
			}
			b, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatalf("读不到 %s：%v", e.Name(), err)
			}
			if !baselineFallbackMarkerRE.Match(b) {
				continue
			}
			if out[ot] == nil {
				out[ot] = map[string]bool{}
			}
			out[ot][e.Name()] = true
			// ★ 声明必须给出切换迁移，否则「勿单独应用」只是空话
			//   （没有切换迁移 = 部署形态无从谈起 = 没有任何形态可用）。
			if !baselineFallbackSwitchRE.Match(b) {
				t.Errorf("%s/%s 声明了「基线形态 fallback 体」但**没有** `dbinit:baseline-v1-fallback-switch:` 行\n"+
					"  ⇒ 部署形态由哪个迁移换上无从查证 ⇒ 请补上切换迁移名。", ot, e.Name())
			}
		}
	}
	return out
}

// copyTree 把 src 目录整棵树复制到 dst。
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = in.Close() }()
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		out, err := os.Create(target)
		if err != nil {
			return err
		}
		defer func() { _ = out.Close() }()
		_, err = io.Copy(out, in)
		return err
	})
	if err != nil {
		t.Fatalf("复制 %s → %s 失败：%v", src, dst, err)
	}
}

// copyFile 复制单个文件。
func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatalf("读不到 %s：%v", src, err)
	}
	defer func() { _ = in.Close() }()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = out.Close() }()
	if _, err := io.Copy(out, in); err != nil {
		t.Fatalf("复制 %s → %s 失败：%v", src, dst, err)
	}
}

// bashFencedBlocks 抽出 Markdown 里的 ```bash 围栏代码块。
func bashFencedBlocks(md string) []string {
	var out []string
	lines := strings.Split(md, "\n")
	inBlock := false
	var cur []string
	for _, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		if !inBlock && strings.HasPrefix(trimmed, "```bash") {
			inBlock = true
			cur = nil
			continue
		}
		if inBlock && strings.HasPrefix(trimmed, "```") {
			inBlock = false
			out = append(out, strings.Join(cur, "\n"))
			continue
		}
		if inBlock {
			cur = append(cur, ln)
		}
	}
	return out
}

// TestBaselineFallbackObjectsAreNotAppliedByGeneratedReadme 是本轮的核心门：
// 生成 README → 执行它的 apply-all 循环 → 断言基线形态对象一次都没被 apply。
func TestBaselineFallbackObjectsAreNotAppliedByGeneratedReadme(t *testing.T) {
	root := filepath.Join("..", "..")

	// ── ① 非空下限：至少有一个对象声明了「基线形态 fallback 体」 ──
	//    否则「这套机制根本没启用」也是绿的（恒真空转）。
	marked := markedObjects(t, root)
	markedTotal := 0
	for _, m := range marked {
		markedTotal += len(m)
	}
	if markedTotal == 0 {
		t.Fatal("全仓没有任何对象声明 `dbinit:baseline-v1-fallback` ⇒ 这道门无从验证\n" +
			"  （若确实已无基线形态对象，请把本门与源文件里的声明一起删掉，而不是留着空门）")
	}
	t.Logf("声明了基线形态 fallback 体的对象：%d 个（分布：%s）", markedTotal, describeMarked(marked))

	// ── ② 在隔离临时树里真跑生成器 ──
	//
	// ⚠ 只复制**含标记对象的那些类型**（当前只有 views，52 个文件）——
	//   全量复制是 1767 个文件，而生成器的拷贝是「每个文件起一个 cp 进程」
	//   ⇒ 全量跑一次要 30 秒以上，把这道门拖成慢门。
	//   ⇒ 本门关心的是「被标记的那几个对象」在 README 里怎么处理，
	//   其余类型不参与断言（不复制 ⇒ 生成器跳过它们 ⇒ 不产出 README）。
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "sql"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tmp, "deploy", "sql"), 0o755); err != nil {
		t.Fatal(err)
	}
	var typesToCheck []string
	for _, ot := range objectTypes {
		if len(marked[ot]) == 0 {
			continue
		}
		typesToCheck = append(typesToCheck, ot)
		copyTree(t, filepath.Join(root, "sql", "objects", ot), filepath.Join(tmp, "sql", "objects", ot))
	}
	scriptSrc := filepath.Join(root, "deploy", "sql", "sync-objects.sh")
	scriptDst := filepath.Join(tmp, "deploy", "sql", "sync-objects.sh")
	copyFile(t, scriptSrc, scriptDst)

	// ★ 外部命令必须**有界 ctx**（R65 轮 606.375s 挂起归因的同族教训）：
	//   无界的 exec.Command 一旦卡住，得到的**不是「红」而是「挂起」** ——
	//   挂到 go test 的 10 分钟默认超时才退出，而且挂起会**掩盖别的红**
	//   （那轮挂起下藏着另一个文件的 3 条从未执行到的红）。
	//   ⇒ 这里给 2 分钟上限，并在超时时说清是「超时」而不是「失败」。
	genCtx, cancelGen := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancelGen()
	run := exec.CommandContext(genCtx, "bash", scriptDst)
	run.Dir = tmp
	if out, err := run.CombinedOutput(); err != nil {
		if genCtx.Err() != nil {
			t.Fatalf("sync-objects.sh 在隔离树里**超时**（上限 2 分钟）⇒ 这道门会挂起而不是报错\n%s", out)
		}
		t.Fatalf("sync-objects.sh 在隔离树里跑失败：%v\n%s", err, out)
	}

	// ── ③ 对每个含标记对象的类型：README 必须点名基线形态对象，且不得把它们放进可 apply 清单 ──
	for _, ot := range typesToCheck {
		readmePath := filepath.Join(tmp, "deploy", "sql", "objects", ot, "README.md")
		mdBytes, err := os.ReadFile(readmePath)
		if err != nil {
			t.Errorf("生成器没有产出 %s/README.md：%v", ot, err)
			continue
		}
		md := string(mdBytes)

		baselineSection := extractBaselineSection(md)
		for name := range marked[ot] {
			if !strings.Contains(baselineSection, name) {
				t.Errorf("%s/README.md 的「基线形态对象（勿单独应用）」小节**没有**点名 %s\n"+
					"  ⇒ 照 README 操作的人不会知道这个文件不能单独 apply（v1 族已退役 ⇒ 必然 42P01）。",
					ot, name)
			}
		}
		// 双向：没声明的对象不得出现在基线小节里（防止「全标成基线」也能过）。
		for _, e := range listSQLFiles(t, filepath.Join(tmp, "deploy", "sql", "objects", ot)) {
			if marked[ot][e] {
				continue
			}
			if strings.Contains(baselineSection, e) {
				t.Errorf("%s/README.md 把**未声明**的对象 %s 列进了「基线形态对象」小节\n"+
					"  ⇒ 豁免被泛化 ⇒ 这道门就失去了区分能力。", ot, e)
			}
		}
	}

	// ── ④ 真跑 README 里的 apply-all 循环（psql 换成只回显的桩） ──
	//
	//   这才是这道门的核心：不是看 README 写了什么，而是看**照着做**会作用到哪些文件。
	totalApplied := 0
	for _, ot := range typesToCheck {
		readmePath := filepath.Join(tmp, "deploy", "sql", "objects", ot, "README.md")
		mdBytes, err := os.ReadFile(readmePath)
		if err != nil {
			t.Fatalf("读不到生成的 %s/README.md：%v", ot, err)
		}
		applied, err := runApplyLoop(t, tmp, string(mdBytes))
		if err != nil {
			t.Fatalf("执行 %s/README.md 里的 apply 循环失败：%v", ot, err)
		}

		for name := range marked[ot] {
			if applied[name] {
				t.Errorf("%s/README.md 里的 apply 循环**真的 apply 了**基线形态对象 %s\n"+
					"  ⇒ v1 族已退役 ⇒ 这条命令必然 42P01 ⇒ 「照文档操作就会炸」。", ot, name)
			}
		}
		// 非空下限：至少有一个**非**基线形态的对象被 apply
		//（否则「循环什么都没做」也是绿的）。
		if len(applied) == 0 {
			t.Fatalf("%s/README.md 里的 apply 循环**一个文件都没 apply** ⇒ 这道门会恒真空转\n"+
				"  （要么 README 变了，要么桩没接上）", ot)
		}
		totalApplied += len(applied)
		t.Logf("%s/README.md 的 apply 循环实际作用了 %d 个对象（其中基线形态 %d 个）",
			ot, len(applied), countMarked(applied, marked[ot]))
	}
	if totalApplied == 0 {
		t.Fatal("所有类型的 apply 循环都没作用到任何对象 ⇒ 这道门会恒真空转")
	}
}

// extractBaselineSection 抽出 README 里的「基线形态对象（勿单独应用）」小节正文。
func extractBaselineSection(md string) string {
	const header = "## ⚠ 基线形态对象（勿单独应用）"
	i := strings.Index(md, header)
	if i < 0 {
		return ""
	}
	rest := md[i+len(header):]
	if j := strings.Index(rest, "\n## "); j >= 0 {
		return rest[:j]
	}
	return rest
}

// listSQLFiles 列出目录下所有 .sql 文件名。
func listSQLFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读不到 %s：%v", dir, err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// runApplyLoop 执行 README 里的 apply-all 循环，返回**实际被 apply 的对象名集合**。
//
// `psql` 被换成只回显的桩 ⇒ 被 apply 的对象名会出现在输出里。
func runApplyLoop(t *testing.T, workDir, md string) (map[string]bool, error) {
	t.Helper()

	var loop string
	for _, blk := range bashFencedBlocks(md) {
		if strings.Contains(blk, "for f in ") && strings.Contains(blk, "psql") {
			loop = blk
			break
		}
	}
	if loop == "" {
		return nil, fmt.Errorf("生成的 README 里找不到 apply-all 循环（bash 围栏块）")
	}

	// 桩：只回显被 apply 的路径。
	binDir := filepath.Join(workDir, ".stub-bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return nil, err
	}
	stub := "#!/usr/bin/env bash\nprintf 'APPLY %s\\n' \"$*\"\n"
	if err := os.WriteFile(filepath.Join(binDir, "psql"), []byte(stub), 0o755); err != nil {
		return nil, err
	}

	script := filepath.Join(workDir, ".apply-loop.sh")
	if err := os.WriteFile(script, []byte(loop), 0o755); err != nil {
		return nil, err
	}

	// ★ 同上：apply 循环也是外部命令 ⇒ 必须有界 ctx。
	//   这条循环**理论上**该秒退（假 psql 只回显），所以 1 分钟上限足够宽；
	//   真要挂住，说明 README 里生成了某种会阻塞的命令 ⇒ 那本身就是缺陷。
	loopCtx, cancelLoop := context.WithTimeout(context.Background(), time.Minute)
	defer cancelLoop()
	cmd := exec.CommandContext(loopCtx, "bash", script)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"DATABASE_URL=postgres://stub/stub",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if loopCtx.Err() != nil {
			return nil, fmt.Errorf("apply 循环**超时**（上限 1 分钟）⇒ README 里生成了会阻塞的命令\n输出：\n%s", out)
		}
		return nil, fmt.Errorf("%v\n输出：\n%s", err, out)
	}

	applied := map[string]bool{}
	for _, ln := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(ln, "APPLY ") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(ln, "APPLY "))
		if len(fields) == 0 {
			continue
		}
		last := fields[len(fields)-1]
		base := filepath.Base(last)
		if !strings.HasSuffix(base, ".sql") || strings.Contains(base, "<") {
			// 模板占位（<object_name>.sql）不是真实对象 ⇒ 不计入。
			continue
		}
		applied[base] = true
	}
	return applied, nil
}

// countMarked 数一下被 apply 的对象里有几个是基线形态的。
func countMarked(applied map[string]bool, markedOfType map[string]bool) int {
	n := 0
	for name := range applied {
		if markedOfType[name] {
			n++
		}
	}
	return n
}

// describeMarked 把「类型 → 文件数」拼成一行，便于失败时定位。
func describeMarked(marked map[string]map[string]bool) string {
	var parts []string
	for _, ot := range objectTypes {
		if len(marked[ot]) > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", ot, len(marked[ot])))
		}
	}
	return strings.Join(parts, " ")
}

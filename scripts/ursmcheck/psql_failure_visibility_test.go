// psql_failure_visibility_test.go —— 「psql 失败可见性」口径的静态守卫（R102 轮）
//
// 【这个门守什么】
//
//	仓内多处注释断言「psql 失败时的退出码由读入方式决定」，且清理路径据此设计。
//	那是一条**会漂移**的事实（psql / PG 升级都可能改语义）⇒ 一旦失准，
//	「清理失败」会静默 ⇒ 一次性库泄漏且无人察觉。
//	⇒ 本门守住两件���
//	  ① 仓内对 psql 读入方式的**措辞**不得回退到已废弃的形态命名；
//	  ② 每个「回收载体」里，**查落点**必须与 **DROP DATABASE** 同处一个载体。
//
// 【为什么门是静态的、而不是直接跑 psql】
//
//	真跑 psql 需要起容器（≈30s 起 + 10 格读数）⇒ 放进整包门会让整包从 ~15min 变更长，
//	且在「最忙的时候」容易假红（R101 的教训）。
//	⇒ 真读数由**独立可复跑的行为契约脚本**承担：
//	    scripts/audit/measure-psql-form-matrix.sh
//	★ 那个脚本把「读入方式 → (退出码, 回滚, 后续执行)」钉成契约；psql 语义一变就红。
//	⇒ 本门只做**静态口径守卫**，并断言契约脚本**存在且带全三维度**。
//
// 【形态约束】（★ 改本门前先读这段）
//
//	① 仓根用 runtime.Caller 定位（★ 否则静态守卫扫不到文件 ⇒ 恒绿门 —— R101 的教训）。
//	② 「查落点」判据用「含 DROP DATABASE 的行 ± 25 行窗口」⇒ **不写死函数名**
//	   ⇒ 将来把清理抽进 helper 也不必改门（★ 判据要能容纳好写法）。
//	③ 措辞判据只针对**已废弃的具体串**（不是泛化禁令）⇒ 新写法不会被误伤。
//	④ **行为判据（查落点）必须剥注释后再断言** —— 否则注释里出现 `LEFT=` 就算合格
//	   ⇒ 恒绿门（R65 的教训：断言 SQL 文本前必须剥注释）。
//	   ★ 但**措辞判据恰恰要检注释** ⇒ 那一条**故意不剥**（在代码里写明理由）。
package ursm

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// repoRootByCaller 返回仓根目录。
//
// ★ 必须用 runtime.Caller：写死相对路径（"../.."）或依赖 os.Getwd()，
//
//	在测试的工作目录变化时会扫不到文件 ⇒ 静态守卫变成恒绿门（R101 轮实测到的同类坑）。
//
// ★★ 同包另有一个 `repoRootFromUrsm`（stale_expiry_criterion_test.go）用 os.Getwd()
//
//	⇒ 那是**依赖工作目录**的脆形态 ⇒ 本门**不复用**它（也不擅改他人文件 ⇒ 只登记）。
func repoRootByCaller(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 失败 ⇒ 无法定位仓根")
	}
	// file = <repo>/scripts/ursmcheck/<this>.go
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("仓根定位可疑：%s 下没有 go.mod", root)
	}
	return root
}

// r102PsqlLegs = 走 stdin 形态的三个 python 腿（清理路径的回收载体在这三个文件里）。
var r102PsqlLegs = []string{
	"scripts/audit/verify_archive_orphan_local.py",
	"scripts/audit/verify_columnar_bulk_write_local.py",
	"scripts/audit/verify_promote_wal_cost_local.py",
}

// r102ContractScript = 行为契约脚本（真读数由它承担）。
const r102ContractScript = "scripts/audit/measure-psql-form-matrix.sh"

// stripPyComments 剥掉 python 的**文档串**与**行注释**，保留普通字符串字面量。
//
// ★ 为什么用它：「行为判据」不能被注释里的关键词骗过（R65 的教训）。
//
//	—— 若不剥，注释里写一句 `LEFT=` 就能让判据恒绿。
//
// ★★ 为什么**普通字符串必须保留**（第一版写错的地方）：
//
//	三个 python 腿的回收动作长这样——
//	  run_sql("SET statement_timeout = '120s'; DROP DATABASE IF EXISTS {DB} WITH (FORCE);")
//	⇒ 「DROP DATABASE」就在**字符串字面量**里 ⇒ 剥掉字符串 ⇒ 判据会**扫不到回收动作**
//	⇒ 变成恒绿门（比恒绿更坏：它还给出「合格」的结论）。
//
// ★ 实现注记：不用正则 —— Go 的 RE2 **不支持反向引用**（`\1`）⇒ 上一版直接 panic。
//
//	⇒ 改成单遍状态机。
func stripPyComments(src string) string {
	var b strings.Builder
	b.Grow(len(src))
	const (
		stCode = iota
		stDoc
	)
	state := stCode
	docQuote := "" // 当前文档串的定界符（`"""` 或 `'''`）
	inS, inD := false, false
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch state {
		case stDoc:
			if strings.HasPrefix(src[i:], docQuote) {
				i += len(docQuote) - 1
				state = stCode
			}
			continue
		case stCode:
			switch {
			case strings.HasPrefix(src[i:], `"""`):
				state = stDoc
				docQuote = `"""`
				i += 2
				continue
			case strings.HasPrefix(src[i:], `'''`):
				state = stDoc
				docQuote = `'''`
				i += 2
				continue
			case c == '#' && !inS && !inD:
				for i < len(src) && src[i] != '\n' {
					i++
				}
				b.WriteByte('\n')
				continue
			case c == '\'' && !inD:
				inS = !inS
			case c == '"' && !inS:
				inD = !inD
			}
			b.WriteByte(c)
		}
	}
	return b.String()
}

// r102HasResidualCheck 判定「含 DROP DATABASE 的行 ± window 行」窗口内是否有查落点。
//
// 查落点的两种合法写法：
//
//	① 直接查：`LEFT=` 与 `pg_database` 同现（python 腿的 _residual() 就是这个形态）
//	② 调 helper：调用了名字里带 `_residual` 的辅助函数
//	★ 两者都算合格 ⇒ 判据能容纳好写法。
func r102HasResidualCheck(codeLines []string, window int) (bool, int) {
	for i, ln := range codeLines {
		if !strings.Contains(ln, "DROP DATABASE") {
			continue
		}
		lo := i - window
		if lo < 0 {
			lo = 0
		}
		hi := i + window
		if hi >= len(codeLines) {
			hi = len(codeLines) - 1
		}
		seg := strings.Join(codeLines[lo:hi+1], "\n")
		hasSQL := strings.Contains(seg, "pg_database") && strings.Contains(seg, "LEFT=")
		hasHelper := strings.Contains(seg, "_residual(")
		if hasSQL || hasHelper {
			return true, i + 1
		}
	}
	return false, -1
}

// T1：行为契约脚本存在，且把「退出码 / 回滚 / 后续执行」三维度都钉住了。
//
// ★ 这一条是「门能看见真读数」的入口：脚本没了 ⇒ 三维度的读数没了。
// ★ 第二条断言（rb 与 ct 两维都在）针对的是本脚本**第一版只测一维**的缺陷
//
//	（只放了一个落点表在坏语句之前 ⇒ 只能测「回滚」⇒ 测不出「后续是否执行」）。
func TestPsqlFormContractScriptDeclaresThreeDimensions(t *testing.T) {
	root := repoRootByCaller(t)
	p := filepath.Join(root, r102ContractScript)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("行为契约脚本不存在：%s（⇒ 真读数载体缺失）", r102ContractScript)
	}
	s := string(b)
	for _, want := range []string{
		"EXPECT_C_ON", "EXPECT_STDIN_ON", "EXPECT_FDASH_ON", "EXPECT_FDEV_ON", // 四种形态
		"EXPECT_RB_C",                         // 回滚维
		"EXPECT_CT_C",                         // 后续执行维
		"r102_pre_marker", "r102_post_marker", // 两个落点表
		"ON_ERROR_STOP=1",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("契约脚本缺少 %q ⇒ 三维度读数不完整", want)
		}
	}
}

// T2：三个 python 腿的回收载体里，查落点必须与 DROP DATABASE 同处一个载体。
//
// ★ 行为判据 ⇒ 判之前**先剥注释**（否则注释里写一句 `LEFT=` 就恒绿）。
func TestPsqlCleanupCarrierKeepsResidualCheck(t *testing.T) {
	root := repoRootByCaller(t)
	for _, rel := range r102PsqlLegs {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Errorf("读不到 %s：%v", rel, err)
			continue
		}
		code := stripPyComments(string(b))
		lines := strings.Split(code, "\n")
		ok, line := r102HasResidualCheck(lines, 25)
		if !ok {
			t.Errorf("%s：没有「查落点」与 DROP DATABASE 同处一个回收载体（±25 行窗口内）\n"+
				"    ⇒ 清理失败会静默 ⇒ 一次性库泄漏无人察觉", rel)
			continue
		}
		t.Logf("%s：查落点与回收动作同载体（第 %d 行附近）", rel, line)
	}
}

// T3：三个 python 腿的注释不得再使用已废弃的形态命名。
//
// ★ 这一条**故意不剥注释**：它检的正是注释措辞。
// ★ 只针对**已废弃的具体串**，不是泛化禁令 ⇒ 新写法不会被误伤（R101 的教训：
//
//	禁令门会禁止掉正确的话）。
func TestPsqlNoStaleFormWording(t *testing.T) {
	root := repoRootByCaller(t)
	// 已废弃串 → 为什么废弃
	stale := map[string]string{
		"stdin 脚本形态": "裸 stdin 是交互模式、-f - 才是脚本模式；R102 实测两者失败语义一致，措辞应写「stdin 形态」",
		"（脚本模式）":     "stdin=True 那条路径**没有 -f** ⇒ 是裸 stdin 交互模式，不是脚本模式",
	}
	for _, rel := range r102PsqlLegs {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Errorf("读不到 %s：%v", rel, err)
			continue
		}
		s := string(b)
		for needle, why := range stale {
			if strings.Contains(s, needle) {
				t.Errorf("%s：注释仍用已废弃措辞 %q ⇒ %s", rel, needle, why)
			}
		}
	}
}

// T4：判据自检 —— 证明 T2 的判据**有牙**。
//
// ★ 「门绿」≠「量具有牙」：这里用两个反向夹具证明判据会红：
//
//	① 合形态（有查落点）⇒ 必须判合格
//	② 坏形态（查落点被挪到窗口外）⇒ 必须判不合格
func TestPsqlResidualJudgeHasTeeth(t *testing.T) {
	good := []string{
		"def _drop_db():",
		"    out = run_sql(\"DROP DATABASE IF EXISTS x WITH (FORCE);\")",
		"    left = _residual()",
	}
	if ok, _ := r102HasResidualCheck(good, 25); !ok {
		t.Errorf("判据自检失败：合形态（查落点同载体）被误判为不合格 ⇒ 判据过严")
	}
	bad := []string{
		"def _drop_db():",
		"    out = run_sql(\"DROP DATABASE IF EXISTS x WITH (FORCE);\")",
	}
	// 把查落点挪到 40 行之外 ⇒ 窗口内没有 ⇒ 必须判不合格
	for i := 0; i < 40; i++ {
		bad = append(bad, "    pass")
	}
	bad = append(bad, "    left = _residual()")
	if ok, _ := r102HasResidualCheck(bad, 25); ok {
		t.Errorf("判据自检失败：坏形态（查落点不在载体窗口内）被判为合格 ⇒ 判据恒绿")
	}
}

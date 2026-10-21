package ursm_test

// R95 轮 · 段 D 建 / ★ 段 E 改轴的门：**「自建资源回收」的兜底【质量】守卫**。
//
// 被测对象：
//
//	scripts/ursmcheck/selfcreated_cleanup_quality_ledger.tsv   （台账：逐条人工裁决）
//	scripts/ursmcheck/selfcreated_cleanup_language_map.tsv     （★ 语言映射表：判据的唯一语义事实源）
//
// ── 为什么需要它 ────────────────────────────────────────────────────────────
//
// 已有纪律：「自己建的资源必须用 trap 兜住所有退出路径」（R94 轮续九已落地）。
// ★★ 但实测证明：**「有 trap」≠「兜底合格」**。兜底有 4 个独立可失效的轴：
//
//	A fallback_covers ★★ 兜底【载体】里是否**真的包着回收动作**（★ 不是「有没有 trap」！）
//	                 ⇒ 缺了它：提前退出（return/exit N）跳过回收（实测攒下 7 个残留库）
//	                 ⇒ ★★ 段 E 订正：段 D 判的是【存在性】，实测反例见下
//	B signal_trap     有没有接管信号（★ 是否【必需】按语言判，见「合格兜底」的定义）
//	C cleanup_guard   清理语句自带【防卡保护】（两种机制任一）
//	                 ⇒ C1 statement_timeout 类（中止语句）
//	                 ⇒ C2 pg_terminate_backend 类（先把阻塞会话踢掉再 DROP）
//	                 ⇒ ★ 缺了它：清理语句卡死 ⇒ 「一次慢 DROP」被放大成「每次退出都卡」
//	                   （实测：`DROP DATABASE … WITH (FORCE)` 卡 ≈13 分钟后被 `Killed: 9`；
//	                    而本机 PG 的 statement_timeout=0 / lock_timeout=0 ⇒ 无服务端兜底）
//	D child_reap      有后台子进程时，清理前有没有 kill + wait 收割
//	                 ⇒ ★ 缺了它：子进程被孤儿化 ⇒ 它在父进程 cleanup【之后】才把资源建出来 ⇒ 依然泄漏
//
// ── ★★ 段 E 的三组实测（判据的来源，全部可复现）─────────────────────────────
//
// 【实测 1】跨语言：兜底在【信号终止】下会不会执行
//
//	语言   兜底机制                 SIGTERM 下清理是否执行
//	──────────────────────────────────────────────────────────────
//	shell  trap ... EXIT            ✔ 会执行
//	python atexit.register          ★ 不执行
//	python try-finally              ★ 不执行
//	go     defer                    ★ 不执行
//
//	⇒⇒⇒ **⇒ 「合格兜底」的定义是 f(语言) 的** ⇒⇒⇒ 判据必须【语言感知】。
//	⇒⇒⇒ ⇒ 该结论的唯一事实源 = selfcreated_cleanup_language_map.tsv（★ 表驱动 ⇒ 代码与表不可能不一致）。
//
// 【实测 2】shell 的 trap 执行时机（★ 段 E 新增，承重）
//
//	变体                                  SIGTERM 后                          读数
//	────────────────────────────────────────────────────────────────────────
//	P1 有 INT/TERM trap（不 exit）        trap 在【前台命令结束】才 fire    ★ 被推迟
//	P2 无 INT/TERM trap、只有 EXIT        t0+1.01s 退出，EXIT 立即执行      ★ 立即
//	P3 INT/TERM trap 里带 exit            trap 在【前台命令结束】才 fire    ★ 一样被推迟
//
//	⇒⇒⇒ **⇒ bash 会把 trap 的执行推迟到当前前台命令结束** ⇒⇒⇒
//	⇒⇒⇒ ⇒ 「挂信号 trap」不是「让回收更及时」，而是**让回收更晚**（或让进程不立即死）。
//	⇒⇒⇒ ⇒ ★★ 这不改变「shell 的 EXIT trap 在信号下会跑」这个结论，但它是那条结论的**机制解释**。
//
// 【实测 3】★ 「存在性 ≠ 覆盖性」（★ 段 E 对段 D 的订正）
//
//	scripts/audit/verify_892_local.sh 第 26 行 = `trap 'rm -f "$TMP"' EXIT`
//	⇒ ★ 它只删【临时 SQL 文件】，**不回收库**（库回收在第 46 行 = 正常路径末尾）
//	⇒ ⇒ 「有 trap」≠「trap 覆盖了要回收的资源」
//	⇒ ⇒ ★ 存在性轴是**假阳性方向**（会把真会泄漏的脚本判成合格）⇒ ⇒ 本门改判【覆盖性】。
//
// ── 「合格兜底」的定义（★ 按实测定，不按直觉定）──────────────────────────
//
//	合格 = A=yes（兜底载体覆盖回收动作）
//	    ∧ 防卡保护（C1 ∨ C2）
//	    ∧ (没有后台子进程 ∨ D=yes)
//	    ∧ (该语言需要信号接管 ⇒ B=yes)      ← ★ 由语言映射表的 signal_covers_sigterm 决定
//
// ★★ 为什么 B（信号接管）**不是**对所有语言都必需：
//   实测 shell 的 EXIT trap 在 SIGTERM/SIGINT 下【照样会跑】⇒ ⇒ shell 侧不接信号**零风险**
//   ⇒ ⇒ ⇒ 若把 B 列为 shell 的无条件必需项，那这条断言在防一个【不存在的风险】⇒ ⇒ ⇒ 假红。
//   ⇒ ⇒ ⇒ 而 python/go 侧实测【不跑】⇒ ⇒ 它们【必须】显式接管信号 ⇒ ⇒ 缺了就是真缺口。
//
// ── 门的形态：台账 + 棘轮 ───────────────────────────────────────────────────
//
// ★ 本门**不**与并行会话 R94 续九 ⑤ 第 4 项（把 trap 纪律推广到其他脚本）重复：
//	他们做的是**修复轴**（加 trap）；本门做的是**验收轴**（什么算「兜底合格」）。
//	⇒ ⇒ 二者互补：他们的推广若只加 trap 而不加防卡保护 ⇒ 本门会红 ⇒ ⇒ 这就是「什么时候算修完」。
//
// ★ 本门**不阻塞**存量：今天已知的缺陷在台账里显式登记为 verdict=待修 + deficits=<缺失项>
//	⇒ ⇒ 门是绿的。⇒ ⇒ 存量缺陷只减不增：源码里补上保护 ⇒ 重算的缺陷集少了 ⇒
//	⇒ ⇒ 台账没同步 ⇒ **门红** ⇒ ⇒ ⇒ 强制登记 ⇒ ⇒ ⇒ 缺陷集单调下降。
//	⇒ ⇒ ★ 诚实边界：段 E 把 A 从存在性改成覆盖性 ⇒ 缺陷集**变大**了（3 行）⇒
//	⇒ ⇒   这是**订正**不是退化 ⇒ 「单调下降」是【订正之后】才成立的性质。
//
// 判据：
//	① 账实相符：台账的 5 个轴值必须与【从源码重算】的逐字一致
//	② 双向候选集：候选集（源码里自建资源 + 自回收的脚本）必须与台账行集完全一致
//	③ verdict=合格 ⇒ deficits 必须为空（即重算出来确实合格）
//	④ verdict=待修 ⇒ deficits 必须【逐项等于】重算出来的缺失项集合（⇒ 缺陷集单调下降）
//	⑤ verdict=无回收义务 ⇒ evidence 必须写明「长驻」/「假阳性」/「不建库」
//	⑥ 结构不变量：取值域合法 / evidence 非空 / 台账非空 / 每个文件只出现一次
//	⑦ 语言映射表覆盖：台账里出现的每种语言都必须在映射表里有行（★ 否则该语言的语义无依据）
//
// ★★ 量具自检（防恒绿门）：本门必须在**探针**上验证 8 种形态给出**两两不同的缺失项集合**
//	⇒ ⇒ 否则整门可能恒绿。★ 且探针里必须有一条「存在性 ≠ 覆盖性」的反例（探针 7）。

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const (
	scqLedger     = "selfcreated_cleanup_quality_ledger.tsv"
	scqLangMapTSV = "selfcreated_cleanup_language_map.tsv"
)

// ── 语言映射表（★ 表驱动：判据的唯一语义事实源）───────────────────────────
//
// ★★★ 为什么要有这张表（★ 段五的实测结论）：
//   「合格兜底」的定义是 **f(语言)** 的 ——
//     shell  的 `trap … EXIT`  在 SIGTERM/SIGINT 下【会执行】
//     python 的 atexit / try-finally 【不会执行】
//     go     的 defer          【不会执行】
//   ⇒ ⇒ 「信号接管」这一项对 shell 是**多余的**，对 python/go 是**必需的**。
//   ⇒ ⇒⇒ ⇒ 若把这个判据写死在代码里，语义就散在两处 ⇒ ⇒⇒ ⇒ 改成【表驱动】：
//   ⇒ ⇒⇒ ⇒ 门的语义只有一处事实源（这张表）⇒ ⇒⇒ ⇒ 代码与表**不可能**不一致。
//
// ★ 这张表同时是**可审计的证据**（每行带实测依据）⇒ 属主/协作者可以直接核。

// scqLangInfo 是语言映射表的一行。
type scqLangInfo struct {
	Language            string
	FallbackMech        string
	SignalCoversSigterm bool
	GuardForm           string
	Evidence            string
}

var (
	scqLangMap    map[string]scqLangInfo
	scqLangMapErr error
)

func init() {
	scqLangMap, scqLangMapErr = scqReadLangMap()
}

// scqReadLangMap 读语言映射表。
func scqReadLangMap() (map[string]scqLangInfo, error) {
	out := map[string]scqLangInfo{}
	fh, err := os.Open(scqLangMapTSV)
	if err != nil {
		return out, fmt.Errorf("打开语言映射表失败: %w", err)
	}
	defer fh.Close()
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	ln := 0
	for sc.Scan() {
		ln++
		text := sc.Text()
		if strings.TrimSpace(text) == "" || strings.HasPrefix(strings.TrimSpace(text), "#") {
			continue
		}
		f := strings.Split(text, "\t")
		if len(f) != 5 {
			return out, fmt.Errorf("语言映射表第 %d 行列数 = %d，期望 5", ln, len(f))
		}
		if f[2] != "yes" && f[2] != "no" {
			return out, fmt.Errorf("语言映射表第 %d 行 signal_covers_sigterm = %q 非法（yes/no）", ln, f[2])
		}
		if strings.TrimSpace(f[4]) == "" {
			return out, fmt.Errorf("语言映射表第 %d 行 evidence 为空", ln)
		}
		out[f[0]] = scqLangInfo{
			Language:            f[0],
			FallbackMech:        f[1],
			SignalCoversSigterm: f[2] == "yes",
			GuardForm:           f[3],
			Evidence:            f[4],
		}
	}
	if err := sc.Err(); err != nil {
		return out, fmt.Errorf("读语言映射表失败: %w", err)
	}
	if len(out) == 0 {
		return out, fmt.Errorf("语言映射表为空 ⇒ 判据无事实源 ⇒ 拒绝给结论")
	}
	return out, nil
}

// ── 判据用的正则 ────────────────────────────────────────────────────────────

var (
	scqReCreateDB = regexp.MustCompile(`(?i)\bCREATE\s+DATABASE\b`)
	scqReCreateCT = regexp.MustCompile(`(?i)\bdocker\s+run\b|\bdocker\s+compose\s+up\b|\bdocker-compose\s+up\b`)
	scqReDropDB   = regexp.MustCompile(`(?i)\bDROP\s+DATABASE\b`)

	scqReTrapLine = regexp.MustCompile(`(?m)^\s*trap\s+.*$`)
	scqReExitTrap = regexp.MustCompile(`(?m)^\s*trap\s+[^\n]*\bEXIT\b`)
	scqReSignal   = regexp.MustCompile(`\b(EXIT|INT|TERM|QUIT|HUP)\b`)
	// ★ 语言感知的【兜底存在性】判据 —— 形态复用并行会话 R94 续九那套量具的 C2 判据
	//   （scripts/audit/measure-selfcreated-cleanup.sh 第 316–324 行）：
	//     shell ⇒ `trap ... EXIT`；python ⇒ atexit.register / try+finally；go ⇒ defer
	// ★★ ★ 而本轮实测证明：**这三者在【信号终止】下并不等价** ——
	//   shell 的 EXIT trap 在 SIGTERM/SIGINT 下【会跑】；python 的 atexit/finally 与
	//   go 的 defer 【都不会跑】⇒ ⇒ ⇒ 「合格兜底」的定义必须【语言感知】。
	scqRePyFallback = regexp.MustCompile(`atexit\.register|finally\s*:`)
	scqReGoFallback = regexp.MustCompile(`(^|[^A-Za-z0-9_])defer\s`)
	scqReGoTimeout  = regexp.MustCompile(`context\.WithTimeout`)
	// ★ 信号接管的判据（python/go 侧必需：实测它们的兜底不覆盖信号）
	scqRePySignal = regexp.MustCompile(`signal\.signal\s*\(|SIGTERM|SIGINT`)
	scqReGoSignal = regexp.MustCompile(`os\.Signal|signal\.Notify`)

	// ★ 清理语句的「自带超时」标记（C1）。
	//
	// ★★ 判据必须收窄到【有确切语义的那一个】，而不是加更多上下文启发：
	//   第一版用 `timeout\s*=\s*\d` / `timeout\s+\d+[smh]?` ⇒ 手工核对时抓到两处假阳性：
	//     ① `db-init-lib.sh` 第 592/1188/1215 行的 `timeout=20/15/10` 是【内嵌 Python 的
	//        subprocess.run(timeout=)】，跟第 780/796 行的 DROP 毫无关系；
	//     ② `run-integration-gate.sh` 第 850 行的 `-timeout 20m` 是【go test】，
	//        而 DROP 在第 239/248 行。
	//   ⇒⇒ ⇒ 所以只认【数据库语义】的超时标记。
	//   ⇒⇒ ⇒ ★ 这也正是 R95 段 C 实测出来的那条修法（`SET statement_timeout='15s'`）⇒ 判据与修法对齐。
	scqReTimeout = regexp.MustCompile(
		`(?i)statement_timeout|lock_timeout|idle_in_transaction_session_timeout`)
	// ★ 防卡的【另一种】机制（C2）：先把阻塞会话踢掉再 DROP。
	//   ★ 仓内已有两个脚本这么写（scripts/local-r112-migrate.sh、
	//   scripts/sync-gateway-db-to-211.sh）⇒ ⇒ 这不是外来建议，是本仓既有实践。
	//   ★ 与 C1 机制不同（一个是中止语句，一个是清掉阻塞者）⇒ ⇒ 分成两轴记，信息量更大。
	scqReDisconnect = regexp.MustCompile(`(?i)pg_terminate_backend|pg_cancel_backend`)

	// ★ 后台子进程：行尾的 `&`（排除 `&&` / `2>&1` / `$(&` / `&>`）、以及 `docker run -d`。
	// ★★ 第一版用一条正则 `(?:\S)\s+&\s*(?:#.*)?$` ⇒ 只认「行尾的 &」⇒
	//   **认不出 `build & PID=$!` 这种「& 后面跟赋值」的形态** ⇒ ⇒ 而那正是本仓
	//   唯一的后台子进程写法 ⇒ ⇒ 第一版把所有脚本都判成「无后台子进程」⇒
	//   ⇒ ★ 而且这个缺陷是【门自己的量具自检】抓出来的（探针 2 与探针 3 的缺失项
	//   集合变得相同 ⇒ 判据无区分度）⇒ ⇒ ⇒ 这就是量具自检的价值。
	scqReDockerRunD = regexp.MustCompile(`(?i)docker\s+run\b[^\n]*\s-d\b`)
	// ★ 执行上下文：判据复用了并行会话 R94 续九那套量具的形态
	//   （scripts/audit/measure-selfcreated-cleanup.sh 的 C1/C3 判据）：
	//   「剥注释、排除正则字面量后仍能匹配到 CREATE DATABASE / createdb」
	//   且「匹配行 **或它 ±3 行窗口内** 含执行上下文」。
	// ★★ 但他们的版本对「第 88 行是注释、注释里含 psql」这一类会漏 ⇒ ⇒ 加上「剥注释」这步就闭合。
	//
	// ★★★ ★★ 【语言感知】——★ 这是本轮实测抓到的**我自己写的判据 bug**：
	//   第一版的非 shell 分支【根本没检查 re 参数】⇒ 只要文件里有任意一行命中执行上下文
	//   就返回 true ⇒⇒ 「有 CREATE DATABASE」这个必要条件被丢掉 ⇒⇒
	//   ⇒⇒ 判据退化成【恒真】⇒⇒ 整个 Go 树（几百个文件）全成了候选 ⇒⇒ 门会红成一片。
	//   ⇒⇒⇒ **⇒ 通用式：谓词函数收了参数却没用它 ⇒ 判据恒真 ⇒ 先查「参数有没有被用」。**
	scqReExecCtx = regexp.MustCompile(`(?i)psql|createdb|\bexec\b|run_psql|pg_exec|docker\s+exec`)
	// ★ python 的执行上下文：SQL 真正落地的那几个形态
	//   （仓内实例：scripts/audit/verify_archive_orphan_local.py 用 run_sql(…)，
	//    scripts/audit/verify_columnar_bulk_write_local.py 用 psql(…)）
	scqReExecCtxPy = regexp.MustCompile(`(?i)psycopg|run_sql\s*\(|psql\s*\(|\.execute\s*\(|cursor|subprocess\.`)
	// ★ go 的执行上下文：pgx 的 Exec/Query 家族（仓内实例：admin.Exec / root.Exec / conn.ExecContext）
	scqReExecCtxGo = regexp.MustCompile(`(?i)\.Exec(Context)?\s*\(|pgx\.|QueryRow|\.Query\s*\(|ExecContext`)
	scqReKillWait  = regexp.MustCompile(`(?i)\bkill\b|\bwait\b`)
	scqReFuncHead  = regexp.MustCompile(`^\s*(?:function\s+)?((?:[A-Za-z_][A-Za-z0-9_]*::)*[A-Za-z_][A-Za-z0-9_]*)\s*\(\s*\)\s*\{`)
	scqReFuncTail  = regexp.MustCompile(`^\}\s*$`)

	// ── 覆盖性判据（★ 段五新增）：兜底「载体」里到底包不包着回收动作 ──────────
	//
	// ★★★ 为什么要把「存在性」升级成「覆盖性」（★ 本轮实测抓到的**假阳性方向**）：
	//   段四的门判「fallback = 文件里有没有 trap … EXIT」⇒ 读数是**存在性**。
	//   ★★ 但实测发现：scripts/audit/verify_892_local.sh 第 26 行是
	//      `trap 'rm -f "$TMP"' EXIT` ⇒ ★ 它只删**临时 SQL 文件**，**不回收库**；
	//      库回收在第 46 行（正常路径末尾）⇒ ⇒ 提前退出 ⇒ 库留下。
	//   ⇒ ⇒ 「有 trap」≠「trap 覆盖了要回收的资源」⇒ ⇒ 存在性轴是**假阳性方向**
	//   ⇒ ⇒ （假阳性方向最危险：门会把真会泄漏的脚本判成合格。）
	scqReTrapExitCmd = regexp.MustCompile(`(?m)^\s*trap\s+(.+?)\s+EXIT\s*$`)
	scqIdentRe       = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

	scqRePyAtexitReg = regexp.MustCompile(`atexit\.register\s*\(\s*([A-Za-z_][A-Za-z0-9_]*)`)
	scqRePyDef       = regexp.MustCompile(`^([ \t]*)def[ \t]+([A-Za-z_][A-Za-z0-9_]*)\s*\(`)
	scqRePyFinally   = regexp.MustCompile(`^([ \t]*)finally\s*:`)

	scqReGoCleanupFn = regexp.MustCompile(`t\.Cleanup\s*\(\s*func\s*\(\s*\)\s*\{`)
	scqReGoDeferFn   = regexp.MustCompile(`defer\s+func\s*\(\s*\)\s*\{`)
	scqReGoCleanupNm = regexp.MustCompile(`t\.Cleanup\s*\(\s*([A-Za-z_][A-Za-z0-9_]*)\s*\)`)
	scqReGoFuncDef   = regexp.MustCompile(`(?m)^func\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`)
)

// scqExecCtxFor 按语言取「执行上下文」正则。
func scqExecCtxFor(lang string) *regexp.Regexp {
	switch lang {
	case "python":
		return scqReExecCtxPy
	case "go":
		return scqReExecCtxGo
	default:
		return scqReExecCtx
	}
}

// scqScopeDirs 是本门的**候选集扫描口径**。
//
// ★★ 口径必须与并行会话那套量具（scripts/audit/measure-selfcreated-cleanup.sh 的
//
//	`grep -rl --include='*.sh' --include='*.bash' --include='*.py' --include='*.go'
//	 … scripts internal cmd`）**一致** ⇒⇒ 理由有二：
//	① 【可比性】两个台账的差集才有意义 ⇒ 否则差集里混着「口径差」而不是「轴差」；
//	② 【范围纪律】产品代码（admin/ domains/ sql/ …）不是「自建一次性资源并自己回收」的审计对象。
//
// ★⚠ **诚实边界（已知局限，本轮未消）**：口径之外，Go 测试文件里【自己】建库并回收的
//
//	（例：admin/admin_500_channel_gap_realdb_test.go 第 294/317 行自建隔离库 + t.Cleanup 回收）
//	**不在本门覆盖内** ⇒ 它们靠共享 helper（internal/testdb/testdb.go，已在口径内）
//	与自身的 t.Cleanup 兜底 ⇒ ⇒ 属主决策项：是否把口径扩到 admin/ domains/ tests/。
var scqScopeDirs = []string{"scripts", "internal", "cmd"}

// scqSelfFiles 是本门【自己】的文件 ⇒ 必须从候选集里排除（量具脚本自匹配）。
var scqSelfFiles = map[string]bool{
	"scripts/ursmcheck/selfcreated_cleanup_quality_test.go": true,
}

// scqShellFuncs 抽出 shell 的 `name() {` … `^}` 函数体。
func scqShellFuncs(src string) map[string]string {
	out := map[string]string{}
	lines := strings.Split(src, "\n")
	var cur string
	var buf []string
	flush := func() {
		if cur != "" {
			out[cur] = strings.Join(buf, "\n")
		}
		cur, buf = "", nil
	}
	for _, ln := range lines {
		if m := scqReFuncHead.FindStringSubmatch(ln); m != nil {
			flush()
			cur, buf = m[1], []string{ln}
			continue
		}
		if cur != "" {
			buf = append(buf, ln)
			if scqReFuncTail.MatchString(ln) {
				flush()
			}
		}
	}
	flush()
	return out
}

// scqLogicalCommands 把源码切成「逻辑命令」：把反斜杠续行并成一条。
//
// ★★ R98 轮改写（★ 原版有一个**假阳性方向**的缺陷，本轮实测抓到）：
//
//	原版会「猜引号」——某行的单引号数为奇数就认为后面还在引号里，于是把【其后直到文件尾】
//	的所有行并成同一条命令 ⇒ ⇒ 判据退化成「文件里任意位置出现过标记就算防卡保护」。
//	实测触发点：含撇号的**英文注释**
//	（scripts/audit/verify-session-family-p0-realdb.sh 第 10 行 "The P0 round's …"）。
//
//	⇒ 修法：★★ **只按反斜杠续行合并**（这是 shell 的真实语义）⇒ **不再猜引号**。
//	⇒ 实测：20 个候选文件上，「猜引号版」与「只按续行版」的 C1/C2 读数**完全一致（0 差异）**
//	⇒ ⇒ 这不是「用更弱的判据换绿灯」，而是**把一个启发式换成确定规则**。
//	⇒ ★★ 通用式：★ 静态判据里凡是用「字符奇偶」去猜语言状态（引号未闭合）的地方，
//	⇒ ⇒ 都应当【先问：真实语义是什么】⇒ shell 里「一条命令」的边界就是【反斜杠续行】⇒ 别去猜引号。
func scqLogicalCommands(src string) []string {
	lines := strings.Split(src, "\n")
	var cmds []string
	var acc string
	for _, ln := range lines {
		t := ln
		cont := strings.HasSuffix(t, "\\")
		if cont {
			t = strings.TrimSuffix(t, "\\")
		}
		acc += t
		if cont {
			acc += "\n"
			continue
		}
		cmds = append(cmds, acc)
		acc = ""
	}
	if acc != "" {
		cmds = append(cmds, acc)
	}
	return cmds
}

// scqStripComments 把「整行都是注释」的行清空（保留行数，便于 ±N 窗口定位）。
//
// ★★ 【语言感知】：第一版只认 shell/python 的 `#` 前缀 ⇒⇒ Go 文件的 `//` 注释
//
//	**没被剥掉** ⇒⇒ 注释里的 "CREATE DATABASE" 被当成真语句 ⇒⇒ 假阳性。
//	⇒⇒ ⇒ shell/python ⇒ `#`；go ⇒ `//`。
func scqStripComments(src string, lang string) []string {
	out := make([]string, 0, 64)
	for _, ln := range strings.Split(src, "\n") {
		t := strings.TrimSpace(ln)
		if t == "" {
			out = append(out, "")
			continue
		}
		if lang == "go" {
			if strings.HasPrefix(t, "//") {
				out = append(out, "")
				continue
			}
		} else if strings.HasPrefix(t, "#") {
			out = append(out, "")
			continue
		}
		out = append(out, ln)
	}
	return out
}

// scqHasExecContext 判断 src 里是否存在「真的在执行 CREATE/DROP DATABASE」。
//
// ★ 判据形态复用了并行会话 R94 续九那套量具的 C1/C3 判据：
//
//	剥注释 + 「匹配行或它 ±3 行窗口内含执行上下文」。
//
// ★★ 为什么必须剥注释（★ 本轮实测抓到的假阳性）：
//
//	scripts/audit/measure-selfcreated-cleanup.sh 是个【量具脚本】⇒ 它的源码里
//	「CREATE DATABASE」「DROP DATABASE」「psql」全是【分析用的正则与标签】
//	⇒ 不剥注释 ⇒ 它会被判成「自建资源 + 自回收」⇒ ⇒ 而它根本不建库
//	⇒ ⇒ ★ 这正是并行会话台账里点名的第 ③ 类假阳性「量具脚本自匹配」。
func scqHasExecContext(lines []string, re *regexp.Regexp, lang string) bool {
	ctxRe := scqExecCtxFor(lang)
	// ★★ 【必要条件】文件里必须真的出现这条 SQL 文本（剥注释后）。
	//   ★★★ 第一版的非 shell 分支【漏了这一步】⇒ 判据恒真 ⇒ Go 树全成候选
	//   ⇒⇒⇒ ⇒ 所以两条分支都必须先过这一关。
	found := false
	for _, ln := range lines {
		if re.MatchString(ln) {
			found = true
			break
		}
	}
	if !found {
		return false
	}
	// ★★ 窗口宽度必须【语言感知】：
	//   shell ⇒ psql 调用与 SQL 紧邻 ⇒ ±3 行窗口成立（沿用并行会话那套量具的判据形态）
	//   python/go ⇒ SQL 常写在**多行 f-string / 反引号**里，真正执行它的
	//   `run_sql(...)` / `admin.Exec(...)` 可能隔了十几行 ⇒ ⇒ ±3 窗口会漏
	//   ⇒ ⇒ ★ 所以这两类语言用【文件级】执行上下文判定。
	if lang != "shell" {
		for _, ln := range lines {
			if ctxRe.MatchString(ln) {
				return true
			}
		}
		return false
	}
	for i, ln := range lines {
		if !re.MatchString(ln) {
			continue
		}
		lo, hi := i-3, i+3
		if lo < 0 {
			lo = 0
		}
		if hi >= len(lines) {
			hi = len(lines) - 1
		}
		for j := lo; j <= hi; j++ {
			if j == i && !ctxRe.MatchString(ln) {
				continue
			}
			if ctxRe.MatchString(lines[j]) {
				return true
			}
		}
	}
	return false
}

// scqHasBackground 判断脚本会不会起【后台子进程】。
//
// ★ 逐行扫并排除这些不是后台启动的 `&`：`&&`、`2>&1`/`>&2`、`&>`、`|&`。
func scqHasBackground(src string) bool {
	for _, ln := range strings.Split(src, "\n") {
		t := strings.TrimSpace(ln)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if scqReDockerRunD.MatchString(ln) {
			return true
		}
		// ★★ 只看【命令层】的 `&` ⇒ 引号内的不算。
		//   ★★ 手工核对抓到一处假阳性：scripts/omnifree/e2e-verify.sh 第 86 行
		//      `pass "psql & go 已安装"` ⇒ 引号里的 `&` 后面还跟空格
		//      ⇒ 「& 后面是空白就当后台启动」这条规则会误判。
		inS, inD := false, false
		for i := 0; i < len(ln); i++ {
			c := ln[i]
			switch {
			case c == '\\' && i+1 < len(ln):
				i++ // 跳过转义字符
				continue
			case c == '\'' && !inD:
				inS = !inS
				continue
			case c == '"' && !inS:
				inD = !inD
				continue
			}
			if inS || inD || c != '&' {
				continue
			}
			if i+1 < len(ln) && ln[i+1] == '&' {
				i++
				continue
			}
			if i > 0 && (ln[i-1] == '>' || ln[i-1] == '|') {
				continue
			}
			if i+1 < len(ln) && ln[i+1] == '>' {
				continue
			}
			if i+1 == len(ln) || ln[i+1] == ' ' || ln[i+1] == '\t' {
				return true
			}
		}
	}
	return false
}

// scqTrapSignals 抽出一份文件里 trap 注册过的信号集合。
func scqTrapSignals(src string) map[string]bool {
	sigs := map[string]bool{}
	for _, m := range scqReTrapLine.FindAllString(src, -1) {
		for _, tok := range scqReSignal.FindAllString(m, -1) {
			sigs[strings.ToUpper(tok)] = true
		}
	}
	return sigs
}

// ── 覆盖性判据：兜底「载体」是否真的包着回收动作 ───────────────────────────
//
// ★★★ 这是段五对段四的**订正**：段四判「有没有 trap」（存在性），
//   段五判「trap/atexit/defer **指到的那个载体**里有没有 DROP DATABASE」（覆盖性）。
//   ⇒ 差别不是措辞：★ 三个 verify_892_*.sh 的 trap 只删临时文件 ⇒ 存在性=yes、覆盖性=no。

// scqLastSeg 取标识符的最后一段（去掉命名空间前缀）。
//
// ★ 仓内实例：scripts/_lib/db-init-lib.sh 第 793 行是 `db_init::_drop_verify_db() {`，
//
//	第 798 行是 `trap 'db_init::_drop_verify_db' EXIT` ⇒ ⇒ 两侧都要按最后一段比对。
func scqLastSeg(s string) string {
	if i := strings.LastIndex(s, "::"); i >= 0 {
		return s[i+2:]
	}
	return s
}

// scqShellFallbackCoversDrop：有没有一个 `trap <cmd> EXIT`，其 <cmd> 指向的函数体里含 DROP。
//
// ★★ 两个必须处理的细节：
//
//	① `trap - EXIT` 是【解除 trap】（正常跑完时交还调用方）⇒ 必须排除
//	  （仓内实例：measure-factsource-cost-matrix.sh 第 333 行、probe-onetime-db-shape.sh 第 247 行）；
//	② shell 的函数名可能带命名空间前缀（db_init::_drop_verify_db）⇒ 按最后一段比对。
func scqShellFallbackCoversDrop(src string) bool {
	funcs := scqShellFuncs(src)
	bySeg := map[string]string{}
	for k, v := range funcs {
		bySeg[scqLastSeg(k)] = v
	}
	for _, m := range scqReTrapExitCmd.FindAllStringSubmatch(src, -1) {
		cmd := strings.TrimSpace(m[1])
		if cmd == "-" || cmd == "--" {
			continue // ★ trap - EXIT = 解除，不是兜底
		}
		for _, id := range scqIdentRe.FindAllString(cmd, -1) {
			if body, ok := bySeg[scqLastSeg(id)]; ok && scqReDropDB.MatchString(body) {
				return true
			}
		}
	}
	return false
}

// scqPyBlockOf 取 lines[defIdx] 之后、缩进大于 indent 的那一段（python 的函数体/finally 块）。
func scqPyBlockOf(lines []string, defIdx int, indent int) string {
	var out []string
	for i := defIdx + 1; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if t == "" {
			out = append(out, lines[i])
			continue
		}
		ind := len(lines[i]) - len(strings.TrimLeft(lines[i], " \t"))
		if ind <= indent {
			break
		}
		out = append(out, lines[i])
	}
	return strings.Join(out, "\n")
}

// scqPyFallbackCoversDrop：python 侧两种载体 —— ① atexit.register(fn)，fn 的函数体里有 DROP；
// ② `finally:` 块里有 DROP。
func scqPyFallbackCoversDrop(src string) bool {
	lines := strings.Split(src, "\n")
	// ① atexit.register(fn)
	for _, m := range scqRePyAtexitReg.FindAllStringSubmatch(src, -1) {
		name := m[1]
		for i, ln := range lines {
			mm := scqRePyDef.FindStringSubmatch(ln)
			if mm == nil || mm[2] != name {
				continue
			}
			if scqReDropDB.MatchString(scqPyBlockOf(lines, i, len(mm[1]))) {
				return true
			}
		}
	}
	// ② finally: 块
	for i, ln := range lines {
		mm := scqRePyFinally.FindStringSubmatch(ln)
		if mm == nil {
			continue
		}
		if scqReDropDB.MatchString(scqPyBlockOf(lines, i, len(mm[1]))) {
			return true
		}
	}
	return false
}

// scqGoBraceBlock 从 src[openIdx]（必须是 '{'）起做花括号配对，返回整个块。
//
// ★ 跳过字符串字面量（"…" / `…` / '…'）与 // 行注释 ⇒ 否则 SQL 里的 '{' 会让配对跑偏。
func scqGoBraceBlock(src string, openIdx int) string {
	depth := 0
	i := openIdx
	for i < len(src) {
		c := src[i]
		switch {
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
			continue
		case c == '"' || c == '\'':
			q := c
			i++
			for i < len(src) && src[i] != q {
				if src[i] == '\\' {
					i++
				}
				i++
			}
			i++
			continue
		case c == '`':
			i++
			for i < len(src) && src[i] != '`' {
				i++
			}
			i++
			continue
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return src[openIdx : i+1]
			}
		}
		i++
	}
	return src[openIdx:]
}

// scqGoFallbackCoversDrop：go 侧三种载体 —— ① t.Cleanup(func(){…})；② defer func(){…}()；
// ③ t.Cleanup(具名函数) / defer 具名函数。
//
// ★★ 判据只认「**闭包/函数**形态」⇒ 不会把 `defer cancel()` / `defer conn.Close()`
//
//	这种【取消 ctx / 关连接】的 defer 误认成「回收兜底」。
func scqGoFallbackCoversDrop(src string) bool {
	for _, re := range []*regexp.Regexp{scqReGoCleanupFn, scqReGoDeferFn} {
		for _, loc := range re.FindAllStringIndex(src, -1) {
			open := strings.Index(src[loc[0]:loc[1]], "{")
			if open < 0 {
				continue
			}
			if scqReDropDB.MatchString(scqGoBraceBlock(src, loc[0]+open)) {
				return true
			}
		}
	}
	// ③ 具名函数形态
	for _, m := range scqReGoCleanupNm.FindAllStringSubmatch(src, -1) {
		name := m[1]
		for _, fm := range scqReGoFuncDef.FindAllStringSubmatchIndex(src, -1) {
			if src[fm[2]:fm[3]] != name {
				continue
			}
			open := strings.Index(src[fm[1]:], "{")
			if open < 0 {
				continue
			}
			if scqReDropDB.MatchString(scqGoBraceBlock(src, fm[1]+open)) {
				return true
			}
		}
	}
	return false
}

// scqFallbackCoversDrop 按语言判「兜底载体是否包着回收动作」。
func scqFallbackCoversDrop(src string, lang string) bool {
	switch lang {
	case "python":
		return scqPyFallbackCoversDrop(src)
	case "go":
		return scqGoFallbackCoversDrop(src)
	default:
		return scqShellFallbackCoversDrop(src)
	}
}

// scqAxes 是一个脚本的兜底质量轴。
type scqAxes struct {
	Lang              string // shell / python / go
	FallbackCovers    bool   // ★ A：兜底「载体」里确实包着回收动作（★ 覆盖性，不是存在性）
	SignalTrap        bool   // B：有没有接管信号（存在性；★ 是否必需由【语言映射表】决定）
	CleanupTimeout    bool   // C1
	CleanupDisconnect bool   // C2
	ChildReap         bool   // D
	ChildApplies      bool   // D 是否适用（文件确实会起后台子进程）
}

// scqGuarded 清理语句是否带【防卡保护】（两种机制任一）。
func (a scqAxes) scqGuarded() bool { return a.CleanupTimeout || a.CleanupDisconnect }

// scqDeficits 算出「为了合格还缺什么」。
//
// ★★ 缺陷名只取【真正有风险的缺口】：
//   - fallback      ★ 兜底载体**不覆盖回收动作**（★ 段五订正：段四判的是「存在性」）
//     ⇒ 提前退出跳过回收（实测攒下 7 个残留库）
//     ⇒ ★★ 存在性 ≠ 覆盖性：verify_892_local.sh 的 `trap 'rm -f "$TMP"' EXIT`
//     ⇒   只删临时文件、**不回收库** ⇒ 段四读成「有兜底」是**假阳性方向**。
//   - cleanup_guard 清理语句无防卡保护 ⇒ 慢 DROP 会在退出时卡死
//   - signal_trap   没接管信号 ⇒ ★★ 仅 python/go 必需（实测：atexit / try-finally / defer
//     在 SIGTERM 下【不执行】；而 shell 的 EXIT trap【会执行】⇒ shell 无需信号分支）
//   - child_reap    【仅当有后台子进程】没 kill+wait ⇒ 孤儿子进程在 cleanup 之后建出资源
func scqDeficits(a scqAxes) []string {
	var miss []string
	if !a.FallbackCovers {
		miss = append(miss, "fallback")
	}
	if !a.scqGuarded() {
		miss = append(miss, "cleanup_guard")
	}
	// ★★ 「信号接管」是否必需，**由语言映射表决定**（scripts/ursmcheck/selfcreated_cleanup_language_map.tsv）
	//   ⇒ 表里 signal_covers_sigterm=yes ⇒ 该语言的兜底在信号终止下会执行 ⇒ 不必额外接管信号
	//   ⇒ 表里 no ⇒ 必须显式接管信号
	//   ⇒★ ★ 判据的唯一依据是那张表（表驱动）⇒ 代码与表不可能不一致。
	if !a.SignalCoversSigterm() && !a.SignalTrap {
		miss = append(miss, "signal_trap")
	}
	if a.ChildApplies && !a.ChildReap {
		miss = append(miss, "child_reap")
	}
	sort.Strings(miss)
	return miss
}

// SignalCoversSigterm 该语言的兜底机制在【进程被 SIGTERM 终止】时会不会执行。
//
// ★ 读自语言映射表（表驱动 ⇒ 语义只有一处事实源）。
func (a scqAxes) SignalCoversSigterm() bool {
	return scqLangMap[a.Lang].SignalCoversSigterm
}

// scqLangOf 按扩展名判语言。
func scqLangOf(path string) string {
	switch {
	case strings.HasSuffix(path, ".py"):
		return "python"
	case strings.HasSuffix(path, ".go"):
		return "go"
	default:
		return "shell"
	}
}

// scqCompute 从源码重算各轴（★ 语言感知）。
func scqCompute(src string, lang string) scqAxes {
	ax := scqAxes{Lang: lang}
	// ★ A：兜底【覆盖性】—— 载体里是否真的包着回收动作（★ 段五新增，见 scqFallbackCoversDrop）。
	ax.FallbackCovers = scqFallbackCoversDrop(src, lang)
	// ★ B：信号接管的【存在性】—— 按语言各认各的形态。
	//   ★ 是否【必需】不由这里决定，而由语言映射表的 signal_covers_sigterm 决定（见 scqDeficits）。
	switch lang {
	case "python":
		ax.SignalTrap = scqRePySignal.MatchString(src)
	case "go":
		ax.SignalTrap = scqReGoSignal.MatchString(src)
	default:
		sigs := scqTrapSignals(src)
		ax.SignalTrap = sigs["INT"] || sigs["TERM"] || sigs["QUIT"] || sigs["HUP"]
	}
	// ★ 防卡保护：go 侧还有「客户端 ctx」这一种形态（context.WithTimeout）
	//   ⇒ shell/python 只能用服务端的 statement_timeout / pg_terminate_backend。
	if lang == "go" && scqReGoTimeout.MatchString(src) {
		ax.CleanupTimeout = true
	}

	funcs := scqShellFuncs(src)
	// ★★ R98 轮修：逻辑命令的切分必须【先剥注释】⇒ 否则一条含撇号的英文注释
	//   会把引号奇偶性带偏 ⇒ 其后所有行被并成同一条命令（见 scqLogicalCommands 里的说明）。
	//   ★ scqStripComments 只清空「整行都是注释」的行、保留行数 ⇒ 行号定位不受影响。
	cmds := scqLogicalCommands(strings.Join(scqStripComments(src, lang), "\n"))

	// C1 / C2：清理语句自带防卡保护。
	// ★★ 判据落在【含 DROP DATABASE 的那条逻辑命令】上（或紧邻它的前一条命令）
	//   ⇒⇒ 「全文件里出现过 timeout」不行（会把 go test / Python 的 timeout 吃进来）；
	//   ⇒⇒ 「函数体里出现过 timeout」也不行（shell 函数边界在含 heredoc 的文件里不可靠）。
	for i, c := range cmds {
		if !scqReDropDB.MatchString(c) {
			continue
		}
		hitTO, hitDC := false, false
		if scqReTimeout.MatchString(c) {
			hitTO = true
		}
		if scqReDisconnect.MatchString(c) {
			hitDC = true
		}
		if i > 0 {
			// ★★ R98 轮修：往回找【最近的一条非空命令】⇒ 注释/空行（已被剥成空串）
			//   **不打断**相邻性（注释不改变执行顺序）⇒ 而一条【真命令】会打断。
			//   ★ 这条修法的由来：scripts/sync-gateway-db-to-211.sh 的 terminate 与 DROP
			//   之间只隔着 3 行注释 ⇒ 语义上相邻，却因「相邻 = 上一条命令」而断链
			//   ⇒ 而那个文件自己的注释里就写着「先 pg_terminate_backend 再 DROP」的范例。
			for j := i - 1; j >= 0; j-- {
				if strings.TrimSpace(cmds[j]) == "" {
					continue
				}
				if scqReTimeout.MatchString(cmds[j]) {
					hitTO = true
				}
				if scqReDisconnect.MatchString(cmds[j]) {
					hitDC = true
				}
				break
			}
		}
		if hitTO {
			ax.CleanupTimeout = true
		}
		if hitDC {
			ax.CleanupDisconnect = true
		}
	}

	// D：会不会起后台子进程；起了的话，清理前有没有 kill + wait。
	// ★★ 这里【按函数体】找 kill/wait 是安全的：kill/wait 只可能出现在 cleanup 函数里，
	//   不会出现在内嵌 Python / Go 代码段里。
	ax.ChildApplies = scqHasBackground(src)
	if ax.ChildApplies {
		for _, b := range funcs {
			if scqReDropDB.MatchString(b) && scqReKillWait.MatchString(b) {
				ax.ChildReap = true
			}
		}
		if !ax.ChildReap {
			for _, c := range cmds {
				if scqReDropDB.MatchString(c) && scqReKillWait.MatchString(c) {
					ax.ChildReap = true
				}
			}
		}
		if !ax.ChildReap {
			for _, m := range scqReTrapLine.FindAllString(src, -1) {
				for _, tok := range regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\s*$`).FindAllStringSubmatch(m, -1) {
					if b, ok := funcs[tok[1]]; ok && scqReKillWait.MatchString(b) {
						ax.ChildReap = true
					}
				}
			}
		}
	}
	return ax
}

// ── 台账 ────────────────────────────────────────────────────────────────────

// scqRow 是台账的一行。
type scqRow struct {
	File              string
	Language          string // shell/python/go
	FallbackCovers    string // yes/no —— ★ 兜底【覆盖性】：载体里是否包着回收动作
	SignalTrap        string // yes/no
	CleanupTimeout    string // yes/no
	CleanupDisconnect string // yes/no
	ChildReap         string // yes/no/na
	Origin            string // static/manual
	Verdict           string // 合格/待修/无回收义务
	Deficits          string // 逗号分隔的缺失项名（待修行用）
	Evidence          string
}

var scqValidVerdict = map[string]bool{"合格": true, "待修": true, "无回收义务": true}

// scqReadLedger 读台账。
func scqReadLedger(t *testing.T) []scqRow {
	t.Helper()
	fh, err := os.Open(scqLedger)
	if err != nil {
		t.Fatalf("打开台账失败: %v", err)
	}
	defer fh.Close()
	var rows []scqRow
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	ln := 0
	for sc.Scan() {
		ln++
		text := sc.Text()
		if strings.TrimSpace(text) == "" || strings.HasPrefix(strings.TrimSpace(text), "#") {
			continue
		}
		f := strings.Split(text, "\t")
		if len(f) != 11 {
			t.Fatalf("台账第 %d 行列数 = %d，期望 11\n%s", ln, len(f), text)
		}
		rows = append(rows, scqRow{
			File: f[0], Language: f[1], FallbackCovers: f[2], SignalTrap: f[3],
			CleanupTimeout: f[4], CleanupDisconnect: f[5], ChildReap: f[6],
			Origin: f[7], Verdict: f[8], Deficits: f[9], Evidence: f[10],
		})
	}
	return rows
}

// scqCandidateFiles 扫全仓，找出「自建资源 + 自己回收（DROP DATABASE）」的脚本。
//
// ★★ 扫描口径 = scqScopeDirs（scripts/ + internal/ + cmd/）⇒ 与并行会话那套量具一致
//
//	⇒⇒ 两个台账的差集才是【轴差】而不是【口径差】。
func scqCandidateFiles(t *testing.T) []string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("取仓库根失败: %v", err)
	}
	var out []string
	err = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			base := filepath.Base(p)
			if base == ".git" || base == "vendor" || base == "node_modules" {
				return filepath.SkipDir
			}
			// ★ 口径外的一级目录直接跳过（不逐目录枚举再比对，避免口径漂移）
			rel, rerr := filepath.Rel(root, p)
			if rerr != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
			if rel != "." {
				top := rel
				if i := strings.IndexByte(rel, '/'); i >= 0 {
					top = rel[:i]
				}
				inScope := false
				for _, d := range scqScopeDirs {
					if top == d {
						inScope = true
						break
					}
				}
				if !inScope {
					return filepath.SkipDir
				}
			}
			return nil
		}
		ext := filepath.Ext(p)
		if ext != ".sh" && ext != ".py" && ext != ".go" {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if scqSelfFiles[rel] {
			return nil // ★ 量具脚本自匹配 ⇒ 排除自己
		}
		data, rerr2 := os.ReadFile(p)
		if rerr2 != nil {
			return nil
		}
		src := string(data)
		lang := scqLangOf(rel)
		lines := scqStripComments(src, lang)
		// ★ 「自建」：CREATE DATABASE 真在执行（剥注释 + 执行上下文）
		//   ⇒ 或它会起 docker 容器（容器里的库是它自己建的）
		creates := scqHasExecContext(lines, scqReCreateDB, lang) || scqReCreateCT.MatchString(src)
		if !creates {
			return nil
		}
		// ★ 「自回收」：DROP DATABASE 真在执行
		if !scqHasExecContext(lines, scqReDropDB, lang) {
			return nil
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("扫仓失败: %v", err)
	}
	sort.Strings(out)
	return out
}

// ── 判据 ① 账实相符 + ② 双向候选集 + ③④⑤⑥ ───────────────────────────────

func TestSelfCreatedCleanupQualityLedger(t *testing.T) {
	rows := scqReadLedger(t)
	if len(rows) == 0 {
		t.Fatal("台账为空 ⇒ 分母为 0，拒绝给结论")
	}

	// ⑥ 结构不变量
	seen := map[string]bool{}
	for i, r := range rows {
		ln := i + 1
		if r.File == "" {
			t.Errorf("台账第 %d 行 file 为空", ln)
			continue
		}
		if seen[r.File] {
			t.Errorf("台账里 %s 出现多次", r.File)
		}
		seen[r.File] = true
		if r.Language != "shell" && r.Language != "python" && r.Language != "go" {
			t.Errorf("台账 %s：language = %q 非法（只允许 shell/python/go）", r.File, r.Language)
		}
		for _, v := range []string{r.FallbackCovers, r.SignalTrap, r.CleanupTimeout, r.CleanupDisconnect} {
			if v != "yes" && v != "no" {
				t.Errorf("台账 %s：轴值 %q 非法（只允许 yes/no）", r.File, v)
			}
		}
		if r.ChildReap != "yes" && r.ChildReap != "no" && r.ChildReap != "na" {
			t.Errorf("台账 %s：child_reap = %q 非法（只允许 yes/no/na）", r.File, r.ChildReap)
		}
		if r.Origin != "static" && r.Origin != "manual" {
			t.Errorf("台账 %s：origin = %q 非法（只允许 static/manual）", r.File, r.Origin)
		}
		if !scqValidVerdict[r.Verdict] {
			t.Errorf("台账 %s：verdict = %q 非法（只允许 合格/待修/无回收义务）", r.File, r.Verdict)
		}
		if strings.TrimSpace(r.Evidence) == "" {
			t.Errorf("台账 %s：evidence 为空（每行必须写清依据）", r.File)
		}
	}

	// ② 双向候选集
	cands := scqCandidateFiles(t)
	candSet := map[string]bool{}
	for _, c := range cands {
		candSet[c] = true
	}
	for _, c := range cands {
		if !seen[c] {
			t.Errorf("★ 新候选未登记：%s\n"+
				"  ⇒ 它自建资源且自回收 ⇒ 必须登记进台账；\n"+
				"  ⇒ 「合格」的定义 = 有 EXIT trap ∧ 清理语句自带防卡保护 ∧\n"+
				"  ⇒   (无后台子进程 ∨ (接管 INT/TERM ∧ 先 kill+wait 收割))", c)
		}
	}
	// ★★ origin 自洽：
	//   origin=static ⇒ 必须能被静态候选集认出（否则说明它已经不该是 static ⇒ 改 origin 或删行）
	//   origin=manual ⇒ 必须【不能】被静态候选集认出（能认出的就该是 static）
	//   ⇒⇒★ manual 这一档的存在理由：**静态候选集判据会漏掉「经由调用别的脚本自建资源」的对象**
	//   ⇒⇒★ 而那个对象恰恰是本仓唯一有「后台子进程 + kill+wait 收割」形态的脚本
	//   ⇒⇒★ ⇒ 若只靠静态候选集，D 轴（收割）就会永远无对象 ⇒ 门会漏掉真正需要收割的那个脚本。
	for _, r := range rows {
		inCand := candSet[r.File]
		switch r.Origin {
		case "static":
			if !inCand {
				t.Errorf("★ 台账里的 %s 标 origin=static，但静态候选集认不出它\n"+
					"  ⇒ 若它已不是「自建资源 + 自回收」⇒ 请删行；\n"+
					"  ⇒ 若它仍是这样但静态判据认不出 ⇒ 请改 origin=manual 并在 evidence 里说明", r.File)
			}
		case "manual":
			if inCand {
				t.Errorf("台账里的 %s 标 origin=manual，但静态候选集能认出它 ⇒ 请改 origin=static", r.File)
			}
		}
	}

	// ① 账实相符 + ③④
	for _, r := range rows {
		data, err := os.ReadFile(filepath.Join("../..", r.File))
		if err != nil {
			t.Errorf("读 %s 失败: %v", r.File, err)
			continue
		}
		ax := scqCompute(string(data), scqLangOf(r.File))
		yn := func(b bool) string {
			if b {
				return "yes"
			}
			return "no"
		}
		gotReap := "na"
		if ax.ChildApplies {
			gotReap = yn(ax.ChildReap)
		}
		if r.Language != ax.Lang {
			t.Errorf("★ %s 的 language 与从扩展名推出的不一致：台账=%s 重算=%s",
				r.File, r.Language, ax.Lang)
		}
		if r.FallbackCovers != yn(ax.FallbackCovers) || r.SignalTrap != yn(ax.SignalTrap) ||
			r.CleanupTimeout != yn(ax.CleanupTimeout) || r.CleanupDisconnect != yn(ax.CleanupDisconnect) ||
			r.ChildReap != gotReap {
			t.Errorf("★ %s 账实不符\n  台账: fallback=%s signal=%s timeout=%s disconnect=%s reap=%s\n"+
				"  重算: fallback=%s signal=%s timeout=%s disconnect=%s reap=%s\n"+
				"  ⇒ 源码改了必须同步台账（这正是本门要抓的「改了没登记」）",
				r.File, r.FallbackCovers, r.SignalTrap, r.CleanupTimeout, r.CleanupDisconnect, r.ChildReap,
				yn(ax.FallbackCovers), yn(ax.SignalTrap), yn(ax.CleanupTimeout), yn(ax.CleanupDisconnect), gotReap)
		}

		miss := strings.Join(scqDeficits(ax), ",")
		switch r.Verdict {
		case "合格":
			if miss != "" {
				t.Errorf("★ %s 标了「合格」，但重算出来还缺 %s\n"+
					"  ⇒ 「合格」的定义见本文件头 ⇒ ⇒ 不能标合格", r.File, miss)
			}
			if r.Deficits != "" {
				t.Errorf("台账 %s：verdict=合格 但 deficits 非空（%q）", r.File, r.Deficits)
			}
		case "待修":
			if r.Deficits != miss {
				t.Errorf("★ %s 的 deficits = %q，重算缺失项 = %q\n"+
					"  ⇒ deficits 必须逐项等于重算结果 ⇒ ⇒ 补上后要同步台账 ⇒ ⇒ 缺陷集只减不增",
					r.File, r.Deficits, miss)
			}
		case "无回收义务":
			if r.Deficits != "" {
				t.Errorf("台账 %s：verdict=无回收义务 但 deficits 非空（%q）", r.File, r.Deficits)
			}
			// ★ 无回收义务有三种正当理由：长驻 / 静态假阳性 / 根本不建库
			//   （第三、第四种是并行会话 R94 续九台账里已登记的裁决类别）
			ok := false
			for _, marker := range []string{"长驻", "假阳性", "不建库"} {
				if strings.Contains(r.Evidence, marker) {
					ok = true
				}
			}
			if !ok {
				t.Errorf("台账 %s：verdict=无回收义务 ⇒ evidence 必须写明「长驻」/「假阳性」/「不建库」", r.File)
			}
		}
	}
}

// ── 量具自检：探针必须给出不同读数（防整门恒绿）─────────────────────────────

// TestSelfCreatedCleanupQualityProbesDistinct 量具自检。
func TestSelfCreatedCleanupQualityProbesDistinct(t *testing.T) {
	// 探针 1：合格形态（EXIT trap + statement_timeout + 收割 + 接管信号）
	ok := `#!/usr/bin/env bash
cleanup() {
  if [ -n "$PID" ]; then kill "$PID" 2>/dev/null || true; wait "$PID" 2>/dev/null || true; fi
  psql -c "SET statement_timeout='15s'; DROP DATABASE IF EXISTS x" || true
}
trap cleanup EXIT
trap cleanup INT TERM
build & PID=$!
`
	// 探针 2：待修形态（有 EXIT trap，但无防卡保护、无信号接管、无收割）
	bad := `#!/usr/bin/env bash
cleanup() {
  psql -c "DROP DATABASE IF EXISTS x" || true
}
trap cleanup EXIT
build & PID=$!
`
	// 探针 3：超时标记出现在【别的命令】里（`go test -timeout 600s`）⇒ 不得被判成
	//	「清理语句自带防卡保护」⇒ 这是上一版判据太宽的坑。
	mis := `#!/usr/bin/env bash
cleanup() {
  psql -c "DROP DATABASE IF EXISTS x" || true
}
trap cleanup EXIT
go test -timeout 600s ./...
`
	// 探针 4：合格形态的另一种（pg_terminate_backend 类）⇒ 判据要认仓内既有实践
	dis := `#!/usr/bin/env bash
cleanup() {
  psql -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='x';" || true
  psql -c "DROP DATABASE IF EXISTS x" || true
}
trap cleanup EXIT
`

	// ★★ 探针 5（★ 段五新增）：python 形态 —— atexit 注册的函数里包着回收
	//   ⇒ 兜底【覆盖性】= yes；但【无信号接管】⇒ ★ 必须缺 signal_trap
	py := `import atexit, os

def _drop_db() -> None:
    try:
        run_sql("DROP DATABASE IF EXISTS x;")
    except Exception:
        pass

atexit.register(_drop_db)
`
	// ★★ 探针 6（★ 段五新增）：go 形态 —— t.Cleanup 闭包里包着回收 + context.WithTimeout
	//   ⇒ 覆盖性=yes、防卡=yes（客户端 ctx）；但【无信号接管】⇒ ★ 必须缺 signal_trap
	goSrc := `package main

import "context"

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	t.Cleanup(func() {
		admin.Exec(ctx, "DROP DATABASE IF EXISTS x")
	})
}
`
	// ★★ 探针 7（★ 段五新增）：★ 「存在性 ≠ 覆盖性」的反例 ——
	//   EXIT trap 只删临时文件，**不包着回收** ⇒ 覆盖性 = no ⇒ 必须缺 fallback。
	//   ★★ 这正是仓内 verify_892_local.sh 第 26 行的真实形态
	//   （段四把它读成「有兜底」⇒ 假阳性方向）。
	notCovered := `#!/usr/bin/env bash
TMP="$(mktemp /tmp/x_XXXXXX.sql)"
trap 'rm -f "$TMP"' EXIT
psql -c "SET statement_timeout='15s'; DROP DATABASE IF EXISTS x" || true
psql -c "CREATE DATABASE x" || true
`
	// ★★ 探针 8（★ 段五新增）：python 的合格形态（atexit + 信号接管 + 服务端超时）
	pyOK := `import atexit, os, signal, sys

def _drop_db() -> None:
    try:
        run_sql("SET statement_timeout='15s'; DROP DATABASE IF EXISTS x;")
    except Exception:
        pass

def _on_term(signum, frame):
    _drop_db()
    sys.exit(143)

signal.signal(signal.SIGTERM, _on_term)
atexit.register(_drop_db)
`

	axOK := scqCompute(ok, "shell")
	if !axOK.FallbackCovers || !axOK.SignalTrap || !axOK.CleanupTimeout || !axOK.ChildApplies || !axOK.ChildReap {
		t.Errorf("探针 1（合格形态）判据读数不对: %+v\n"+
			"  ⇒ 期望 fallback=yes signal=yes timeout=yes reap=yes", axOK)
	}
	if d := scqDeficits(axOK); len(d) != 0 {
		t.Errorf("探针 1（合格形态）不应有缺失项，实际 = %v", d)
	}

	axBad := scqCompute(bad, "shell")
	if !axBad.FallbackCovers {
		t.Errorf("探针 2 应认出「EXIT trap 包着回收」: %+v", axBad)
	}
	if axBad.CleanupTimeout || axBad.CleanupDisconnect {
		t.Errorf("★ 探针 2 不该判成「清理语句带防卡保护」: %+v", axBad)
	}
	if axBad.SignalTrap {
		t.Errorf("探针 2 应判成无信号接管: %+v", axBad)
	}
	if !axBad.ChildApplies || axBad.ChildReap {
		t.Errorf("探针 2 有后台子进程但无收割 ⇒ 期望 reap=no: %+v", axBad)
	}

	axMis := scqCompute(mis, "shell")
	if axMis.CleanupTimeout || axMis.CleanupDisconnect {
		t.Errorf("★ 探针 3：`go test -timeout 600s` 不在清理命令里 ⇒ 不得判成「清理语句带防卡保护」: %+v", axMis)
	}

	axDis := scqCompute(dis, "shell")
	if !axDis.CleanupDisconnect {
		t.Errorf("探针 4 应认出 pg_terminate_backend 类防卡保护: %+v", axDis)
	}
	if d := scqDeficits(axDis); len(d) != 0 {
		t.Errorf("探针 4（pg_terminate_backend 形态）不应有缺失项，实际 = %v", d)
	}

	// ★★ 探针 5/6：python 与 go 的兜底【覆盖性】要成立，且【信号接管】必须成为缺失项
	axPy := scqCompute(py, "python")
	if !axPy.FallbackCovers {
		t.Errorf("★ 探针 5：atexit 注册的函数里包着回收 ⇒ 覆盖性应为 yes: %+v", axPy)
	}
	if axPy.SignalTrap {
		t.Errorf("探针 5：python 无信号接管 ⇒ 应判成 no: %+v", axPy)
	}
	if d := strings.Join(scqDeficits(axPy), ","); d != "cleanup_guard,signal_trap" {
		t.Errorf("★ 探针 5 的缺失项 = %q，期望 %q\n"+
			"  ⇒ python 侧：atexit 在 SIGTERM 下【不执行】⇒ signal_trap 必须无条件必需", d, "cleanup_guard,signal_trap")
	}

	axGo := scqCompute(goSrc, "go")
	if !axGo.FallbackCovers {
		t.Errorf("★ 探针 6：t.Cleanup 闭包里包着回收 ⇒ 覆盖性应为 yes: %+v", axGo)
	}
	if !axGo.CleanupTimeout {
		t.Errorf("探针 6：context.WithTimeout 应判成「客户端防卡保护」: %+v", axGo)
	}
	if d := strings.Join(scqDeficits(axGo), ","); d != "signal_trap" {
		t.Errorf("★ 探针 6 的缺失项 = %q，期望 %q\n"+
			"  ⇒ go 侧：defer 在 SIGTERM 下【不执行】⇒ signal_trap 必须无条件必需", d, "signal_trap")
	}

	// ★★ 探针 7：★ 「存在性 ≠ 覆盖性」—— trap 只删临时文件 ⇒ 覆盖性 = no
	axNC := scqCompute(notCovered, "shell")
	if axNC.FallbackCovers {
		t.Errorf("★ 探针 7：EXIT trap 只 `rm -f $TMP`、不包着 DROP ⇒ 覆盖性必须判成 no: %+v", axNC)
	}
	if d := strings.Join(scqDeficits(axNC), ","); d != "fallback" {
		t.Errorf("★ 探针 7 的缺失项 = %q，期望 %q\n"+
			"  ⇒ ★ 这就是「有 trap ≠ 兜底覆盖了要回收的资源」的判据落点", d, "fallback")
	}

	// ★★ 探针 8：python 的合格形态 ⇒ 无缺失项
	axPyOK := scqCompute(pyOK, "python")
	if !axPyOK.FallbackCovers || !axPyOK.SignalTrap {
		t.Errorf("探针 8（python 合格形态）读数不对: %+v", axPyOK)
	}
	if d := scqDeficits(axPyOK); len(d) != 0 {
		t.Errorf("探针 8（python 合格形态）不应有缺失项，实际 = %v", d)
	}

	// ★★ 探针 9/10/11（★ R98 轮新增）：★★ 把「逻辑命令切分 + 相邻性」这两条规则钉死。
	//
	//   探针 9  ★「非注释行里的奇数撇号」不得把后续行全并成一条命令
	//   探针 10 ★「紧邻的前一条命令」必须真的生效（中间隔一条**真命令** ⇒ 断链）
	//   探针 11 ★ 注释/空行**不打断**相邻性（语义上相邻 ⇒ 仍算守卫）
	//
	//   ⇒ 三条合起来覆盖了 R98 轮修的三处：切分只按续行 / 相邻性跳过空命令 / 剥注释。
	oddQuote := `#!/usr/bin/env bash
x="don't"
psql -c "DROP DATABASE IF EXISTS x WITH (FORCE);"
psql -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='x';" >/dev/null 2>&1 || true
`
	axOdd := scqCompute(oddQuote, "shell")
	if axOdd.CleanupDisconnect {
		t.Errorf("★ 探针 9：守卫标记在 DROP【之后】⇒ 不得判成 disconnect=yes: %+v\n"+
			"  ⇒ ★ 若这里判成 yes，说明切分器把「含奇数撇号的那行」之后的全部行并成了一条命令", axOdd)
	}
	if axOdd.CleanupTimeout {
		t.Errorf("★ 探针 9：DROP 所在命令里没有 SET ⇒ timeout 必须是 no: %+v", axOdd)
	}

	notAdjacent := `#!/usr/bin/env bash
cleanup() {
  psql -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='x' AND pid <> pg_backend_pid();" >/dev/null 2>&1 || true
  echo "中间隔一条真命令：terminate 与 DROP 不再相邻"
  psql -c "SET statement_timeout = '15s'" -c "DROP DATABASE IF EXISTS x WITH (FORCE);"
}
trap cleanup EXIT
`
	axNA := scqCompute(notAdjacent, "shell")
	if axNA.CleanupDisconnect {
		t.Errorf("★ 探针 10：terminate 与 DROP 之间隔了一条真命令 ⇒ 不得判成 disconnect=yes: %+v\n"+
			"  ⇒ 「紧邻的前一条命令」这条规则必须真的生效", axNA)
	}
	if !axNA.CleanupTimeout {
		t.Errorf("★ 探针 10：SET 与 DROP 在同一条命令里 ⇒ timeout 应判成 yes: %+v", axNA)
	}
	if !axNA.FallbackCovers {
		t.Errorf("★ 探针 10：trap → cleanup → DROP 的链条必须成立: %+v", axNA)
	}

	commentBetween := `#!/usr/bin/env bash
cleanup() {
  psql -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='x' AND pid <> pg_backend_pid();" >/dev/null 2>&1 || true
  # 中间隔一行注释：注释不改变执行顺序 ⇒ 语义上仍然相邻
  psql -c "SET statement_timeout = '15s'" -c "DROP DATABASE IF EXISTS x WITH (FORCE);"
}
trap cleanup EXIT
`
	axCB := scqCompute(commentBetween, "shell")
	if !axCB.CleanupDisconnect {
		t.Errorf("★ 探针 11：terminate 与 DROP 之间只隔着注释 ⇒ 语义上相邻 ⇒ disconnect 应判成 yes: %+v\n"+
			"  ⇒ ★ 真实文件 scripts/sync-gateway-db-to-211.sh 就是这个形态（它自己写着「先 terminate 再 DROP」）", axCB)
	}
	if !axCB.CleanupTimeout {
		t.Errorf("★ 探针 11：SET 与 DROP 在同一条命令里 ⇒ timeout 应判成 yes: %+v", axCB)
	}

	// ★★ 区分度的正确要求（★ 第一版写成「四个探针两两不同」是错的：
	//   探针 1 与探针 4 都是**合格**形态 ⇒ 它们的缺失项集合【本来就该相同】⇒
	//   ⇒ 那样写等于要求「合格形态之间必须不同」⇒ ⇒ 门会红在一个假问题上）。
	// ⇒ 正确要求：① 合格形态都无缺失项；② 不合格形态都有缺失项；
	//   ③ 不合格形态之间【必须两两不同】（否则其中一条判据其实没生效）。
	type probeCase struct {
		label string
		ax    scqAxes
	}
	passCases := []probeCase{
		{"1-合格shell", axOK}, {"4-合格disconnect", axDis}, {"8-合格python", axPyOK},
		{"10-真命令打断相邻", axNA}, {"11-注释不打断相邻", axCB},
	}
	failCases := []probeCase{
		{"2-无防卡无收割", axBad}, {"3-超时标记误命中", axMis},
		{"5-python无信号", axPy}, {"6-go无信号", axGo}, {"7-载体不覆盖回收", axNC},
		{"9-撇号把后续行并成一条", axOdd},
	}
	for _, p := range passCases {
		if d := scqDeficits(p.ax); len(d) != 0 {
			t.Errorf("合格形态的探针不该有缺失项: %s ⇒ %v", p.label, d)
		}
	}
	seen := map[string]string{}
	for _, p := range failCases {
		d := strings.Join(scqDeficits(p.ax), ",")
		if d == "" {
			t.Errorf("不合格形态的探针必须有缺失项: %s", p.label)
			continue
		}
		if prev, dup := seen[d]; dup {
			t.Errorf("★ 两个不合格探针的缺失项集合相同（%q）：%s 与 %s\n"+
				"  ⇒ 其中一条判据其实没生效（判据无区分度）", d, prev, p.label)
		}
		seen[d] = p.label
	}
}

// ── 量具自检：语言映射表是判据的唯一语义事实源 ─────────────────────────────

// TestSelfCreatedCleanupLanguageMapCoversLedger 验证「语言映射表」与台账/代码三方自洽。
//
// ★ 三条不变量：
//
//	① 台账里出现的每种语言，映射表里必须有行（否则该语言的语义无依据）；
//	② 映射表里的 fallback_mech 必须与代码侧认的机制一致（表与代码不许各说各话）；
//	③ 映射表里 signal_covers_sigterm=yes ⇒ 该语言【不需要】signal_trap 这一项；
//	   no ⇒ 【需要】⇒ ⇒ 也就是「判据的必需项由表驱动」。
func TestSelfCreatedCleanupLanguageMapCoversLedger(t *testing.T) {
	if scqLangMapErr != nil {
		t.Fatalf("★ 语言映射表读不出来 ⇒ 判据无事实源: %v", scqLangMapErr)
	}
	// ② 表里的机制名必须与代码侧一致
	wantMech := map[string][]string{
		"shell":  {"trap", "EXIT"},
		"python": {"atexit", "finally"},
		"go":     {"defer", "Cleanup"},
	}
	for lang, info := range scqLangMap {
		toks, ok := wantMech[lang]
		if !ok {
			t.Errorf("★ 语言映射表里的 %q 不在本门认识的语言集合内（shell/python/go）", lang)
			continue
		}
		hit := false
		for _, tk := range toks {
			if strings.Contains(info.FallbackMech, tk) {
				hit = true
				break
			}
		}
		if !hit {
			t.Errorf("★ 语言映射表 %s 的 fallback_mech = %q，与代码侧认识的机制（%v）对不上\n"+
				"  ⇒ 表与代码必须一致 ⇒ 要么改表，要么改代码", lang, info.FallbackMech, toks)
		}
	}
	// ① 台账里的每种语言都要在表内
	rows := scqReadLedger(t)
	langs := map[string]bool{}
	for _, r := range rows {
		langs[r.Language] = true
	}
	for lang := range langs {
		if _, ok := scqLangMap[lang]; !ok {
			t.Errorf("★ 台账里出现了语言 %q，但语言映射表里没有它\n"+
				"  ⇒ 该语言的「合格兜底」语义无依据 ⇒ 必须补映射表", lang)
		}
	}
	// ③ 必需项由表驱动：表里 signal_covers_sigterm=no 的语言 ⇒ 必须缺 signal_trap
	for _, r := range rows {
		info, ok := scqLangMap[r.Language]
		if !ok {
			continue
		}
		if info.SignalCoversSigterm {
			continue
		}
		ax := scqAxes{Lang: r.Language}
		if d := strings.Join(scqDeficits(ax), ","); !strings.Contains(d, "signal_trap") {
			t.Errorf("★ 语言 %s 的映射表说 signal_covers_sigterm=no ⇒ 缺 signal_trap 时必须报出来\n"+
				"  ⇒ 实际缺失项 = %q", r.Language, d)
		}
	}
}

// ── 量具自检：判据层与枚举层分离（从源码抽取，而不是复制）────────────────────

// TestSelfCreatedCleanupQualityUsesRepoFacts 验证判据本身不是空壳。
func TestSelfCreatedCleanupQualityUsesRepoFacts(t *testing.T) {
	if !scqReCreateDB.MatchString("CREATE DATABASE foo") {
		t.Fatal("scqReCreateDB 认不出 CREATE DATABASE ⇒ 候选集恒空 ⇒ 整门恒绿")
	}
	if scqReCreateDB.MatchString("SELECT 1") {
		t.Fatal("scqReCreateDB 误判 SELECT ⇒ 判据过宽")
	}
	if !scqReDropDB.MatchString("DROP DATABASE IF EXISTS foo") {
		t.Fatal("scqReDropDB 认不出 DROP DATABASE")
	}
	if scqReDropDB.MatchString("DROP TABLE foo") {
		t.Fatal("scqReDropDB 误判 DROP TABLE")
	}
	// ★ 判据必须认仓内既有实践：两个脚本用的是 pg_terminate_backend 类防卡
	if !scqReDisconnect.MatchString("SELECT pg_terminate_backend(pid) FROM pg_stat_activity") {
		t.Fatal("scqReDisconnect 认不出仓内既有的 pg_terminate_backend 形态")
	}
	// ★ 而 Python subprocess 的 timeout / go test 的 timeout 都不算
	if scqReTimeout.MatchString("subprocess.run(cmd, timeout=20)") {
		t.Fatal("★ scqReTimeout 误认 Python subprocess 的 timeout ⇒ 判据过宽")
	}
	if scqReTimeout.MatchString("go test -timeout 600s ./...") {
		t.Fatal("★ scqReTimeout 误认 go test 的 timeout ⇒ 判据过宽")
	}
	// 台账里必须真的引用了仓内存在的文件
	rows := scqReadLedger(t)
	for _, r := range rows {
		if _, err := os.Stat(filepath.Join("../..", r.File)); err != nil {
			t.Errorf("台账里的 %s 在仓内不存在: %v", r.File, err)
		}
	}
	fmt.Fprintf(os.Stderr, "scq 台账 %d 行；候选集 %d 个\n", len(rows), len(scqCandidateFiles(t)))
}

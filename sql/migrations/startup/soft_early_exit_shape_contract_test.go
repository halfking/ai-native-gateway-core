package startup

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// stripSQLComments 让判据只看见**可执行**的 SQL。
//
// 这不是洁癖：门自己的 `EXECUTE\s+` 会在注释里命中。给 816/817 写「守卫搬到
// 重建块内」的说明时，我在注释里写了「实测并发读方持锁时这条 EXECUTE 直接超时」
// ——`EXECUTE` 出现在注释中，与真 DDL 无法区分。若不剥离，任何人**在注释里提到
// 一个词**就能让这道门改判。判据必须只对可执行文本生效。
// TestToRegclassGuardIsNotFooledBySiblingDoBlock — startup 迁移里，
// `DO $$ … RETURN; $$` **不是脚本级早退**，而 816/817 用它守住了会崩的东西。
//
// PostgreSQL 的每个 `DO $$ … $$;` 是独立语句，块内 `RETURN` 只结束**该块**。
// 真库最小验证（一条命令）：
//
//	DO $$ BEGIN RAISE NOTICE '块1'; RETURN; END $$;   → NOTICE: 块1
//	DO $$ BEGIN RAISE NOTICE '块2'; END $$;           → NOTICE: 块2   ← 没被拦住
//
// 816 与 817 都是：块 1 用 `to_regclass('public.X') IS NULL` 判「视图链不全」，
// 打印 skipping 后 `RETURN`；块 2 却对**同一个 X** 硬写 `'public.X'::regclass`，
// 然后 `CREATE OR REPLACE VIEW`。守卫说"链不全就跳过"，块 2 说"链不全就崩"——
// 而且崩在守卫自己刚检查过的那个关系上。真库复现（事务已回滚）：
//
//	NOTICE: 817: view chain incomplete (680-incident shape); skipping — db.ensure rebuilds at startup
//	ERROR:  relation "public.request_logs_with_current_month_without_request_class_due_at" does not exist
//
// 安装器逐文件跑 `psql --single-transaction`，所以后果不是"少做一次重建"，
// 是**整条迁移失败 ⇒ 安装/升级中止**。
//
// 另一面是链完整时：块 1 判"已是目标形态"并 RETURN，块 2 仍无条件
// `CREATE OR REPLACE VIEW`（要 AccessExclusiveLock）。实测并发读方持锁时，
// 超时正落在该 EXECUTE 上，而迁移自己刚打印过 "nothing to do"。
//
// ── 为什么判据这么窄（第一版判据已经红过六次假阳性）──────────────────
// 粗判据「软早退后面还有块带 DDL」会把下面这些**全部**误判成缺陷：
//
//	765_bodies_columnar_storage
//	644_tuning_views_selfcheck_and_candidate_failure_cache
//	330 / 341 / 616 / 678_...
//
// 它们的形态是**守卫与动作同块**（RETURN@342 在同块 DDL@696 之前），
// RETURN 在块内确实拦得住后面那段——是正确的写法。所以判据收窄到
// 可判定的那一维：**守卫用 to_regclass 测存在性的关系，后续块又硬引用它。**
// 收窄后全目录 88 个多块迁移只命中 816/817 两条。
//
// ── 第三层收窄（816/817 修完之后加的）──────────────────────────────────
// 改成「每个块守自己要碰的关系」之后，这道门**重新报红**——跨块规则只能看见
// 「块 1 守了、块 2 直连」，看不见「块 2 也守了」。判据因此再收一层：只看
// **后续块自己没守**的那些硬引用。真正的缺陷是「A 守了、B 没守却直连」，
// 不是「A 曾经守过」。
// 收窄后必须重验它仍能抓到原缺陷：变异 M3 把 817 还原成改前形态 ⇒ 门红；
// 修复版 ⇒ 绿。**放宽判据后不重验，等于把门调到刚好不报。**
//
// 另记一条**形状相同但后果未判**的：330_usage_ledger_partition 块 1 打印
// "already partitioned, skipping migration" 后 RETURN，块 2 仍 `EXECUTE replace`。
// 块 2 没有硬引用守卫测过的关系，所以本门不报；但"跳过分区迁移"之后仍重建索引
// 是否越权，要逐条读语义才能定，不在结构判据的能力范围内。
//
// ── 已知欠账：登记而不是删断言 ─────────────────────────────────────────
// 下面两条**已经确认有缺陷，但本门对它们记为已知**，不判红。理由不是"放过"，
// 是 816/817 已在本机与部分环境应用并登记进 schema_migrations，而迁移自身的
// 头注写明「不直接改已应用迁移，否则『已跑过』与『文件内容』分叉」。因此
// 结构修复要等新迁移或显式裁决，不能在本轮偷偷改内容。
//
// 这个登记是**双向承重**的：登记项被真正修好（守卫折进同一块 / 守卫块变最后一块）
// 时本门会反过来报红，要求删掉登记项——否则这条欠账会在没人再提的时候长期静默。
// 这与本仓既有登记表门（TestInsertRequestLogPlaceholderAlignment 的「登记项失效
// 时门反过来报红」）同一范式。
// 816 与 817 已于 2026-10-03 修成同块形态（守卫与重建同块，RETURN 真的拦得住），
// 本表现已清空——留在这里只会让下一个写同样形状的人以为「已登记过，没关系」。
// 这张表的价值在于**反向判据**：条目被修好时门会反过来要求删登记。
var knownIneffectiveToRegclassGuards = map[string]string{}

// NOT TESTED HERE: 守卫选的关系对不对、重建语义是否正确。逐条真库验收。
func TestToRegclassGuardIsNotFooledBySiblingDoBlock(t *testing.T) {
	entries, err := filepath.Glob("*.sql")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("no startup migration files found — 门在空目录上恒绿")
	}

	doBlock := regexp.MustCompile(`(?is)\bDO\s+\$\$.*?\$\$\s*;`)
	toRegclass := regexp.MustCompile(`to_regclass\(\s*'public\.([a-z0-9_]+)'`)
	hardRef := regexp.MustCompile(`'public\.([a-z0-9_]+)'::regclass`)
	returnRE := regexp.MustCompile(`(?i)\bRETURN\s*;`)
	mutateRE := regexp.MustCompile(`(?is)CREATE\s+OR\s+REPLACE\s+VIEW|EXECUTE\s+`)

	type hit struct {
		file      string
		guardLine int
		actLine   int
		rel       string
	}
	var hits []hit
	multiBlock := 0

	for _, name := range entries {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		src := stripSQLComments(string(b)) // 包内既有的 helper（migration_602_test.go）
		locs := doBlock.FindAllStringIndex(src, -1)
		if len(locs) < 2 {
			continue
		}
		multiBlock++
		blocks := make([]string, len(locs))
		starts := make([]int, len(locs))
		for i, l := range locs {
			blocks[i] = src[l[0]:l[1]]
			starts[i] = 1 + strings.Count(src[:l[0]], "\n")
		}
		for i, blk := range blocks {
			if !returnRE.MatchString(blk) {
				continue
			}
			guarded := toRegclass.FindAllStringSubmatch(blk, -1)
			if len(guarded) == 0 {
				continue // 守卫不测关系存在性 ⇒ 本门不判（见文件头「为什么这么窄」）
			}
			for j := i + 1; j < len(blocks); j++ {
				if !mutateRE.MatchString(blocks[j]) {
					continue
				}
				later := map[string]bool{}
				for _, m := range hardRef.FindAllStringSubmatch(blocks[j], -1) {
					later[m[1]] = true
				}
				// 关键收窄：**只看后续块自己没守的那些引用**。
				// 2026-10-03 修完 816/817 后这里曾重新报红——因为块 1 守了、
				// 块 2 也守（每个块守自己要碰的关系），跨块规则看不见块 2 自守。
				// 真正的缺陷是「A 块守了、B 块**没守**却直连」，不是「A 守过」。
				selfGuarded := map[string]bool{}
				for _, m := range toRegclass.FindAllStringSubmatch(blocks[j], -1) {
					selfGuarded[m[1]] = true
				}
				var common []string
				for _, g := range guarded {
					if later[g[1]] && !selfGuarded[g[1]] {
						common = append(common, g[1])
					}
				}
				if len(common) > 0 {
					sort.Strings(common)
					hits = append(hits, hit{name, starts[i], starts[j], strings.Join(common, ", ")})
				}
				break // 第一个会动 DDL 的后续块即足够判定
			}
		}
	}

	t.Logf("扫描 %d 个 startup 迁移（其中 %d 个含多个 DO 块）", len(entries), multiBlock)

	found := map[string]bool{}
	for _, h := range hits {
		found[h.file] = true
		if reason, known := knownIneffectiveToRegclassGuards[h.file]; known {
			t.Logf("已知欠账 %s:块%d→块%d 关系 %s —— %s",
				h.file, h.guardLine, h.actLine, h.rel, reason)
			continue
		}
		t.Errorf("%s:块%d 用 to_regclass 判存在性并 RETURN，但块%d 对同一关系 %s 硬写 ::regclass。"+
			"\n    DO 块是独立语句，RETURN 只结束本块 —— 守卫拦不住它。"+
			"\n    守卫条件成立时（该关系不存在），块%d 不是「跳过」而是 relation does not exist；"+
			"逐文件事务 ⇒ 整条迁移失败。"+
			"\n    修法（不要改已应用迁移的语义）：把守卫与重建折进同一个块的 IF … THEN … END IF，"+
			"或让守卫块成为最后一块。",
			h.file, h.guardLine, h.actLine, h.rel, h.actLine)
	}

	// 登记项失效时门反过来报红：否则这条欠账会在没人再提的时候长期静默。
	stale := make([]string, 0, len(knownIneffectiveToRegclassGuards))
	for name := range knownIneffectiveToRegclassGuards {
		if !found[name] {
			stale = append(stale, name)
		}
	}
	sort.Strings(stale)
	for _, name := range stale {
		if _, err := os.Stat(name); os.IsNotExist(err) {
			t.Errorf("knownIneffectiveToRegclassGuards 里的 %s 已不存在：删掉登记项", name)
			continue
		}
		t.Errorf("%s 已不再命中本门的判据（守卫已折进同一块，或守卫块已变成最后一块）："+
			"请删掉 knownIneffectiveToRegclassGuards 里的登记项。"+
			"\n    这条反向判据是登记表的承重面——没有它，欠账修好后登记会永久留着，"+
			"下一个写同样形状的人会以为「已登记过，没关系」。", name)
	}
}

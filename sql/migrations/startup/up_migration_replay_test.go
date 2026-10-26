package startup

// 正向迁移的「重建即写路径断」门（2026-10-06，审计 §10.69）
//
// 承接 `down_migration_write_path_test.go`。那道门只扫 `.down.sql`，
// 而 §10.66 的证据表明线上那次重建**不是**跑 down，是**手工重放了 up**：
//
//     10-06 02:58~04:47  保留任务报 42P01（表不存在）
//     10-06 ~05:00       表以「453 的 32 列 + 818 的 24 列、3 列主键、非分区」回来
//
// ⇒ 能造出这个状态的**上一份文件不是回滚脚本，是 `453_..._sql` 本身**：
//   它 `CREATE TABLE … PRIMARY KEY (snapshot_ts, credential_id, raw_model_name)`。
//   它作为历史迁移**当初是对的**（463 随后把它改成 4 列），
//   但它**永久留在仓里**，任何人手工重放一次就复现 P0。
//
// ★ 本门与 down 门同族但**不是同一道**：
//   down 门抓「回滚会退回坏形态」，本门抓「重放历史建表会重建出坏形态」。
//   两条路径产出**相同**的可观测状态，而**只防一条**正是 §10.67 踩过的坑。

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// knownUpReplayLandmines = 会把该表重建/改写成与 writer 的 ON CONFLICT
// 不一致的形态的**正向** SQL。
//
// 为什么 453 必须留在表里而不是删掉：它是历史迁移，
// `schema_migrations` 记着它 2026-07-22 已应用；删文件会让
// 「基线 ↔ 账本」的核对少一条记录，比留着更糟。
// 留着的代价是「可被手工重放」，所以登记它并写明代价。
var knownUpReplayLandmines = map[string]string{
	"453_ursm_v2_node_snapshot_min.sql": `
它当初是对的：CREATE TABLE 时只有 3 列主键，随后 463（upsm_v2_snapshot_tenant_identity）
把主键改成 4 列并给 tenant_id 加 NOT NULL。schema_migrations 记着它 2026-07-22 已应用。
★ 危险在于它是**建表**文件：2026-10-06 线上那次重建就是手工重放它（+818.sql），
  产出「3 列主键 + tenant_id 可空」，writer 的 ON CONFLICT (4 列) 随即每次抛 42P10。
★ 不能删：删掉会让「账本已记录 453」失去对应的文件定义。
★ 因此登记在册，并明确它的代价：**任何手工重放本文件都会重建出断掉的写路径**。
  正确做法是重放**当前**形态（4 列），或干脆不重放、走 463。`,
}

// TestUpMigrationsDoNotRecreateTheBrokenShape —— 本门正身。
//
// 扫正向迁移里对该表声明的主键，凡与 writer 的 ON CONFLICT 列集合不同的
// ⇒ 必须登记在 `knownUpReplayLandmines` 里并写明后果。
//
// ★ 判据量的仍是**消费者的可用性**（跑完写入还能不能成功），
//
//	不是「文件是不是历史上该有的样子」。453 当初完全正确，
//	今天它危险是因为**它会被重放**。
func TestUpMigrationsDoNotRecreateTheBrokenShape(t *testing.T) {
	want := writerONConflictCols(t)

	// 正向文件：*.sql（排除 .down.sql / .skip 的 down 语义）。
	// .sql.skip 也扫 —— 830 虽被改标 skip，它若被解 skip 就会生效。
	patterns := []string{"*.sql", "*.sql.skip"}
	var files []string
	for _, p := range patterns {
		m, err := filepath.Glob(p)
		if err != nil {
			t.Fatalf("glob %s: %v", p, err)
		}
		files = append(files, m...)
	}

	var offenders []string
	var checked int
	for _, name := range files {
		if strings.HasSuffix(name, ".down.sql") {
			continue
		}
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		body := string(b)
		if !strings.Contains(body, snapshotTable) {
			continue
		}
		// ★★ 范围收窄（2026-10-20，§143.35 收尾轮）：
		//   本文件只是【提到】这张表（读它 / 引用它）⇒ 它的主键不是这张表的
		//   ⇒ 不在本题范围内。
		//   ★ 原先的取法是「文件里第一个 PRIMARY KEY」⇒ 建了【另一张】带主键的表
		//   又提到这张表的迁移（例：892 建 ursm_snapshot_slot_daily）会被误判。
		got, declared, hasPK := declaredPrimaryKeyCols(body)
		if !declared || !hasPK {
			continue
		}
		checked++
		if got == want {
			continue
		}
		if _, registered := knownUpReplayLandmines[name]; registered {
			continue
		}
		offenders = append(offenders, strings.Join([]string{
			name,
			"declares PK [" + got + "]",
			"writer needs [" + want + "]",
		}, "\n  "))
	}

	if checked < 2 {
		t.Fatalf("只检查了 %d 个【声明】%s 的正向迁移 —— 预期至少 2 个（453 与 830）。"+
			"★ 没有覆盖就等于没有判据。", checked, snapshotTable)
	}
	if len(offenders) > 0 {
		t.Errorf("以下正向迁移会把该表声明成 writer 无法使用的形态；"+
			"手工重放其一就会复现 2026-10-06 的 P0（每次写入抛 42P10）：\n  %s\n\n"+
			"★ 处置：若确实需要保留（历史迁移），登记进 knownUpReplayLandmines 并写明代价；\n"+
			"  若已不该存在，走正常通道移除。",
			strings.Join(offenders, "\n  "))
	}
}

// reAnyPrimaryKey 匹配任何写法的 PRIMARY KEY (…) —— 内联的与 ALTER 的都算。
// 上游的 reAddPrimaryKey 刻意要求 `ADD` 前缀（那是 down 门需要的精确性），
// 在这里复用会漏掉 453 的内联建表写法。
var reAnyPrimaryKey = regexp.MustCompile(`(?is)PRIMARY\s+KEY\s*\(([^)]*)\)`)

// ─────────────────────────────────────────────────────────────────────────────
// ★★ 范围收窄（2026-10-20，§143.35 收尾轮）
//
// ★★★ 本门原先的取法是「文件正文里【提到】ursm_node_snapshot_min」⇒
//   「文件里的【第一个】PRIMARY KEY (…)」⇒ 与 writer 的 ON CONFLICT 比。
// ⇒ ⇒ **范围过宽**：任何「建了【另一张】带主键的表、同时又提到这张表」的迁移
//   都会被误判成 offender。
// ⇒ ⇒ **实测踩到**：迁移 892（URSM 巡检的每日 slot 汇总表）建的是
//   `ursm_snapshot_slot_daily`（PK = day, granularity, slot, bucket_ts），
//   它只是**读** `ursm_node_snapshot_min` ⇒ **重放它不会碰这张表的主键**
//   ⇒ ⇒ **不是 landmine**，却把本门判红了。
// ⇒ ⇒ ★ 修法：把主键提取**限定到该表自身的声明语句**：
//   ① ALTER 形态（`ALTER TABLE [ONLY] [public.]<表> … ADD [CONSTRAINT x] PRIMARY KEY (…)`）
//      —— 它是后发生的，对**最终形态**权威 ⇒ **优先**；
//   ② 内联形态（`CREATE TABLE [IF NOT EXISTS] [public.]<表> ( … PRIMARY KEY (…) )`）
//      —— 只看**建表语句自己的括号块**里的主键（PG 的列类型里也可能有括号，
//         所以必须做括号配对，不能用正则一路扫）。
// ⇒ ⇒ ★★ 收窄后【守卫力未被削弱】，实测逐条复核：
//   453（内联 3 列 PK）⇒ 仍然 OFFENDER ✔；
//   463（ALTER 4 列）⇒ OK ✔；830（ALTER 4 列）⇒ OK ✔；
//   818 / 875（只提到、不声明）⇒ 跳过 ✔；892（建的是另一张表）⇒ 跳过 ✔。
// ─────────────────────────────────────────────────────────────────────────────

// reSnapshotCreateTable 匹配「声明该表」的建表写法（内联主键形态）。
var reSnapshotCreateTable = regexp.MustCompile(
	`(?is)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?(?:public\.)?` + snapshotTable + `\s*\(`)

// reSnapshotAddPrimaryKey 匹配「给该表加主键」的 ALTER 写法。
//
// ★ `[^;]*?` 不跨语句边界 ⇒ 不会把「ALTER 别的表」误当成「ALTER 这张表」。
var reSnapshotAddPrimaryKey = regexp.MustCompile(
	`(?is)ALTER\s+TABLE\s+(?:ONLY\s+)?(?:public\.)?` + snapshotTable +
		`\b[^;]*?ADD\s+(?:CONSTRAINT\s+\S+\s+)?PRIMARY\s+KEY\s*\(([^)]*)\)`)

// parenBlockContent 返回「以左括号开头的那一段」里、最外层括号内的内容。
//
// ★ 必须做括号配对：PG 的列类型自带括号（numeric(10,2)），
//   默认值也可能是表达式（DEFAULT (now())）⇒ 一路正则扫会取错位置。
func parenBlockContent(s string) string {
	depth := 1
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return s[:i]
			}
		}
	}
	return s
}

// declaredPrimaryKeyCols 返回「本文件为 ursm_node_snapshot_min 声明的主键列集合」。
//
//	declared —— 本文件是否【声明】了这张表（只提到不算）
//	ok      —— 是否成功取到主键列
func declaredPrimaryKeyCols(body string) (cols string, declared bool, ok bool) {
	// ① ALTER 形态优先（最终形态权威）。
	if m := reSnapshotAddPrimaryKey.FindStringSubmatch(body); m != nil {
		return joinCols(m[1]), true, true
	}
	// ② 内联 CREATE TABLE 形态：只看建表语句自己的括号块。
	mi := reSnapshotCreateTable.FindStringIndex(body)
	if mi == nil {
		return "", false, false
	}
	if m := reAnyPrimaryKey.FindStringSubmatch(parenBlockContent(body[mi[1]:])); m != nil {
		return joinCols(m[1]), true, true
	}
	return "", true, false
}

// TestUpLandmineRegistryHasNoStaleEntries —— 登记表的防腐，两个方向都要。
func TestUpLandmineRegistryHasNoStaleEntries(t *testing.T) {
	if len(knownUpReplayLandmines) == 0 {
		t.Fatal("knownUpReplayLandmines 为空 —— 上面的门已不再拦任何东西，却仍显示绿色")
	}
	for name, reason := range knownUpReplayLandmines {
		if _, err := os.Stat(name); err != nil {
			t.Errorf("登记表列了 %q 但该文件不存在：%v\n"+
				"★ 删文件会放过静默，留空登记会让人误以为已处理。", name, err)
		}
		if strings.TrimSpace(reason) == "" {
			t.Errorf("%s 登记理由为空 —— 必须写清「重放它会发生什么」", name)
		}
		if !strings.Contains(reason, "42P10") {
			t.Errorf("%s 的登记理由没提 42P10（本次 P0 的实际错误签名）", name)
		}
	}
}

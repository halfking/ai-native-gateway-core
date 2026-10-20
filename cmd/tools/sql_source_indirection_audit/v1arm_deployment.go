package main

// v1arm_deployment.go — 「两个时点」的分离：**基线形态** vs **部署形态**（§19.10 跨会话订正）
//
// # 为什么需要这个文件（跨会话订正，2026-10-21）
//
// R55 轮之前有一道门 `TestComposerDefinesNoV1ArmView`（§9.260）：
// 「全仓不得存在含 v1 臂的视图」，它的推导源是 `sql/objects/views/` 的 DDL + composer。
// ⇒ 那道门**生来就红**：写它的时候（迁移 885 轮，10-20 14:33），
// `sql/objects/views/` 里的 5 个文件**已经**是 v1 fallback 体（10-20 09:58 的
// `e9a96c26c` 改会话族时发现新装会 42P01 ⇒ 把基线退回 v1 体 + 交给迁移 884 切换）。
//
// ★ **那 5 个文件里的 v1 臂是基线必需，不是缺陷**：
// 基线 schema 是**逐语句**应用的，而 `CREATE VIEW` 会**急切校验**它引用的关系
// ⇒ 若写成会话族形态（`session_turns_hot` / `session_turn_details*`），
// 全新安装会在基线阶段就报 **42P01: relation "public.session_turns_hot" does not exist** 并中止。
// 部署形态由**切换迁移**（883 / 884）换上 ⇒ 两条路径跑完启动序列后形态一致。
//
// ⇒ **判据必须分两个时点**：
//
//	基线时点形态（sql/objects/views/ 的基线体）    ⇒ **允许** v1 臂
//	部署时点形态（切换迁移的体 / 迁移后的实况）    ⇒ **不该有** v1 臂
//
// # 本文件的实现口径：**部署形态不是假设，是读出来的**
//
// 「声明自己是基线形态 fallback 体」只拿到一张**空白支票**。要拿到豁免，
// 它还必须给出一个**切换迁移**，而本文件会去**读那个迁移的体**：
//
//	① 切换迁移文件存在；
//	② 它**已登记**在启动链（sql/schema/installed_startup_migrations.tsv，
//	   该 TSV 是 installer/internal/dbinit/runner.go:Runner.StartupFiles 的镜像）；
//	③ 它排在**v1 族退役迁移之前**（否则部署形态还没换上，v1 就被删了 ⇒ 视图读空）；
//	④ **它自己的体里没有 v1 臂**（⇒ 部署形态真的是干净的）。
//
// ★ ③④ 让「豁免」变成**可证伪**的：把切换迁移的体改脏（加一条 v1 臂），
//   这道门立刻红；把切换迁移从启动链里摘掉，也立刻红。
//
// # 声明是**机器可读**的
//
// 源文件头里两行（不带 `--` 后缀以外的任何自由文本）：
//
//	-- dbinit:baseline-v1-fallback
//	-- dbinit:baseline-v1-fallback-switch: 884_session_family_v_analytics_views.sql
//
// ⇒ 「源里出现 v1 臂」这件事由**声明**承担，而不是靠一道会要求危险改动的门来断言。
//   声明过期（文件已不含 v1 臂）、声明缺迁移、迁移没登记、迁移排在退役之后 ——
//   四种情况逐条红。

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ── 声明标记 ──────────────────────────────────────────────────────────────

var (
	// baselineFallbackMarkerRE 匹配「本文件是基线形态 fallback 体」这一行声明。
	// ★ 沿用仓内既有的 `dbinit:` 标记惯例（installer/internal/dbinit/runner.go 的
	//   `dbinit:no-transaction`：独立注释行，允许尾随说明文字）
	//   ⇒ 标记后面必须跟空白或行尾（否则会误匹配 `...-switch:` 那一行）。
	baselineFallbackMarkerRE = regexp.MustCompile(`(?m)^--[ \t]*dbinit:baseline-v1-fallback(?:[ \t]|$)`)
	// baselineFallbackSwitchRE 匹配「哪个迁移把它换成部署形态」。
	baselineFallbackSwitchRE = regexp.MustCompile(`(?m)^--[ \t]*dbinit:baseline-v1-fallback-switch:[ \t]*(\S+)`)
)

// BaselineFallbackDecl 是一个「基线形态 fallback 体」的声明。
type BaselineFallbackDecl struct {
	// View 视图名
	View string
	// File 相对仓库根的源文件路径（正斜杠）
	File string
	// Switch 声明的切换迁移文件名（相对 sql/migrations/startup/）
	Switch string
}

// V1ArmAudit 是「部署时点 v1 臂」的一次完整审计结果。
type V1ArmAudit struct {
	// DeploymentArms 部署时点仍含 v1 臂的视图（**必须为空**）。
	DeploymentArms []string
	// BaselineFallbacks 从源文件解析出的基线形态 fallback 体声明。
	BaselineFallbacks []BaselineFallbackDecl
	// Problems 声明纪律的问题（每条都指到具体文件/迁移，可定位）。
	Problems []string
}

// baselineFallbackDecls 解析 sql/objects/views/ 下的基线形态 fallback 体声明。
//
// 只认**带标记**的文件 ⇒ 未声明却含 v1 臂的视图会落到 DeploymentArms（主断言红）。
func baselineFallbackDecls(root string) ([]BaselineFallbackDecl, error) {
	dir := filepath.Join(root, "sql", "objects", "views")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var decls []BaselineFallbackDecl
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		src := string(b)
		if !baselineFallbackMarkerRE.MatchString(src) {
			continue
		}
		decl := BaselineFallbackDecl{
			File: "sql/objects/views/" + e.Name(),
		}
		if m := createViewRE.FindSubmatch(b); m != nil {
			decl.View = string(m[1])
		}
		if sm := baselineFallbackSwitchRE.FindStringSubmatch(src); sm != nil {
			decl.Switch = sm[1]
		}
		decls = append(decls, decl)
	}
	sort.Slice(decls, func(i, j int) bool { return decls[i].File < decls[j].File })
	return decls, nil
}

// startupChainOrder 读出启动迁移的**有序**文件名。
//
// 真相源是 installer/internal/dbinit/runner.go 的 `Runner.StartupFiles`；
// `sql/schema/installed_startup_migrations.tsv` 是它的机器可读镜像
// （仓内多道门以该 TSV 为准，例：bg/realschema_migration_health_e2e_test.go）。
func startupChainOrder(root string) ([]string, error) {
	p := filepath.Join(root, "sql", "schema", "installed_startup_migrations.tsv")
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimSpace(fields[1])
		if name != "" {
			out = append(out, name)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// v1DropMigrationIndex 找出「在启动链里退役 v1 族」的那个迁移的下标。
//
// ★ 判据是**推导**的，不是写死文件名。三个条件同时成立才算「退役 v1 族」：
//
//	① body 含 "DROP TABLE"；
//	② body 引用了 v1 基表名（引号形式）；
//	③ body 同时引用了**两代拼写**（带 `_` 前缀与不带）。
//
// 条件 ③ 是关键：只删其中一张的迁移很多（562 / 687 / 765 / 829 / 836 都只碰一张），
// 而**整族退役**的那一个（890）会把两代拼写的全族名字都列出来。
func v1DropMigrationIndex(root string, chain []string) (int, string) {
	for i, name := range chain {
		b, err := os.ReadFile(filepath.Join(root, "sql", "migrations", "startup", name))
		if err != nil {
			continue
		}
		src := string(b)
		if !strings.Contains(src, "DROP TABLE") {
			continue
		}
		hasPrefixed, hasBare := false, false
		for t := range v1Tables {
			if !strings.Contains(src, "'"+t+"'") {
				continue
			}
			if strings.HasPrefix(t, "_") {
				hasPrefixed = true
			} else {
				hasBare = true
			}
		}
		if hasPrefixed && hasBare {
			return i, name
		}
	}
	return -1, ""
}

// sqlFileViewEdges 解析一个 SQL 文件里的 CREATE VIEW 体，返回 视图 → 引用关系。
func sqlFileViewEdges(body string) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	body = stripSQLComments(body)
	locs := createViewRE.FindAllStringSubmatchIndex(body, -1)
	for i, loc := range locs {
		end := len(body)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		name := body[loc[2]:loc[3]]
		refs := map[string]bool{}
		for _, m := range fromRelationRE.FindAllStringSubmatch(body[loc[0]:end], -1) {
			refs[firstRelationToken(m[1])] = true
		}
		if len(refs) > 0 {
			out[name] = refs
		}
	}
	return out
}

// AuditV1Arms 做一次完整的「部署时点 v1 臂」审计。
//
// 部署时点的视图体来自**两个**来源：
//  1. 未声明为基线形态 fallback 体的视图 DDL（sql/objects/views/*.sql）+ composer；
//  2. 已声明的那些视图，其**切换迁移的体**（这才是部署形态）。
//
// ⇒ 声明了但没给出可核的切换迁移 ⇒ 不给豁免（进 Problems）。
func AuditV1Arms(root string) V1ArmAudit {
	audit := V1ArmAudit{}

	decls, err := baselineFallbackDecls(root)
	if err != nil {
		audit.Problems = append(audit.Problems,
			"★ 读不到 sql/objects/views/ 目录（声明解析失败）："+err.Error())
		return audit
	}
	audit.BaselineFallbacks = decls

	chain, err := startupChainOrder(root)
	if err != nil {
		audit.Problems = append(audit.Problems,
			"★ 读不到启动迁移清单 sql/schema/installed_startup_migrations.tsv："+err.Error())
		return audit
	}
	chainIndex := map[string]int{}
	for i, n := range chain {
		chainIndex[n] = i
	}
	dropIdx, dropName := v1DropMigrationIndex(root, chain)
	if dropIdx < 0 {
		audit.Problems = append(audit.Problems,
			"★ 在启动链里找不到「删 v1 基表」的迁移 ⇒ 无法判定「切换迁移排在退役之前」")
	}

	// ── 边：视图 → 它引用的关系 ──
	edges := map[string]map[string]bool{}
	// ① 源文件（sql/objects/views/*.sql）—— 这是**基线形态**的体
	srcViewEdges := map[string]map[string]bool{}
	declared := map[string]bool{}
	for _, d := range decls {
		declared[d.View] = true
	}
	if entries, err := os.ReadDir(filepath.Join(root, "sql", "objects", "views")); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
				continue
			}
			b, err := os.ReadFile(filepath.Join(root, "sql", "objects", "views", e.Name()))
			if err != nil {
				continue
			}
			for name, refs := range sqlFileViewEdges(string(b)) {
				edges[name] = refs
				srcViewEdges[name] = refs
			}
		}
	}
	// ② composer：Go 源码里的 CREATE VIEW 字符串 —— 这是**部署形态**的体
	//    （composer 产出的就是运行期真正执行的定义）
	if b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(viewSourceFiles[1]))); err == nil {
		for name, refs := range sqlFileViewEdges(string(b)) {
			edges[name] = refs
		}
	}

	// ── 声明纪律：每条声明都要给出可核的切换迁移 ──
	//
	// ★ 处置口径（三选一，没有第四种）：
	//   ① 兜得住（迁移存在 + 已登记 + 排在退役之前 + 体里没有 v1 臂）
	//      ⇒ 用**迁移的体**当部署形态（覆盖源文件的体）⇒ 豁免生效；
	//   ② 兜不住 ⇒ 记一条 Problem（门会红）**并且**把该视图从部署边里剔掉
	//      （它的源文件体是**基线形态**，不是部署形态 ⇒ 不能拿它判部署时点）；
	//   ③ 部署形态自己带 v1 臂 ⇒ 既进 DeploymentArms，又记 Problem。
	deploymentEdges := map[string]map[string]bool{}
	for name, refs := range edges {
		deploymentEdges[name] = refs
	}

	for _, d := range decls {
		// 过期声明检查：源文件里已经**没有** v1 臂了 ⇒ 声明该退休。
		srcRefs, ok := srcViewEdges[d.View]
		if !ok || !refsHitV1(srcRefs) {
			audit.Problems = append(audit.Problems, fmt.Sprintf(
				"%s: 声明了「基线形态 fallback 体」但**源文件里已经没有 v1 臂** ⇒ 声明过期。"+
					"请删掉那两行声明（否则它会一直占着一个豁免名额）。", d.File))
			delete(deploymentEdges, d.View)
			continue
		}

		switchName := d.Switch
		if switchName == "" {
			audit.Problems = append(audit.Problems, fmt.Sprintf(
				"%s: 声明了「基线形态 fallback 体」但**没给切换迁移** ⇒ 豁免无效。"+
					"请补一行 `-- dbinit:baseline-v1-fallback-switch: <迁移文件名>`，"+
					"或去掉那行 `-- dbinit:baseline-v1-fallback` 声明（那样它会被当部署形态判）。",
				d.File))
			delete(deploymentEdges, d.View)
			continue
		}
		idx, ok := chainIndex[switchName]
		if !ok {
			audit.Problems = append(audit.Problems, fmt.Sprintf(
				"%s: 声明的切换迁移 %s **不在启动链**（sql/schema/installed_startup_migrations.tsv）"+
					"⇒ 部署时点不会换上会话族形态，豁免无效。", d.File, switchName))
			delete(deploymentEdges, d.View)
			continue
		}
		if dropIdx >= 0 && idx > dropIdx {
			audit.Problems = append(audit.Problems, fmt.Sprintf(
				"%s: 声明的切换迁移 %s 排在 v1 族退役迁移 %s **之后**（链位 %d > %d）"+
					"⇒ 视图还没换成会话族形态，v1 就被删了 ⇒ 豁免无效。",
				d.File, switchName, dropName, idx, dropIdx))
			delete(deploymentEdges, d.View)
			continue
		}
		// 切换迁移的体 = **部署形态** ⇒ 它的边覆盖源文件的边。
		migPath := filepath.Join(root, "sql", "migrations", "startup", switchName)
		mig, err := os.ReadFile(migPath)
		if err != nil {
			audit.Problems = append(audit.Problems, fmt.Sprintf(
				"%s: 声明的切换迁移 %s **读不到**（%v）⇒ 豁免无效。", d.File, switchName, err))
			delete(deploymentEdges, d.View)
			continue
		}
		migEdges := sqlFileViewEdges(string(mig))
		migRefs, replaced := migEdges[d.View]
		if !replaced {
			audit.Problems = append(audit.Problems, fmt.Sprintf(
				"%s: 切换迁移 %s 里**没有** %s 的 CREATE OR REPLACE VIEW ⇒ 豁免无效"+
					"（部署形态无从谈起）。", d.File, switchName, d.View))
			delete(deploymentEdges, d.View)
			continue
		}
		// 部署形态自己带 v1 臂 ⇒ 声明无效（豁免必须有迁移兜底）。
		deploymentEdges[d.View] = migRefs
		if refsHitV1(migRefs) {
			audit.Problems = append(audit.Problems, fmt.Sprintf(
				"%s: 切换迁移 %s 的**部署形态体里还有 v1 臂** ⇒ 豁免无效"+
					"（读该视图的读方在运行期会静默少行）。", d.File, switchName))
		}
	}

	// ── 主断言的输入：部署时点的边 ──
	arms := closureV1Arm(deploymentEdges, nil)
	for v := range arms {
		audit.DeploymentArms = append(audit.DeploymentArms, v)
	}
	sort.Strings(audit.DeploymentArms)
	return audit
}

// DeploymentArmSet 返回部署时点 v1 臂视图的**集合形态**（给分类器挂载用）。
func (a V1ArmAudit) DeploymentArmSet() map[string]bool {
	out := map[string]bool{}
	for _, v := range a.DeploymentArms {
		out[v] = true
	}
	return out
}

// refsHitV1 报告一组引用关系里有没有 v1 基表。
func refsHitV1(refs map[string]bool) bool {
	for r := range refs {
		if v1Tables[r] {
			return true
		}
	}
	return false
}

// partition_bound_probe —— 「逐读点实测分区下界」的量具（R174 §10 六立项）。
//
// # 这个工具回答什么问题
//
// R174 轮实测出：canonical 视图（public.request_logs_with_current_month）的投影里
// **没有 partition_date** ⇒ ⇒ 任何经它做的读点都**无法表达分区下界**
// ⇒ ⇒ PG 做不了分区裁剪 ⇒ ⇒ 秒级慢 SQL。
//
// ★★ 但 R174 同时实测出：**「加分区下界」在【最常见的近 30 天窗口】上反而慢 3.0~3.9×**
// ⇒ ⇒ **「加下界」不是一个可以按形态一刀切铺开的方案** ⇒ ⇒ **必须逐读点实测**。
//
// ⇒ 本工具就是那把尺子。两个子命令：
//
//	scan     —— 静态：AST 扫生产 .go，逐【SQL 字面量】判定它的 ts 谓词形态
//	measure  —— 动态：给两条【可比】的 SQL + 各自的参数，跑 EXPLAIN (ANALYZE, BUFFERS)
//	            并输出**可比的**读数（执行时间 / 规划时间 / buffers / 行数 /
//	            **被裁掉的分区数**）
//
// # 为什么 `measure` 要人给两条 SQL，而不是工具自动改写
//
// 「把 canonical 视图换成【带下界的两臂直查】」这个改写**不是一个机械替换**：
// 视图体是 `db/request_logs_view_schema.go` 的 `canonicalSessionOnlyDDL()` 生成的
// 118 列投影（含 details LEFT JOIN 与 api_key_id/credential_id 的 CASE 归一）。
// ⇒ 工具自动改写 = 工具自己重写一遍生产 SQL ⇒ **测的不是生产那条路径**。
//
// ⇒ 所以：**改写由人显式给出（spec 里两条 SQL 都写出来），工具只负责【量】**。
// ⇒ 这样每一次测量都是**可复核的**（两条 SQL 都在 spec 里，逐字可读）。
//
// # 「被裁掉的分区数」怎么取
//
// ★ PG 的 Append / Merge Append 节点在发生分区裁剪时会带 **`Subplans Removed: N`**
// ⇒ ⇒ **那就是「裁掉了几个分区」的直接证据**，比「看扫描了哪些分区」更准
// （后者会把「没扫但也没裁」的形态算成「没裁」）。
//
// # 工具只读
//
// 本工具**只跑 SELECT / EXPLAIN**，不写库。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/db"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "用法: partition_bound_probe <scan|measure> [flags]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "scan":
		runScan(os.Args[2:])
	case "measure":
		runMeasure(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "未知子命令 %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Println(`partition_bound_probe —— 逐读点实测分区下界的量具

  scan    --root <repo 根> [--out <tsv>]
          AST 扫生产 .go，逐 SQL 字面量判定 ts 谓词形态（相对窗口 / 参数化绝对窗口 / 无）

  measure --spec <spec.json> [--dsn <dsn>] [--timeout <秒>]
          对 spec 里每条读点的两条【可比】SQL 跑 EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)，
          输出执行时间 / 规划时间 / buffers / 行数 / **被裁掉的分区数（Subplans Removed）**`,
	)
}

// ── measure ────────────────────────────────────────────────────────────────

// ReadPoint 是一条待测读点。
type ReadPoint struct {
	ID string `json:"id"`
	// File 是这个读点在源码里的位置（人填，用于回溯）。
	File string `json:"file"`
	// Note 是这条读点的一句话说明（人填）。
	Note string `json:"note"`
	// Variants 是两条【可比】的 SQL。
	//   · A 形 = 现状（canonical 视图）
	//   · B 形 = 加了 partition_date 下界的两臂直查
	Variants []Variant `json:"variants"`
}

// Variant 是一条 SQL 变体。
type Variant struct {
	Name string `json:"name"`
	// SQL 是这条变体的 SQL（当 Source 为空时用）。
	SQL string `json:"sql"`
	// Params 是 $1..$n 的实参（按序）。
	Params []any `json:"params"`

	// ★ Source = "canonical_view" 时：**不要**在 spec 里贴 SQL ——
	//   工具自己去库里取 `pg_get_viewdef(public.request_logs_with_current_month)`，
	//   也就是**生产部署形态的那份视图体**（★ 不是手抄的）。
	//   ⇒ ⇒ B 臂与 A 臂的差异就只有「有没有分区下界」这一处。
	Source string `json:"source"`
	// InjectPartitionBound = true 时：把上面取到的视图体按 UNION ALL 拆成两臂，
	//   **给每一条臂追加** `WHERE t.partition_date >= DATE '<bound_date>'`。
	//   ⇒ 这正是 R174 轮实测过的「两臂 + 精确分区下界」形态。
	InjectPartitionBound bool `json:"inject_partition_bound"`
	// BoundDate 是注入的下界（'YYYY-MM-DD'）。
	BoundDate string `json:"bound_date"`
	// ★ NarrowColumns 是「窄投影」的列清单（配合 RewriteCanonical = "derived_narrow"）。
	//   ⇒ 派生表的两臂**只投影这几列**，但 **FROM / JOIN / WHERE 全部保留**
	//   ⇒ ⇒ 与 A 臂（canonical 原样）**只差「投影宽度」这一个变量**。
	//
	// ★★ 为什么必须一次只变一个变量：★ R177 首轮那个「窄投影对照」同时变了
	//   ①数据来源（视图 → 裸表）②投影宽度（118 → 4）③**去掉了 details LEFT JOIN**
	//   ⇒ ⇒ 它测到的 buffers 差（237K vs 77K）**主要来自 join 被去掉**，
	//   **不是**来自列变窄 ⇒ ⇒ 「窄投影省 IO」这个结论当时是**不成立的**。
	//   ⇒ ⇒ derived_narrow 是把那三个变量拆开后的**干净对照**。
	NarrowColumns []string `json:"narrow_columns"`
	// ★ NarrowAliases = "derived_narrow_alias" 用：**按输出别名**裁剪投影。
	//   ⇒ 保留 canonical 视图 select 列表里【别名命中】的那些项，
	//   **表达式原样保留**（含 CASE 归一）⇒ ⇒ 语义与 canonical 完全一致，
	//   只有「投影宽度」这一个变量变了。
	//
	// ★★ 为什么需要它：★ PG 会把子查询里【没被用到】的输出列在计划期删掉
	//   ⇒ ⇒ 对 `COUNT(*)` 这类读点，118 列 vs 1 列的 buffers **完全一样**
	//   （★ 本地实测 1100 vs 1100）⇒ ⇒ 窄投影的收益**必须在真读点上量**，
	//   而真读点消费的是【若干个别名】⇒ ⇒ 按别名裁剪才是对的切法。
	NarrowAliases []string `json:"narrow_aliases"`
	// ★ RewriteCanonical = "derived" 时：把**本变体的 SQL 里**对 canonical 视图的引用
	//   改写成派生表 `( <视图体> )` ⇒ ⇒ 读点自己的 WHERE / JOIN / 投影全部保留，
	//   只有「数据从哪来」这一处变了。
	// ★ RewriteCanonical = "derived_with_bound" 时：同上，且**在派生表内部**给两臂
	//   各追加 `WHERE t.partition_date >= DATE '<bound_date>'` ⇒ ⇒ 分区下界**被推进了**。
	//
	// ★★ 为什么必须是「派生表」而不是「外层包一层 WHERE」：
	//   视图体**不投影 partition_date** ⇒ 外层看不到它 ⇒ 外层过滤**完全无效**。
	//   ⇒ ⇒ 下界必须**推进两臂内部**才能真的裁掉分区。
	RewriteCanonical string `json:"rewrite_canonical"`
	// ★★ NativeSource（R184 轮新增）—— **原生两臂源**形态。
	//
	// ★ 为什么要有它：★ R182 起，生产读点大量改读
	//   `db.SessionFamilyTurnsSourceSQL()`（原生两臂 hot ∪ 父表 + details 腿），
	//   它的 SQL **不是字面量**（是 Go 函数拼出来的 115 列投影 × 2 臂）
	//   ⇒ ⇒ **没法手抄进 spec** ⇒ ⇒ 手抄就等于「测的不是生产那条路径」。
	//
	// ★ 做法：★ spec 的 SQL 里写占位符 `{{NATIVE}}`，工具在运行时
	//   **从 db 包取生产同款文本**填进去 ⇒ ⇒ A/B 两臂都必然是生产那条路径。
	//   · "two_arm"            ⇒ db.SessionFamilyTurnsSourceSQL()
	//   · "two_arm_lower_bound" ⇒ db.SessionFamilyTurnsSourceLowerBoundSQL(BoundExpr)
	//
	// ★★ 「一次只变一个变量」仍然成立：★ A/B 两臂**只有分区下界**这一处不同
	//   （投影、JOIN、谓词全部逐字相同）⇒ ⇒ 读数差只能归因于下界。
	NativeSource string `json:"native_source"`
	// BoundExpr 是 native_source="two_arm_lower_bound" 时的下界 **SQL 表达式**。
	// ★ 允许传表达式（而不只是常量）：★ 相对窗口的下界必须与 ts 谓词**同源**
	//   （NOW() 派生）⇒ ⇒ 才是 ts 谓词的蕴含条件 ⇒ ⇒ 不丢行。
	BoundExpr string `json:"bound_expr"`
}

// ★ NativeSourceMarker 是 spec 里代表「生产同款原生两臂源」的占位符。
const NativeSourceMarker = "{{NATIVE}}"

// resolveNativeSource 把 {{NATIVE}} 占位符换成生产同款的两臂源文本。
//
// ★ 为什么由工具来填（而不是把 SQL 手抄进 spec）：
//
//	⇒ 手抄 = 测的不是生产那条路径（★ 与本文件顶部「改写由人显式给出」的
//	初衷不冲突：这里改写的是**源的取法**，SQL 的其余部分仍逐字写在 spec 里）。
func resolveNativeSource(sqlText, nativeSource, boundExpr string) (string, error) {
	if !strings.Contains(sqlText, NativeSourceMarker) {
		return sqlText, nil
	}
	switch nativeSource {
	case "two_arm":
		return strings.ReplaceAll(sqlText, NativeSourceMarker, db.SessionFamilyTurnsSourceSQL()), nil
	case "two_arm_lower_bound":
		if strings.TrimSpace(boundExpr) == "" {
			return "", fmt.Errorf("native_source=two_arm_lower_bound 但没给 bound_expr")
		}
		return strings.ReplaceAll(sqlText, NativeSourceMarker,
			db.SessionFamilyTurnsSourceLowerBoundSQL(boundExpr)), nil
	case "":
		return "", fmt.Errorf("SQL 里有 %s 占位符，但没给 native_source", NativeSourceMarker)
	default:
		return "", fmt.Errorf("未知 native_source=%q", nativeSource)
	}
}

// VariantReading 是一次 EXPLAIN 的读数。
type VariantReading struct {
	Variant string `json:"variant"`
	// OK=false 表示这条没跑出来（超时 / SQL 错误）⇒ Err 是原因。
	OK  bool   `json:"ok"`
	Err string `json:"err,omitempty"`

	PlanningMs  float64 `json:"planning_ms"`
	ExecutionMs float64 `json:"execution_ms"`

	SharedHitBlocks  int64 `json:"shared_hit_blocks"`
	SharedReadBlocks int64 `json:"shared_read_blocks"`

	// ActualRows 是顶层节点的 Actual Rows。
	ActualRows int64 `json:"actual_rows"`
	// SubplansRemoved 是「被裁掉的分区数」的总和（★ 分区裁剪的直接证据）。
	SubplansRemoved int64 `json:"subplans_removed"`
	// PartitionsScanned 是 plan 里出现过的分区关系名个数（去重）。
	PartitionsScanned int `json:"partitions_scanned"`
	// PartitionNames 是扫到的分区关系名（逗号分隔）。
	PartitionNames string `json:"partition_names,omitempty"`
	// RunsMs 是重复跑的**每一次**执行时间（升序）⇒ 复核噪声用。
	RunsMs []float64 `json:"runs_ms,omitempty"`
}

// PointReading 是一条读点的全部变体读数。
type PointReading struct {
	ID       string           `json:"id"`
	File     string           `json:"file"`
	Note     string           `json:"note"`
	Variants []VariantReading `json:"variants"`
}

// Spec 是测量输入。
type Spec struct {
	ReadPoints []ReadPoint `json:"read_points"`
}

func runMeasure(argv []string) {
	fs := flag.NewFlagSet("measure", flag.ExitOnError)
	specPath := fs.String("spec", "", "spec JSON 路径")
	dsn := fs.String("dsn", os.Getenv("DATABASE_URL"), "Postgres DSN（默认取 $DATABASE_URL）")
	timeoutSec := fs.Int("timeout", 120, "单条语句的 statement_timeout（秒）")
	repeat := fs.Int("repeat", 3, "每个变体重复次数（取执行时间中位数）★ 单次读数被噪声主导")
	out := fs.String("out", "", "TSV 输出路径（默认 stdout）")
	sshHost := fs.String("ssh-host", "", "★ 252 模式：SSH 主机（空 = 用 --dsn 直连）")
	sshPort := fs.Int("ssh-port", 25022, "252 模式：SSH 端口")
	sshUser := fs.String("ssh-user", "root", "252 模式：SSH 用户")
	sshContainer := fs.String("ssh-container", "pg-252-pg17", "252 模式：远端 PG 容器名")
	sshDBUser := fs.String("ssh-db-user", "llm_gateway", "252 模式：DB 角色")
	sshDBName := fs.String("ssh-db-name", "llm_gateway", "252 模式：DB 名")
	view := fs.String("canonical-view", os.Getenv("CANONICAL_VIEW"), "★ canonical 视图名（★ 只有【读视图形态】的变体才需要；原生两臂形态不需要）")
	_ = fs.Parse(argv)

	if *specPath == "" {
		fmt.Fprintln(os.Stderr, "--spec 必填")
		os.Exit(2)
	}
	raw, err := os.ReadFile(*specPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "读 spec 失败: %v\n", err)
		os.Exit(1)
	}
	var spec Spec
	if err := json.Unmarshal(raw, &spec); err != nil {
		fmt.Fprintf(os.Stderr, "解析 spec 失败: %v\n", err)
		os.Exit(1)
	}
	// ★ 视图名**按需**要求（★ R184）：★ 原生两臂形态（native_source）的 spec
	//   根本不碰 canonical 视图 ⇒ ⇒ 硬要一个视图名只是噪声（而且会让人以为
	//   量的是视图那条路径）⇒ ⇒ 只有真的有变体要读视图时才要求它。
	if *view == "" && specNeedsCanonicalView(spec) {
		fmt.Fprintln(os.Stderr, "--canonical-view 必填（★ 视图名不硬编码在源码里 ⇒ 工具对任何视图都适用）")
		os.Exit(2)
	}
	if *view != "" {
		bindCanonicalView(*view)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()

	var tr transport
	if *sshHost != "" {
		sshTr := &sshTransport{
			host: *sshHost, port: *sshPort, user: *sshUser,
			container: *sshContainer, dbUser: *sshDBUser, dbName: *sshDBName,
		}
		// 控制 socket 路径带端口 ⇒ 多机并行不打架。
		sshTr.controlPath = fmt.Sprintf("/tmp/kx-pbp-ssh-mux-%d", *sshPort)
		tr = sshTr
	} else {
		if *dsn == "" {
			fmt.Fprintln(os.Stderr, "--dsn 必填（或设 DATABASE_URL），或用 --ssh-host 走 252 模式")
			os.Exit(2)
		}
		pool, err := pgxpool.New(ctx, *dsn)
		if err != nil {
			fmt.Fprintf(os.Stderr, "连库失败: %v\n", err)
			os.Exit(1)
		}
		defer pool.Close()
		if err := pool.Ping(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "ping 失败: %v\n", err)
			os.Exit(1)
		}
		tr = &pgxTransport{pool: pool, dsn: *dsn}
	}
	fmt.Fprintf(os.Stderr, "== 通道：%s\n", tr.Describe())

	var rows []PointReading
	for _, rp := range spec.ReadPoints {
		pr := PointReading{ID: rp.ID, File: rp.File, Note: rp.Note}
		// ★ 两个变体的读点 ⇒ **交错**测（measurePair）⇒ 抵消缓存漂移的顺序偏差。
		if len(rp.Variants) == 2 {
			a, b := measurePair(ctx, tr, rp.Variants[0], rp.Variants[1], *timeoutSec, *repeat, *view)
			pr.Variants = append(pr.Variants, a, b)
		} else {
			for _, v := range rp.Variants {
				pr.Variants = append(pr.Variants, measureRepeated(ctx, tr, v, *timeoutSec, *repeat, *view))
			}
		}
		rows = append(rows, pr)
		fmt.Fprintf(os.Stderr, "测完 %s\n", rp.ID)
	}

	// TSV：每个变体一行 + 一行判定。
	var b strings.Builder
	b.WriteString("read_point\tfile\tvariant\tok\texecution_ms\tplanning_ms\tshared_hit\tshared_read\tactual_rows\tsubplans_removed\tpartitions_scanned\tpartition_names\terr\n")
	for _, pr := range rows {
		for _, v := range pr.Variants {
			fmt.Fprintf(&b, "%s\t%s\t%s\t%t\t%.3f\t%.3f\t%d\t%d\t%d\t%d\t%d\t%s\t%s\n",
				pr.ID, pr.File, v.Variant, v.OK, v.ExecutionMs, v.PlanningMs,
				v.SharedHitBlocks, v.SharedReadBlocks, v.ActualRows,
				v.SubplansRemoved, v.PartitionsScanned, v.PartitionNames, v.Err)
		}
		if len(pr.Variants) == 2 {
			a, b2 := pr.Variants[0], pr.Variants[1]
			// ★ 实参类型要与格式串逐个对上（%.3f 必须给 float；给 int 会打出 %!f(int=0)）。
			// ★ 末列 = 「B 相比 A **少扫了哪些关系**」⇒ 这是【裁剪的直接证据】。
			fmt.Fprintf(&b, "%s\t%s\t%s\t%t\t%.3f\t%.3f\t%d\t%d\t%d\t%d\t%d\t%s\t%s\n",
				pr.ID, pr.File, "VERDICT", a.OK && b2.OK,
				b2.ExecutionMs-a.ExecutionMs, 0.0, int64(0), int64(0), int64(0),
				b2.SubplansRemoved-a.SubplansRemoved, 0,
				droppedRelations(a, b2), verdict(a, b2))
		}
	}
	if *out == "" {
		fmt.Print(b.String())
		return
	}
	if err := os.WriteFile(*out, []byte(b.String()), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "写输出失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "已写 %s\n", *out)
}

// droppedRelations 返回「B 比 A 少扫了哪些关系」（★ 裁剪的直接证据）。
//
// ★ 为什么要单列这个量：★ 「Subplans Removed」只统计**运行期**被裁掉的子计划；
//
//	而**常量**下界会让规划器在**计划期**就把分区从 Append 里拿掉
//	⇒ ⇒ 那时 `Subplans Removed = 0`，但**扫的关系列表确实变短了**。
//	⇒ ⇒ 只看 Subplans Removed 会把「裁剪成功」读成「没裁」。
func droppedRelations(a, b VariantReading) string {
	// ★ 方向是 **A \ B**：A（现状）扫了、而 B（加下界）**没有**扫的那些关系
	//   ⇒ 那就是「下界换来的裁剪」。
	bm := map[string]bool{}
	for _, n := range strings.Split(b.PartitionNames, ",") {
		if n != "" {
			bm[n] = true
		}
	}
	var dropped []string
	for _, n := range strings.Split(a.PartitionNames, ",") {
		if n != "" && !bm[n] {
			dropped = append(dropped, n)
		}
	}
	return strings.Join(dropped, ",")
}

// verdict 给两条变体一个可读的判定。
func verdict(a, b VariantReading) string {
	if !a.OK || !b.OK {
		return "不可比（有一侧没跑出来）"
	}
	switch {
	case b.ExecutionMs < a.ExecutionMs*0.95:
		return fmt.Sprintf("B 更快（%.3f → %.3f ms，%.2f×）", a.ExecutionMs, b.ExecutionMs, a.ExecutionMs/b.ExecutionMs)
	case b.ExecutionMs > a.ExecutionMs*1.05:
		return fmt.Sprintf("B 更慢（%.3f → %.3f ms，%.2f×）", a.ExecutionMs, b.ExecutionMs, b.ExecutionMs/a.ExecutionMs)
	default:
		return fmt.Sprintf("持平（%.3f → %.3f ms）", a.ExecutionMs, b.ExecutionMs)
	}
}

// canonicalViewDefSQL 取生产部署形态的 canonical 视图体。
//
// ★ 视图名**不硬编码**（★ 见 scan.go 里 bindCanonicalView 的注释：硬编码会被仓内那道
//
//	「canonical 视图提及登记门」当成一个生产读点文件 ⇒ ⇒ 门红）。
func canonicalViewDefSQL(viewName string) string {
	return "SELECT pg_get_viewdef('public." + viewName + "'::regclass, true)"
}

// specNeedsCanonicalView 判这个 spec 里有没有【读视图形态】的变体。
func specNeedsCanonicalView(spec Spec) bool {
	for _, rp := range spec.ReadPoints {
		for _, v := range rp.Variants {
			if v.RewriteCanonical != "" || v.Source == "canonical_view" {
				return true
			}
		}
	}
	return false
}

// buildVariantSQL 按 variant 的描述构造最终要跑的 SQL。
func buildVariantSQL(ctx context.Context, tr transport, v Variant, viewName string) (string, error) {
	// ★ R184：★ 先把 {{NATIVE}} 占位符换成**生产同款**的两臂原生源。
	//   ⇒ 之后的两条分支（rewrite_canonical / source）都基于**填好的** SQL 继续。
	if strings.Contains(v.SQL, NativeSourceMarker) {
		filled, err := resolveNativeSource(v.SQL, v.NativeSource, v.BoundExpr)
		if err != nil {
			return "", err
		}
		v.SQL = filled
	}
	if v.RewriteCanonical != "" {
		// 读点 SQL 形态：视图引用在 FROM/JOIN 里 ⇒ 把「视图名」换掉即可。
		body, err := fetchCanonicalBody(ctx, tr, viewName)
		if err != nil {
			return "", err
		}
		if v.RewriteCanonical == "derived_with_bound" {
			if v.BoundDate == "" {
				return "", fmt.Errorf("rewrite_canonical=derived_with_bound 但没给 bound_date")
			}
			body = injectPartitionBound(body, v.BoundDate)
		}
		if v.RewriteCanonical == "derived_narrow" {
			if len(v.NarrowColumns) == 0 {
				return "", fmt.Errorf("rewrite_canonical=derived_narrow 但没给 narrow_columns")
			}
			body = narrowArms(body, v.NarrowColumns)
		}
		if v.RewriteCanonical == "derived_narrow_alias" {
			if len(v.NarrowAliases) == 0 {
				return "", fmt.Errorf("rewrite_canonical=derived_narrow_alias 但没给 narrow_aliases")
			}
			body = narrowArmsByAlias(body, v.NarrowAliases, false)
		}
		if v.RewriteCanonical == "derived_narrow_alias_nojoin" {
			if len(v.NarrowAliases) == 0 {
				return "", fmt.Errorf("rewrite_canonical=derived_narrow_alias_nojoin 但没给 narrow_aliases")
			}
			body = narrowArmsByAlias(body, v.NarrowAliases, true)
		}
		rewritten := replaceCanonicalRef(v.SQL, viewName, "("+body+")")
		if rewritten == v.SQL {
			return "", fmt.Errorf("rewrite_canonical=%s 但 SQL 里没找到 canonical 视图名 %q", v.RewriteCanonical, viewName)
		}
		return rewritten, nil
	}
	if v.Source != "canonical_view" {
		return v.SQL, nil
	}
	body, err := fetchCanonicalBody(ctx, tr, viewName)
	if err != nil {
		return "", err
	}
	if !v.InjectPartitionBound {
		return body, nil
	}
	if v.BoundDate == "" {
		return "", fmt.Errorf("inject_partition_bound=true 但没给 bound_date")
	}
	return injectPartitionBound(body, v.BoundDate), nil
}

// fetchCanonicalBody 取生产部署形态的 canonical 视图体。
func fetchCanonicalBody(ctx context.Context, tr transport, viewName string) (string, error) {
	out, err := tr.Query(ctx, canonicalViewDefSQL(viewName))
	if err != nil {
		return "", fmt.Errorf("取 canonical 视图体失败: %w", err)
	}
	// ★ 这里拿到的是**视图体文本**（不是 JSON）⇒ **不能**做任何「剥前导 [」的处理：
	//   视图体里本身就有 ARRAY[…]，剥了会把 SQL 截断 ⇒ 42601。
	body := strings.TrimSpace(out)
	body = strings.TrimSuffix(body, ";")
	if body == "" {
		return "", fmt.Errorf("canonical 视图体为空")
	}
	return body, nil
}

// replaceCanonicalRef 把 SQL 里的 canonical 视图引用换成 repl（通常是派生表）。
//
// ★ 同时替换 `public.<name>` 与裸 `<name>` 两种写法。
func replaceCanonicalRef(sql, viewName, repl string) string {
	out := sql
	out = strings.ReplaceAll(out, "public."+viewName, repl)
	out = strings.ReplaceAll(out, viewName, repl)
	return out
}

// injectPartitionBound 把视图体按 UNION ALL 拆成两臂，给每条臂追加分区下界。
//
// ★ 为什么是「拆臂 + 各臂追加 WHERE」而不是「外层包一层 WHERE」：
//
//	视图体**不投影 partition_date** ⇒ 外层看不到它 ⇒ 外层过滤**无效**。
//	⇒ 必须把下界**推进两臂内部**（这正是 R174 轮实测的 F/G 形态）。
//
// ★ 为什么追加在臂尾而不是紧跟 FROM：
//
//	父表臂的形状是 `FROM session_turns t LEFT JOIN session_turn_details d ON …`
//	⇒ 在 FROM 后面插 WHERE 会打断 JOIN ⇒ 追加在臂尾（ON 子句之后）才对。
func injectPartitionBound(body, boundDate string) string {
	arms := strings.Split(body, "UNION ALL")
	for i, arm := range arms {
		a := strings.TrimSpace(arm)
		a = strings.TrimSuffix(a, ";")
		a += " WHERE t.partition_date >= DATE '" + boundDate + "'"
		arms[i] = a
	}
	return strings.Join(arms, " UNION ALL ")
}

// narrowArms 把视图体的每条臂的**投影列表**换成窄投影，但**保留 FROM / JOIN / WHERE**。
//
// ★ 这个函数存在的唯一理由：**一次只变一个变量**。
//
//	R177 首轮那个「窄投影对照」同时改了三个变量（来源 / 列宽 / 有没有 details join）
//	⇒ ⇒ 读数分不清是哪一个变量起的作用 ⇒ ⇒ 结论不成立。
//	⇒ derived_narrow 只动**投影列表**这一个变量。
//
// ★ 切分点 = **行首的 FROM**（pg_get_viewdef(…, true) 的 pretty 输出里，
//
//	每条臂的 FROM 子句独占一行且行首缩进）。
//	★ 实测（252 的视图体 297 行）：select 列表里**没有**任何 FROM 字面量
//	⇒ ⇒ 按行首 FROM 切分是安全的。
func narrowArms(body string, cols []string) string {
	arms := strings.Split(body, "UNION ALL")
	proj := "SELECT " + strings.Join(cols, ", ")
	for i, arm := range arms {
		a := strings.TrimSpace(arm)
		a = strings.TrimSuffix(a, ";")
		lines := strings.Split(a, "\n")
		cut := -1
		for j, ln := range lines {
			t := strings.TrimSpace(ln)
			if strings.HasPrefix(t, "FROM ") || t == "FROM" {
				cut = j
				break
			}
		}
		if cut < 0 {
			// ★ 这条臂的 FROM 不在行首 ⇒ 认不出来 ⇒ **保守起见不改它**
			//   （宁可让对照不成立，也不要改出一个语义不同的 SQL）。
			continue
		}
		indent := ""
		if len(lines) > 0 {
			indent = lines[0][:len(lines[0])-len(strings.TrimLeft(lines[0], " \t"))]
		}
		arms[i] = indent + proj + "\n" + strings.Join(lines[cut:], "\n")
	}
	return strings.Join(arms, " UNION ALL ")
}

// narrowArmsByAlias 按【输出别名】裁剪每条臂的投影列表。
//
// ★ 与 narrowArms 的区别：narrowArms 是「换一份列清单」（要求人把表达式抄一遍），
//
//	本函数是「**从 canonical 自己的 select 列表里挑**」⇒ ⇒ 表达式**原样保留**
//	⇒ ⇒ CASE 归一（api_key_id / credential_id / request_status 等）**不会被改错**。
//
// ★ dropJoin = true 时：★ **额外**把 `LEFT JOIN session_turn_details*` 摘掉，
//
//	并把**来自 details 侧**的列换成 `NULL::bigint AS <别名>`。
//	⇒ ⇒ 这不是「等价改写」，而是【代价归因】测量：
//	★★ 用来回答「如果不需要 details 侧的列，这个读点要读多少块」。
//
// ★ 切分：① 行首 FROM 定位 select 列表的结束；② 顶层逗号切项（跟踪括号深度与引号）。
func narrowArmsByAlias(body string, aliases []string, dropJoin bool) string {
	want := map[string]bool{}
	for _, a := range aliases {
		want[a] = true
	}
	arms := strings.Split(body, "UNION ALL")
	for i, arm := range arms {
		a := strings.TrimSpace(arm)
		a = strings.TrimSuffix(a, ";")
		lines := strings.Split(a, "\n")
		cut := -1
		for j, ln := range lines {
			t := strings.TrimSpace(ln)
			if strings.HasPrefix(t, "FROM ") || t == "FROM" {
				cut = j
				break
			}
		}
		if cut < 0 {
			continue
		}
		indent := ""
		if len(lines) > 0 {
			indent = lines[0][:len(lines[0])-len(strings.TrimLeft(lines[0], " \t"))]
		}
		selText := strings.Join(lines[:cut], " ")
		selText = strings.TrimSpace(selText)
		selText = strings.TrimPrefix(strings.ToUpper(selText), "SELECT")
		items := splitTopLevelItems(selText)
		var kept []string
		for _, it := range items {
			al := selectAlias(it)
			if al == "" || want[al] {
				// ★ 别名认不出来 ⇒ **保守地保留**（宁可多投影一列，也不要改坏语义）。
				kept = append(kept, strings.TrimSpace(it))
			}
		}
		if len(kept) == 0 {
			continue
		}
		rest := lines[cut:]
		if dropJoin {
			// ★ 摘掉 details LEFT JOIN 那一行（★ 只摘 `d` 别名的那个 JOIN）。
			var keptRest []string
			for _, ln := range rest {
				t := strings.TrimSpace(ln)
				if strings.HasPrefix(strings.ToUpper(t), "LEFT JOIN ") &&
					strings.Contains(t, "session_turn_details") {
					continue
				}
				keptRest = append(keptRest, ln)
			}
			rest = keptRest
			// ★ 来自 details 侧的列 ⇒ 换成 NULL 常量（★ 语义会变，★ 但行集不变 ⇒ ★ 用来量代价）。
			for j, it := range kept {
				if strings.Contains(it, "d.") {
					kept[j] = "NULL::bigint AS " + selectAlias(it)
				}
			}
		}
		arms[i] = indent + "SELECT " + strings.Join(kept, ", ") + "\n" + strings.Join(rest, "\n")
	}
	return strings.Join(arms, " UNION ALL ")
}

// splitTopLevelItems 按**顶层逗号**切 select 列表（跟踪括号深度与单引号字符串）。
func splitTopLevelItems(s string) []string {
	var items []string
	depth := 0
	inStr := false
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			if c == '\'' {
				inStr = false
			}
			continue
		}
		switch c {
		case '\'':
			inStr = true
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				items = append(items, s[start:i])
				start = i + 1
			}
		}
	}
	if rest := strings.TrimSpace(s[start:]); rest != "" {
		items = append(items, s[start:])
	}
	return items
}

var (
	// reAliasAS 抓结尾的 `AS <别名>`。
	reAliasAS = regexp.MustCompile(`\bAS\s+([A-Za-z_][A-Za-z0-9_]*)\s*$`)
	// reAliasTail 抓结尾的标识符（无 AS 时用；如 `t.request_id` → request_id）。
	reAliasTail = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\s*$`)
)

// selectAlias 取一个 select 项的输出别名。
func selectAlias(item string) string {
	s := strings.TrimSpace(item)
	if m := reAliasAS.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	if m := reAliasTail.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

// summarize 把多次读数汇总成一条（取成功读数的执行时间**中位数**）。
func summarize(runs []VariantReading) VariantReading {
	if len(runs) == 0 {
		return VariantReading{}
	}
	var ok []VariantReading
	for _, r := range runs {
		if r.OK {
			ok = append(ok, r)
		}
	}
	if len(ok) == 0 {
		return runs[len(runs)-1]
	}
	ms := make([]float64, 0, len(ok))
	for _, r := range ok {
		ms = append(ms, r.ExecutionMs)
	}
	sort.Float64s(ms)
	med := ms[len(ms)/2]
	best := ok[0]
	for _, r := range ok {
		if r.ExecutionMs == med {
			best = r
		}
	}
	best.ExecutionMs = med
	best.RunsMs = ms
	return best
}

// measurePair **交错地**测两个变体。
//
// ★★ 为什么必须交错（★ R177 首轮量具的第二个缺陷）：
//
//	首轮的实现是「A 跑 n 次 → B 跑 n 次」⇒ ⇒ **B 永远在 A 之后跑**
//	⇒ ⇒ B 吃的是**更热的缓存** ⇒ ⇒ 系统性地偏袒 B。
//	★ 实测证据：首轮 252 上 usage.go 读点「B 快 1.12×」，第二轮同一条读点「B 慢 1.28×」
//	⇒ ⇒ **方向都反了** ⇒ ⇒ 首轮那个 1.12× 是**偏差**，不是效应。
//	⇒ ⇒ 现在改成：偶数轮 A 先、奇数轮 B 先 ⇒ 两个变体「当先 / 当后」次数相等
//	⇒ ⇒ 缓存升温这类**系统性漂移**不会单方面落到某一侧。
func measurePair(ctx context.Context, tr transport, va, vb Variant, timeoutSec, n int, viewName string) (VariantReading, VariantReading) {
	if n < 1 {
		n = 1
	}
	var ra, rb []VariantReading
	for i := 0; i < n; i++ {
		if i%2 == 0 {
			ra = append(ra, measureOne(ctx, tr, va, timeoutSec, viewName))
			rb = append(rb, measureOne(ctx, tr, vb, timeoutSec, viewName))
		} else {
			rb = append(rb, measureOne(ctx, tr, vb, timeoutSec, viewName))
			ra = append(ra, measureOne(ctx, tr, va, timeoutSec, viewName))
		}
	}
	return summarize(ra), summarize(rb)
}

// measureRepeated 把一个变体跑 n 次，取执行时间**中位数**。
//
// ★ 为什么必须重复：★ 单次 EXPLAIN ANALYZE 的读数被噪声主导
//
//	（★ 本机实测同一读点两次跑出 6.774ms 与 10.386ms ⇒ 单次读数**不可比**）。
//	⇒ ⇒ 判定必须落在**中位数**上，且**原始每次数**一起留档（可复核）。
func measureRepeated(ctx context.Context, tr transport, v Variant, timeoutSec, n int, viewName string) VariantReading {
	if n < 1 {
		n = 1
	}
	var runs []VariantReading
	for i := 0; i < n; i++ {
		runs = append(runs, measureOne(ctx, tr, v, timeoutSec, viewName))
	}
	return summarize(runs)
}

// measureOne 跑一次 EXPLAIN 并抽读数。
func measureOne(ctx context.Context, tr transport, v Variant, timeoutSec int, viewName string) VariantReading {
	r := VariantReading{Variant: v.Name}
	// statement_timeout：单条语句超时就**如实报出来**，不静默当成「很快」。
	sqlText, err := buildVariantSQL(ctx, tr, v, viewName)
	if err != nil {
		r.Err = err.Error()
		return r
	}
	expl := "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) " + inlineParams(sqlText, v.Params)

	stmtCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()
	// ★ statement_timeout 的值必须带引号（`SET statement_timeout = 120s` 是语法错误 ⇒ 42601）。
	// ★ SET 与 EXPLAIN **分两次调用**：pgx 的扩展协议对「一条调用里多条语句」不友好，
	//   而 SSH 通道的 psql -f - 可以 ⇒ 分开是最省事且两边都稳的写法。
	if _, err := tr.Query(stmtCtx, "SET statement_timeout = '"+strconv.Itoa(timeoutSec)+"s'"); err != nil {
		r.Err = "设置 statement_timeout 失败: " + err.Error()
		return r
	}
	out, err := tr.Query(stmtCtx, expl)
	if err != nil {
		r.Err = err.Error()
		return r
	}
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		r.Err = "EXPLAIN 没有返回内容"
		return r
	}
	payload := []byte(trimmed)
	var probe explainPayload
	if err := json.Unmarshal(payload, &probe); err != nil {
		// 退路：psql 前面可能带了别的列 ⇒ 退到第一个 '[' 再试。
		if i := strings.IndexByte(trimmed, '['); i > 0 {
			payload = []byte(trimmed[i:])
		}
	}
	parseExplain(&r, payload)
	return r
}

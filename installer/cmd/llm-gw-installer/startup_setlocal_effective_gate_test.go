//go:build !integration

package main

// startup_setlocal_effective_gate_test.go —— 「SET LOCAL 必须【真的生效】」门
// （2026-10-31 12h 审计轮新增）
//
// ★★ 缺陷形态：★★ ★ PostgreSQL 的 `SET LOCAL` **只在事务块内生效**；★ 在事务块
// ★★ 外执行只发一条 WARNING，★ **不设置任何值** ⇒ ⇒ ⇒ ★★ ⇒ 于是「我写了
// ★★ lock_timeout / statement_timeout ⇒ ⇒ 我有守卫」这条**自述**是假的
// ★★ ⇒ ⇒ ⇒ ★★ ⇒ 实际后果：★ ADD CONSTRAINT UNIQUE（ACCESS EXCLUSIVE）
// ★★ ⇒ 可以**无限期等锁** ⇒ ⇒ ⇒ ★★ 而 statement_timeout 同理失效 ⇒ ⇒ ⇒
// ★★ ⇒ 「部署期不会卡死」这句话不成立。
//
// ★★ 实测（PG17，本轮实测）：★★ ★ 非事务块里执行 `SET LOCAL lock_timeout='2s'`
// ★★ ⇒ ★★ `WARNING: SET LOCAL can only be used in transaction blocks`
// ★★ ⇒ ★★ 值没生效 ⇒ ⇒ ⇒ ★★ ⇒ **守卫是空文**。
//
// ★★★ **为什么既有门没抓住（★ 这就是本门要补的那一维）**：★★★ ★ 既有门
// ★★★ startup_ddl_lock_guard_gate_test.go 只判「文件里**有没有** lock_timeout
// ★★★ 这个**字符串**」⇒ ⇒ ★★ ⇒ ★★ 一份「写在事务块外」的 lock_timeout 照样让
// ★★★ 那道门绿 ⇒ ⇒ ⇒ ★★ ⇒ **量具只量了「写没写」，没量「写在哪」** ⇒ ⇒ ⇒
// ★★★ ⇒ ★★ ⇒ 所以本门量的正是**「写在哪」** ⇒ ⇒ ⇒ ★★ ⇒ 两道门互补：
// ★★★ ⇒ ★★ 既有门管「有没有」，本门管「有没有用」⇒ ⇒ ⇒ ★★ ⇒ **缺一即失效**。
//
// ★★★ **口径边界（★ 明确登记，不含糊过去）**：★★★ ★ 本门**只拦新增**
// ★★★ （★ 928 之后）⇒ ⇒ ★★ 存量里 **45 份**迁移的 `SET LOCAL` 写在事务块外
// ★★★ （★ 实测清单见 12h 审计档）⇒ ⇒ ⇒ ★★ ⇒ 它们**仍不合规** ⇒ ⇒ ⇒ ★★ ⇒
// ★★★ ⇒ 已进 12h 审计待修正清单（★ 分批改）⇒ ⇒ ⇒ ★★ ⇒ 本门不背历史包袱，
// ★★★ ⇒ ★★ 只保证**不再新增**⇒ ⇒ ⇒ ★★ ⇒ 927 已在本轮修好（★ 自带 BEGIN/COMMIT）。
//
// ★★★ **自证（★ 本门自己也可能是坏的）**：★★★
// ★★★ ① 阳性对照：★ 存量里必须找得到一份「SET LOCAL 在事务块内」的文件（★ 856）
// ★★★    ⇒ ⇒ 否则说明「找得到好形态」这件事本身失效 ⇒ ⇒ ⇒
// ★★★ ② 反向夹具：★ 喂一段**故意**把 SET LOCAL 放在事务块外的文本 ⇒ ⇒ 判定器
// ★★★    必须报「不生效」⇒ ⇒ ⇒
// ★★★ ③ 反向夹具 2：★ 喂一段把 SET LOCAL 放在 BEGIN 之后的文本 ⇒ ⇒ 必须报「生效」
// ★★★    ⇒ ⇒ ⇒
// ★★★ ④ **注释不许参与判定**：★ 927 的修复说明里就写着「SET LOCAL」字样 ⇒ ⇒
// ★★★    ⇒ 若不涂白注释 ⇒ ⇒ 判定器会把注释当成声明 ⇒ ⇒ ⇒ ★★ ⇒ 那就是另一道
// ★★★    ⇒ 假绿（★ 「扫的是代码还是散文」那道题）。

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// setLocalGateCutoff 是本门开始拦截的迁移号（★ 927 是最后一个已修好的 ⇒ ⇒
// ★ 928 起的新迁移必须自带事务块）。
const setLocalGateCutoff = 928

// standaloneBeginRE 匹配**独立成行**的 BEGIN;（★ 事务块起点）。
//
// ★ ★★ **为什么必须要求「独立成行 + 带分号」**：★★ ★ `DO $$ BEGIN … END $$;`
// ★ ★★ 这种匿名代码块里的 BEGIN **不是事务起点** ⇒ ⇒ ★★ 若用宽松匹配 ⇒ ⇒ ★★
// ★ ★★ 它会被误认成事务块 ⇒ ⇒ ⇒ ★★ ⇒ 判定器就会放过真正违规的文件 ⇒ ⇒ ⇒
// ★ ★★ ⇒ 门会变成恒绿 ⇒ ⇒ ⇒ ★★ ⇒ 而 856 等既有约定文件写的正是独立成行的
// ★ ★★ `BEGIN;` ⇒ ⇒ ⇒ ★★ ⇒ 口径与仓内既有约定一致。
var standaloneBeginRE = regexp.MustCompile(`(?im)^[ \t]*BEGIN\s*;`)

// setLocalRE 匹配 SET LOCAL（★ 大小写不敏感）。
var setLocalRE = regexp.MustCompile(`(?i)\bSET\s+LOCAL\b`)

// ★ ★★ **涂白函数不重写（★ §411：同一口径只能有一份实现）**：★★ ★ 同包已有
// ★ ★★ `stripSQLComments`（★ prereqs_schema_parity_test.go：★ 行注释 `--` 与块注释
// ★ ★★ `/* */`）⇒ ⇒ ★★ 直接用它 ⇒ ⇒ ★★ 免得两份实现漂移（★ 本轮第一版重写了一份
// ★ ★★ ⇒ ⇒ ★★ 编译当场报「redeclared」⇒ ⇒ ⇒ ★★ ⇒ ★ **这本身就是口径纪律在起作用**）。
//
// ★ ★★ **为什么必须涂白**：★★ ★ 迁移文件的**说明段**里会写「SET LOCAL」字样
// ★ ★★ （★ 927 的修复说明就写了）⇒ ⇒ ★★ ⇒ 若不涂白 ⇒ ⇒ ★★ 判定器会把**散文**
// ★ ★★ 当成**声明**⇒ ⇒ ⇒ ★★ ⇒ 「第一个 SET LOCAL 的位置」算错 ⇒ ⇒ ⇒ ★★
// ★ ★★ ⇒ 而且换个方向也算错（★ 注释里若先出现 SET LOCAL ⇒ ⇒ 会被判成
// ★ ★★ 「SET LOCAL 在 BEGIN 之前」⇒ ⇒ ⇒ ★★ ⇒ **假红**）⇒ ⇒ ⇒ ★★ ⇒ 所以涂白。

// setLocalGuardEffective 判定一份迁移里的 SET LOCAL 守卫**是否真的生效**。
//
// ★ 口径：★ 注释涂白后 ⇒ 若没有 SET LOCAL ⇒ ⇒ 无关紧要（★ 返回 true）；
// ★ 否则 ⇒ ⇒ 必须存在**独立成行的 BEGIN;** 且它出现在第一个 SET LOCAL **之前**。
func setLocalGuardEffective(body string) bool {
	clean := stripSQLComments(body)
	setIdx := setLocalRE.FindStringIndex(clean)
	if setIdx == nil {
		return true
	}
	beginIdx := standaloneBeginRE.FindStringIndex(clean)
	if beginIdx == nil {
		return false
	}
	return beginIdx[0] < setIdx[0]
}

// TestSetLocalEffectiveDetectorCanFire 是本门的**自证**（★ 三层）。
func TestSetLocalEffectiveDetectorCanFire(t *testing.T) {
	// ① 反向夹具 1：★ SET LOCAL 在事务块外 ⇒ ⇒ 必须判「不生效」。
	if setLocalGuardEffective("SET LOCAL lock_timeout = '2s';\nSELECT 1;\n") {
		t.Error("★ 判定器失灵：事务块外的 SET LOCAL 竟被判为生效")
	}
	// ② 反向夹具 2：★ SET LOCAL 在 BEGIN 之后 ⇒ ⇒ 必须判「生效」。
	if !setLocalGuardEffective("BEGIN;\nSET LOCAL lock_timeout = '2s';\nCOMMIT;\n") {
		t.Error("★ 判定器失灵：事务块内的 SET LOCAL 竟被判为不生效")
	}
	// ③ ★★ **注释不许参与判定**：★★ ★ 注释里先出现 SET LOCAL、声明里后出现
	//    BEGIN ⇒ ⇒ ⇒ 若不涂白 ⇒ ⇒ 判定器会误判 ⇒ ⇒ ⇒ ★★ ⇒ 必须仍然判「生效」。
	if !setLocalGuardEffective("-- 说明：这里会写 SET LOCAL 字样\nBEGIN;\nSET LOCAL lock_timeout = '2s';\nCOMMIT;\n") {
		t.Error("★ 判定器把注释里的 SET LOCAL 当成了声明 ⇒ ⇒ 涂白没生效")
	}
	// ④ DO $$ BEGIN … END $$; 里的 BEGIN **不是**事务起点 ⇒ ⇒ 必须判「不生效」。
	if setLocalGuardEffective("DO $$\nBEGIN\n  PERFORM 1;\nEND $$;\nSET LOCAL lock_timeout = '2s';\n") {
		t.Error("★ 判定器把 DO 匿名块里的 BEGIN 当成了事务起点 ⇒ ⇒ 口径不对")
	}
}

// TestStartupMigrationsAboveCutoffHaveEffectiveSetLocal 是主判据。
func TestStartupMigrationsAboveCutoffHaveEffectiveSetLocal(t *testing.T) {
	if _, err := os.Stat(canonicalStartupDir); err != nil {
		t.Fatalf("读不到 canonical 启动迁移目录 %s: %v", canonicalStartupDir, err)
	}
	entries, err := filepath.Glob(filepath.Join(canonicalStartupDir, "*.sql"))
	if err != nil {
		t.Fatalf("glob 失败: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("枚举到 0 个迁移文件 —— glob 路径写错或目录被搬走，" +
			"此时本门会以「没有违规」的形式空转全绿")
	}

	var checked, withSetLocal int
	for _, p := range entries {
		base := filepath.Base(p)
		if strings.HasSuffix(base, downMigrationSuffix) {
			continue // 回滚脚本不由 runner 执行
		}
		m := migrationVersionRE.FindStringSubmatch(base)
		if m == nil {
			continue // 非编号文件不在本门范围
		}
		digits := migrationLeadingDigitsRE.FindString(base)
		version, err := strconv.Atoi(digits)
		if err != nil {
			t.Fatalf("文件 %s 的前导数字不是整数: %v", base, err)
		}
		if version < setLocalGateCutoff {
			continue // 存量不拦（★ 见文件头的口径边界）
		}
		checked++
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("读 %s: %v", p, err)
		}
		body := string(b)
		if !setLocalRE.MatchString(stripSQLComments(body)) {
			continue // 没有 SET LOCAL ⇒ 与本门无关
		}
		withSetLocal++
		if !setLocalGuardEffective(body) {
			t.Errorf("★ %s 的 `SET LOCAL` 写在【事务块外】⇒ ⇒ PG 只发一条 WARNING，"+
				"★ ★ 守卫（lock_timeout / statement_timeout）**没有生效** ⇒ ⇒ ⇒ "+
				"★ ★ ADD CONSTRAINT UNIQUE 可以无限期等锁 ⇒ ⇒ ⇒ ★ ★ 修法：★ "+
				"★ 文件自带 BEGIN; … COMMIT;（★ 仓内既有约定，★ 如 856）", base)
		}
	}
	t.Logf("★ 扫描到 %d 个 ≥%d 的迁移，其中 %d 个含 SET LOCAL（★ 存量不拦）",
		checked, setLocalGateCutoff, withSetLocal)
}

// TestSetLocalPositiveControlExists 是**阳性对照**：★ 存量里必须找得到一份
// 「SET LOCAL 在事务块内」的迁移（★ 856）⇒ ⇒ 否则说明「找得到好形态」这件事
// 本身已经失效 ⇒ ⇒ ⇒ ★ 那本门就可能恒绿。
func TestSetLocalPositiveControlExists(t *testing.T) {
	control := "856_usage_ledger_ts_cover_agg_idx.sql"
	p := filepath.Join(canonicalStartupDir, control)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("★ 读阳性对照文件 %s 失败: %v", p, err)
	}
	if !setLocalRE.MatchString(stripSQLComments(string(b))) {
		t.Fatalf("★ 阳性对照文件 %s 里没有 SET LOCAL ⇒ ⇒ 形态已漂 ⇒ ⇒ 本门要重审口径", control)
	}
	if !setLocalGuardEffective(string(b)) {
		t.Fatalf("★ 阳性对照文件 %s 的 SET LOCAL 竟被判为不生效 ⇒ ⇒ 判定器口径漂了", control)
	}
}

// TestMigration927SetLocalIsEffective 钉住本轮修好的那一份（★ 927）。
//
// ★ 为什么单独钉：★ 927 是**本轮实测出的 P1**（★ 部署通道不包事务 ⇒ ★ SET LOCAL
// ★ 失效 ⇒ ★ ADD CONSTRAINT 可无限等锁 ⇒ ★ 且三步非原子 ⇒ ★ 「行已删、约束未建」
// ★ 的半应用状态可能）⇒ ⇒ ⇒ ★ 修完之后必须有一道判据钉住它 ⇒ ⇒ ⇒ ★ 否则下一次
// ★ 「顺手整理一下迁移文件」就会把它改回去。
func TestMigration927SetLocalIsEffective(t *testing.T) {
	p := filepath.Join(canonicalStartupDir, "927_routing_policy_singleton_unique.sql")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("★ 读 %s 失败: %v", p, err)
	}
	body := string(b)
	if !setLocalRE.MatchString(stripSQLComments(body)) {
		t.Fatalf("★ 927 里没有 SET LOCAL ⇒ ⇒ 形态已漂 ⇒ ⇒ 本门要重审")
	}
	if !setLocalGuardEffective(body) {
		t.Fatalf("★ 927 的 SET LOCAL 不生效 ⇒ ⇒ 本轮的修复被回退了")
	}
	// ★ 两份副本必须逐字节相同（★ embed 副本是安装器真正用的那份）。
	embed := filepath.Join("embeddata/startup", "927_routing_policy_singleton_unique.sql")
	eb, err := os.ReadFile(embed)
	if err != nil {
		t.Fatalf("★ 读 embed 副本 %s 失败: %v", embed, err)
	}
	if string(eb) != body {
		t.Fatalf("★ 927 的 embed 副本与 canonical 不一致 ⇒ ⇒ 安装器带的 SQL 与正典不同")
	}
}

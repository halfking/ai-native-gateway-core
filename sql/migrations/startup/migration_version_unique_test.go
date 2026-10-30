package startup

import (
	"crypto/sha256"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestNumericUpMigrationVersionsAreUnique(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}

	versionPattern := regexp.MustCompile(`^([0-9]+)_.+\.sql$`)
	seen := make(map[string]string)
	historicalCollisions := map[string]map[string]bool{
		"478": {"478_auto_route_affinity.sql": true, "478_model_reasoning_caps.sql": true},
		"640": {"640_normalize_credential_health_status.sql": true, "640_session_turns_protocol_fields.sql": true},
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasSuffix(name, ".down.sql") {
			continue
		}
		match := versionPattern.FindStringSubmatch(name)
		if match == nil {
			continue
		}
		version, err := strconv.Atoi(match[1])
		// Versions before 492 contain audited historical collisions. The current
		// migration line is collision-free and must stay that way.
		if err != nil || version < 492 {
			continue
		}
		if previous, exists := seen[match[1]]; exists {
			if allowed := historicalCollisions[match[1]]; allowed != nil && allowed[previous] && allowed[name] {
				continue
			}
			t.Fatalf("startup migration version %s is duplicated by %s and %s", match[1], previous, name)
		}
		seen[match[1]] = name
	}
}

func TestDeployedMigration528ChecksumIsStable(t *testing.T) {
	const (
		name = "528_request_logs_bodies_expired_hot_cleanup.sql"
		want = "6998edec4991e2cd261061c5094e21829b85d699becabeaadc3a86dfc5d56cd1"
	)
	body, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(body)); got != want {
		t.Fatalf("deployed startup migration %s checksum changed: got %s want %s", name, got, want)
	}
}

func TestDeployedMigration529ChecksumIsStable(t *testing.T) {
	const (
		name = "529_repair_shared_pg_sticky_and_bodies_2026_07.sql"
		want = "3a21c6943da9ede672c4a0f2074bcbfee3e94efcd4e84dec09b12df3154d41c6"
	)
	body, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(body)); got != want {
		t.Fatalf("deployed startup migration %s checksum changed: got %s want %s", name, got, want)
	}
}

// TestHeavyDDLModernMigrationsDeclareMeasuredBudget — 全表扫描类 DDL 的超时预算
// 必须按「目标环境最大表」实测，而不是按本地测试表拍（24h 审计第 7 轮 §5b-2）：
//
//	914 给 statement_timeout 拍了 120s（本地 252-dev 的会话族小表下绰绰有余），
//	245 的 session_turns 实测 VALIDATE 需 2m25s ⇒ 部署中止（事务回滚，无半应用），
//	只能改超时副本手工应用 + 双账本 stamp 收场。
//	761 先例：回填 252 实测 18.9s ⇒ SET LOCAL statement_timeout='10min'
//	（索引维护占大头，预算不贴实测值，按数量级抬）。
//
// 判据（可证伪，版本 ≥ 900 的现行线）：up 迁移里出现
//   - VALIDATE CONSTRAINT（存量行全表校验，914 烧点本体），或
//   - 不带 CONCURRENTLY 的 CREATE INDEX（建索引扫描全表）
//
// 就必须同时满足：
//  1. 文件里显式 SET statement_timeout 且最大值 ≥ 10min（继承 761 先例的数量级）；
//  2. 文件里有「实测」注释，写明量的是哪个环境哪张表、量出多少 ——
//     没有量过的预算就是拍脑袋，下一个环境会替你重新发现它。
//
// 存量豁免（字节已冻结——checksum 账本记录原文件，改字节会连锁 break）：
//   - 900：未显式 SET（靠角色级 30s 侥幸通过；表小），
//   - 914：120s，245 实测不足，已按「改副本 + stamp」在 245 收场；
//     211/252/154 侧均已应用（2026-10-30 复核 252 PG convalidated=t）。
func TestHeavyDDLModernMigrationsDeclareMeasuredBudget(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	// 与 TestNumericUpMigrationVersionsAreUnique 同款豁免表形态。
	grandfathered := map[string]string{
		"900": "applied；字节冻结；表小靠角色级 30s 侥幸通过",
		"914": "applied(211/245/252/154)；字节冻结；245 实测 VALIDATE 2m25s > 120s 已按改副本+stamp 收场",
		// 2026-10-31 逐行判据上线时抓到的旧判据假绿：912/913 头注释里的
		// 「非 CONCURRENTLY」字样让旧【整文件】CONCURRENTLY 匹配误判豁免。
		// 实质不重：两者的 CREATE INDEX 全部建在【同文件刚新建的空表】上
		// （912: session_bodies_trash/session_hidden；913: maas_* 四表，
		// 无存量可扫），30s 合理。
		"912": "applied；字节冻结；索引建在同文件新建的空表上（无存量扫描），30s 合理",
		"913": "applied；字节冻结；索引建在同文件新建的空表上（无存量扫描），30s 合理",
		// 917/921：sanitize 审核工作台新功能表（2026-10-30 上线、表龄以天计、
		// 无大存量回填），索引构建 30s 内量级充分；风险在锁等待（已配
		// lock_timeout=2s 快速失败）而非扫描时长。表若长期增长到 VALIDATE/
		// 索引重建量级，应按 761 先例抬预算——豁免不是永久许可。
		"917": "applied；字节冻结；新功能表无大存量（sanitize_block_reviews 等 3 表 + 索引）",
		"921": "applied；字节冻结；新功能表无大存量（evidence 表 + reviews 补索引）",
	}
		// 全表扫描类 DDL 的形态。
		validateRe := regexp.MustCompile(`(?i)VALIDATE\s+CONSTRAINT`)
		indexRe := regexp.MustCompile(`(?i)CREATE\s+(UNIQUE\s+)?INDEX`)
		concurrentlyRe := regexp.MustCompile(`(?i)CONCURRENTLY`)
		timeoutRe := regexp.MustCompile(`(?i)SET\s+(?:LOCAL\s+)?statement_timeout\s*=\s*'([^']+)'`)

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasSuffix(name, ".down.sql") {
			continue
		}
		match := regexp.MustCompile(`^([0-9]+)_.+\.sql$`).FindStringSubmatch(name)
		if match == nil {
			continue
		}
		version, err := strconv.Atoi(match[1])
		if err != nil || version < 900 {
			continue
		}
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		sqlText := string(body)

		// 2026-10-31 批判式审计订正：混合文件盲区——旧判据是
		// 「文件里有 CREATE INDEX ∧ 文件里没有 CONCURRENTLY」，一条
		// CREATE INDEX CONCURRENTLY 会把**同文件里另一条非并发** CREATE INDEX
		// 一起放走（后者仍全表扫）。改为逐行判定：每条建索引行自己带
		// CONCURRENTLY 才豁免（仓内惯例建索引单行书写；跨行语句由该门
		// 的 fail-closed 方向兜住——宁可误红也不放走真全表扫）。
		heavy := validateRe.MatchString(sqlText)
		if !heavy {
			for _, line := range strings.Split(sqlText, "\n") {
				if indexRe.MatchString(line) && !concurrentlyRe.MatchString(line) {
					heavy = true
					break
				}
			}
		}
		if !heavy {
			continue
		}
		if reason, ok := grandfathered[match[1]]; ok {
			t.Logf("grandfathered %s: %s", name, reason)
			continue
		}

		maxSeconds := 0
		for _, m := range timeoutRe.FindAllStringSubmatch(sqlText, -1) {
			if s, ok := durationSeconds(m[1]); ok && s > maxSeconds {
				maxSeconds = s
			}
		}
		if maxSeconds < 600 {
			t.Fatalf("%s 含全表扫描类 DDL（VALIDATE CONSTRAINT / 非 CONCURRENTLY CREATE INDEX），"+
				"但最宽 statement_timeout=%ds < 10min。预算必须按目标环境最大表实测后抬够"+
				"（761 先例：实测 18.9s ⇒ 抬 10min；914 反例：拍 120s ⇒ 245 部署中止）。", name, maxSeconds)
		}
		if !strings.Contains(sqlText, "实测") {
			t.Fatalf("%s 含全表扫描类 DDL 但没有「实测」注释。请写明量的是哪个环境哪张表、"+
				"量出多少，再落预算（没有量过的预算就是拍脑袋）。", name)
		}
	}
}

// durationSeconds 解析 PG duration 字面量的秒数（本门只需 s/min/h 三种形态）。
func durationSeconds(lit string) (int, bool) {
	lit = strings.TrimSpace(strings.ToLower(lit))
	if lit == "" {
		return 0, false
	}
	unit := "s"
	num := lit
	switch {
	case strings.HasSuffix(lit, "min"):
		unit, num = "min", strings.TrimSuffix(lit, "min")
	case strings.HasSuffix(lit, "h"):
		unit, num = "h", strings.TrimSuffix(lit, "h")
	case strings.HasSuffix(lit, "s"):
		unit, num = "s", strings.TrimSuffix(lit, "s")
	}
	var n int
	if _, err := fmt.Sscanf(strings.TrimSpace(num), "%d", &n); err != nil {
		return 0, false
	}
	switch unit {
	case "min":
		return n * 60, true
	case "h":
		return n * 3600, true
	default:
		return n, true
	}
}

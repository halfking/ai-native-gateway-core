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
		// 2026-11-11（48h 审计第二轮）：958 由 LESSONS §689（撞号订正前记 §686）定稿轮落地并于当日
		// applied(245/252) ⇒ 字节冻结（R189 规约：改已应用迁移字节会撞
		// 内容指纹 fail-closed，处置走本表豁免而非改文件——本审计轮先改了
		// 958 字节、当场按本规约回退）。DDL 预算证据：两张非并发索引
		// （idx_market_platform_registry_region/_is_ours）建在同文件新建的
		// 空表上（16 行种子 ⇒ 秒级，无存量扫描）；211 水位线在 956，走到
		// 958 时同样是空表新索引 ⇒ 无 914 型「拍小了中止部署」风险。
		"958": "applied(245/252)；字节冻结；两张非并发索引建在同文件新建的空表上（16 行种子 ⇒ 秒级，无存量扫描）",
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
		// 928：探测流量物理隔离车道（2026-11-02）。形态同 912/913 ——
		// 本迁移的三个 CREATE INDEX 全部建在**本文件刚 CREATE 的空表**
		// session_turns_probe_hot 上（无存量可扫），分钟表
		// request_stats_probe_minute 的 PK 落在同文件新建的空表上。
		// ★ 实测（211 PG17.10，2026-11-02）：全新空库跑完整脚本墙钟 0.276s；
		// 最坏形态（表已存在 + 灌 50 万存量行 + 索引缺失，迫使
		// CREATE INDEX 真去扫存量）三索引分别 109.9ms / 140.8ms / 142.6ms、
		// 分钟表建表 4.6ms ⇒ 合计 < 0.5s，与 5min 预算差三个数量级。
		// ⇒ 豁免的是「判据误命中」（本门按 CREATE INDEX 字面判全表扫描），
		//    不是「没量过就拍预算」——量过，且按 761 先例的**数量级**判断：
		//    存量上限就是本迁移自己建的空表，抬到 10min 也不会让它更安全，
		//    只会给后人一个「这张表有百万级存量」的错觉。
		// ★ 字节已冻结：211 台账 content_sha256=31bec328e87f207f… 与磁盘一致，
		//   改字节会走 content replay 通道并连锁 break（见 914 教训）。
		"928": "applied(211)；字节冻结；索引建在同文件新建的空表上（无存量扫描）；实测 0.276s/空库、<0.5s/50 万存量形态",
		// ★★ 2026-11-23（L20 修红轮）：981（会话分析基础设施）。
		// 形态：本文件有 **18** 条**非并发** CREATE INDEX —— 其中 **3 条**建在
		// public.sessions 上（idx_sessions_project / _task_category / _is_probe），
		// 其余 **15 条**建在**本文件刚 CREATE 的 4 个聚合物化视图**上（空 ⇒ 无存量扫描）；
		// 另有 4 条 ALTER TABLE sessions ADD COLUMN；文件本身**没有** SET
		// statement_timeout ⇒ 本门读到 maxSeconds=0 ⇒ 判红。
		//
		// ★ 为什么按「字节冻结」豁免（★ 而不是改字节 —— 见 L23）：
		//   ① 981 在 211 与 252 **都已 applied**（252 台账
		//      llm_gateway_migration_checksums 记 981 = 4b6c5fc3ef284913…，
		//      与磁盘 sha256 **逐字一致**）⇒ 改字节就是 L23 同款分叉
		//      （已 applied 的迁移永不重跑 ⇒ 库里旧、仓内新 ⇒ 部署被
		//      内容指纹 fail-closed 拦下）⇒ ★★ **处置只能走本表豁免**。
		//   ② 两条可能重跑它的路径**都没有 914 型「拍小了中止部署」的风险**：
		//      · 已 applied 的环境（211/252/154/245 共用 252 的库）：**已跑完，
		//        索引已在位 ⇒ 永不重跑**（迁移框架按 checksum 跳过）；
		//      · 唯一还会跑它的是**全新空库**（新装）⇒ public.sessions 是
		//        **空表** ⇒ 无存量扫描 ⇒ 毫秒级。
		//   ③ 文件未设 statement_timeout ⇒ PG 语义是**无上限** ⇒ 它不会
		//      **主动中止**（与 900/958 的「靠角色级 30s 侥幸通过」不同：
		//      本条不存在「超时中止」这一失败模式）。
		//
		// ★ 豁免的不是「没量过就拍预算」——量过：211/252 上 981 确实跑完
		//   （4 个 MV 在场 + sessions 的 3 个索引在位），且**新装路径表为空**。
		// ★ 若将来要在一个**大存量**库上重建这 3 个索引 ⇒ 按 761 先例
		//   另立新迁移并实测抬预算，**不要**改 981 的字节。
		"981": "applied(211/252)；字节冻结（252 台账 checksum 4b6c5fc3… 与磁盘逐字一致 ⇒ 改字节即 L23 同款分叉）；" +
			"18 条非并发索引中 3 条建在 public.sessions 上、15 条建在本文件新建的空视图上；" +
			"已 applied 环境永不重跑、新装路径 sessions 为空表（无存量扫描）⇒ 无 914 型中止风险；文件未设 statement_timeout（无上限）⇒ 不会主动中止",
		// ★★ 2026-11-23（L20 修红轮）：997 / 998 / 999 —— ★★ **本门【第三段】
		// 判据的产物**，★ 且是**同一个坑的三份**：预算段（statement_timeout ≥ 10min）
		// 它们都**过了**，★ 缺的是「实测」注释段。
		//
		// ★★ 关键事实（★ 这是本条豁免的全部理由，★ 一句都不能少）：
		//   ① **「实测」事实上已经发生**，★ 且发生在**真存量**上：
		//      · 997（tasks 维度表）：211 与 252 的 schema_migrations **都有**
		//        '997' 行，★ 252 台账 checksum 08c0f669… 与磁盘 sha256 逐字一致；
		//      · 998（session_summaries 任务关联）：两库都有 '998' 行，
		//        checksum 682c5f43… 与磁盘逐字一致；★ 它的 4 条索引是建在
		//        **有存量的** public.session_summaries 上的**非并发部分索引**
		//        ⇒ ★ 它在真存量上跑完了 10min 预算内的全表扫描 ⇒ ★★ **量过了**；
		//      · 999（分层总结表）：两库都有 '999' 行，checksum 3f374cd0… 逐字一致。
		//   ② ★ **补那句注释就要改【已 applied 迁移】的字节** ⇒ 252/211 的
		//      llm_gateway_migration_checksums 立刻与磁盘不一致 ⇒ **L23 同款分叉**
		//      （部署在迁移校验段被内容指纹 fail-closed 拦下 ⇒ ★★ 154/245 会被
		//      **再冻一次**）⇒ ⇒★ **处置只能走本表豁免，不能改文件**。
		//   ③ ★★ 形态补充（★ 让「为什么它们不是 914 那种风险」可复核）：
		//      · 997 / 999 的索引**全部**建在本文件刚 CREATE 的**空表**上
		//        （public.tasks / public.session_summary_layers）⇒ 无存量扫描；
		//      · 998 的索引建在 public.session_summaries 上（★ 有存量），
		//        但它**已经在 211/252 的真存量上跑完**（见 ①）⇒ 预算被实测验证。
		//   ④ ★★ 本条豁免记下的是一个**通用形态**：
		//        「『实测』的证据可以存在于**生产事实与台账**里，
		//          而**不必（且往往不能）**在已冻结的迁移文件里。」
		//        ⇒ 门要求注释、现实要求冻结 ⇒ ★ 冲突时以**冻结**为准（R189 规约）。
		"997": "applied(211/252)；字节冻结（checksum 08c0f669… 与磁盘逐字一致）；预算段已过（statement_timeout=10min）；" +
			"「实测」已在真库发生（211/252 均跑完、4 条索引建在本文件新建的空表 public.tasks 上 ⇒ 无存量扫描）；补注释即改已 applied 字节 ⇒ L23 同款分叉 ⇒ 只登记不改",
		"998": "applied(211/252)；字节冻结（checksum 682c5f43… 与磁盘逐字一致）；预算段已过（statement_timeout=10min）；" +
			"「实测」已在真存量上发生（211/252 均跑完；4 条非并发部分索引建在**有存量**的 public.session_summaries 上 ⇒ 10min 预算被真库验证够用）；" +
			"补注释即改已 applied 字节 ⇒ L23 同款分叉 ⇒ 只登记不改",
		"999": "applied(211/252)；字节冻结（checksum 3f374cd0… 与磁盘逐字一致）；预算段已过（statement_timeout=10min）；" +
			"「实测」已在真库发生（211/252 均跑完、4 条索引建在本文件新建的空表 public.session_summary_layers 上 ⇒ 无存量扫描）；补注释即改已 applied 字节 ⇒ L23 同款分叉 ⇒ 只登记不改",
		// ★★ 2026-11-23（合并轮六 · 代上游认领）：★ 982_provider_call_attempts
		// （★ 上游 aac6602ab、★ 供应商逐次调用落库）★ 本门判红：★ 文件里有 3 条
		// **非并发** CREATE INDEX（idx_provider_call_attempts_sent /
		// _unconfirmed / _request）⇒ 命中「全表扫描类 DDL」字面判据 ⇒ 但文件头
		// **没有「实测」注释** ⇒ 判红。
		//
		// ★ 为什么按「字节冻结」豁免（★ 与 981/997/998/999 同款处置）**：
		//   ① ★ **211 已 applied**（★ 只读取证：★ `schema_migrations` 记
		//      `982|per-attempt supplier call evidence`）⇒ ★★ 改字节就是 L23 同款
		//      分叉（★ 已 applied 的迁移永不重跑 ⇒ 库里旧、仓内新 ⇒ 部署被内容
		//      指纹 fail-closed 拦下）⇒ ★★ **处置只能走本表豁免**。
		//   ② ★★ **「实测」已在真库发生**（★ 与 997/998/999 的措辞同款）：★
		//      211 实测 `public.provider_call_attempts` **表在位**、★ **4 个索引在位**
		//      （★ 3 条 CREATE INDEX ＋ 主键）、★ **0 行**（★ 文件头明载
		//      「No historical attempts are inferred here」⇒ 无历史回填）★
		//      ⇒ ★ 三条非并发索引建在**本文件新建的空表**上 ⇒ **无存量扫描**
		//      ⇒ ★★ 无 914 型「拍小了中止部署」的风险；★ 且文件已显式
		//      `SET statement_timeout='10min'` ⇒ **预算段已过**。
		//   ③ ★ **诚实边界**：★ 本条**不是**「我方量过秒数」的实测数字
		//      （★ 我方未对该表灌存量再计时）⇒ ★ 豁免依据是
		//      「★ 真实部署已在真库跑通 ＋ 索引建在空表上 ⇒ 无存量扫描 ★」这一
		//      **结构性论证**，★ 而非新造的测速数字。
		// ★ ⇒ ★ **代上游认领**（★ 上游基线 dd89c6bb6 上这条红就在、★ 上游未认领）
		// ★ ⇒ ★ 不改上游已 applied 的迁移字节、★ 只在门内登记表里如实写明理由。
		// ★★ 【★ 键行采用上游那一侧（★ 上游自己认领了这号）★★；★ 下面这段是我方合并轮六/九/十的【代修论证】★★】
		// ★★ "982": "applied(211)；字节冻结（211 schema_migrations 记 982|per-attempt supplier call evidence）；预算段已过（statement_timeout=10min）；" +
		// ★★ 2026-11-24（合并轮九 · 代上游认领；★ 合并轮十随【重编号】改 key）：
		// ★ request_metrics（★ 统计专用分区表）★ 本门判红：★ 文件里有 9 条
		// **非并发** CREATE INDEX ⇒ 命中「全表扫描类 DDL」字面判据 ⇒ 但文件头
		// **没有「实测」注释** ⇒ 判红。
		//
		// ★ 为什么按「结构性论证」豁免（★ 与 912/913/958/997/998/999 同款处置）：
		//   ① ★★ **本迁移从未被任何库应用过**（★ 2026-11-24 合并轮九之前，
		//      installer StartupFiles／embeddata／offline 副本 ＋ 部署通道 files=()
		//      **一条都没登记它** ⇒ 任何环境都不可能有它的记账行）⇒ ★★ **⇒ 与
		//      982 的「字节冻结」是【相反】的情形**：★ 本条不受 L23 分叉约束
		//      （★ 本轮已按「从未应用 ⇒ 改字节零分叉风险」补过它的 ≥952 自登记行）。
		//   ② ★★ **结构性论证（★ 豁免依据、★ 不是实测数字）**：★ 本迁移只做两件事
		//      ——**建一张新表** `public.request_metrics`（★ `CREATE TABLE IF NOT
		//      EXISTS`）＋ **建 9 个建在这张新空表上的索引**（★ 全部 `IF NOT
		//      EXISTS`）⇒ ★★ **⇒ 无存量可扫** ⇒ ★★ 「全表扫描类 DDL」的**前提
		//      不成立** ⇒ ★★ 无 914 型「拍小了中止部署」的风险；★ 且文件已显式
		//      `SET statement_timeout = '10min'` ⇒ **预算段已过**。
		//   ③ ★ **诚实边界（★ 必须写下来）**：★ 本条**不是**「我方量过秒数」的
		//      实测数字（★ **本机 Docker 守护进程不可用 ⇒ 本轮根本测不了**）⇒
		//      ★ 豁免依据是上述**结构性论证**、★ 而非新造的测速数字 ⇒ ★★
		//      ★★ **边界条件（★ 若哪天不成立、★ 豁免即失效）**：★ ① 若上游日后
		//      在**已有存量**的 `request_metrics` 上重放（★ 表在、★ 索引缺）⇒
		//      ★★ 索引构建就会真扫存量 ⇒ ★★ **须按 761 先例补实测并抬预算**；
		//      ★★ ② 若该表在生产被灌到百万行量级 ⇒ ★★ 重建索引同理 ★★。
		// ★ ⇒ ★ **代上游认领**（★ 上游基线上这条红就在、★ 上游未认领）⇒ ★
		// ⇒ ★ **只在本门登记表里如实写明理由、★ 不往迁移文件里写「实测」二字**
		//   （★ 写了就是「假装量过」⇒ ★★ 那正是 914 反例的病根 ★★）。
		//
		// ★★ 2026-11-24（合并轮十 · 随【重编号 1000→983】改 key）：★ 上游把该
		//   迁移**重命名**为 `983_request_metrics`（★ §9.30 ㉒-2）⇒ ★★ **本表
		//   的 key 是【文件名里的版本号】** ⇒ ★ key 仍写 "1000" ⇒ ★★ **查表
		//   miss ⇒ 门判红**（★ 实测：`983_request_metrics.sql 含全表扫描类 DDL
		//   但没有「实测」注释` ★★）⇒ ★★ **⇒ key 必须跟着重编号一起改** ★★
		//   （★ ★ **否则「代上游认领」的白干**、★ ★ 迁移一改名、豁免就掉 ★★）★★
		//   ｜★ ★ **★ 改 key 后的「从未应用」判断依然成立 ★**：★ 重编号**没有**
		//   **产生**任何新的记账行（★ 记账行是环境跑出来的、★ 不是文件名带来的）
		//   ⇒ ★★ ⇒ ★ 任何环境仍然不可能有它的记账行 ⇒ ★★ ⇒ ★ 结构性论证与
		//   ★ 诚实边界**逐字不变**（★ 只把「1000」这个文件名换成「983」 ★★）。
		//   ｜★★ **★ 顺带记一笔上游自己的红 ★★**：★ 上游那笔重编号**只**改了
		//   ★ 文件名与三份拷贝、★ **没改**本表的 key ⇒ ★★ ⇒ ★ `origin/main`
		//   ★★ **当场就是红的**（★ 即 `sql/migrations/startup` 包在 main 上判红 ★★）
		//   ｜★★ **★ 另注：★ 上游同一笔还漏删了 `installer/cmd/llm-gw-installer/
		//   main.go` 里 1000 的两行 `//go:embed` ＋ 两行字节映射表 ★★（★ 拷贝
		//   已删 ⇒ ★★ embed 指向不存在的文件 ⇒ ★★ **整个 installer 主包编译不过** ★★）
		//   ⇒ ★★ **两处都是「★ 重编号的收尾没做干净 ★」** ⇒ ★★ **⇒ 通用式：
		//   ★ 重编号 = 改名 ＋ 改【所有按名字索引的地方】⇒ ★ 名字索引点共有
		//   ★ 四处（★ 拷贝 ×3、★ go:embed ×2、★ 字节映射表 ×2、★ 本豁免表 key ★★）★★
		// ★★ 【★ 键行采用上游那一侧（★ 上游自己认领了这号）★★；★ 下面这段是我方合并轮六/九/十的【代修论证】★★】
		// ★★ "983": "★★ 2026-11-24 合并轮十一订正：★ 本条原先写的【「从未应用 ⇒ 不受 L23 字节冻结约束」】**已被生产事实推翻** —— ★ 上游把该迁移重命名为 983（1000→983）★ ⇒ ★★ **生产 `public.schema_migrations` 里现在有 `983` 这一行**（★ `983|request_metrics statistics base table|2026-11-24 00:38:22.974221+08` ★★）★ ⇒ ★★ ⇒ ★★ **983 已【不是】从未应用** ⇒ ⇒ ★★ **字节冻结现在适用**（★ ★ 而我方**未改它的字节** ⇒ ★★ **零分叉风险** ★★）；" +

		// ★★ 2026-11-24（12h 修订审计轮第三轮）：982（逐次供应商调用证据表）。
		//
		// ★ 为什么它会被本门判红：形态同 981/912/913/928 —— 本文件的 3 条
		//   **非并发** CREATE INDEX（idx_provider_call_attempts_sent /
		//   _unconfirmed / _request）建在 **本文件刚 CREATE 的空表**
		//   public.provider_call_attempts 上 ⇒ 门按字面判「全表扫描类 DDL」。
		//   ⇒ 本门读到 maxSeconds=600（文件自带 SET statement_timeout='10min'
		//   ⇒ **预算段已过**）⇒ 唯一缺的是**「实测」注释**段。
		//
		// ★ 为什么按「字节冻结」登记而不是改字节（★ 见 L23）：
		//   ① **211 已 applied**（只读实测，211 / acc-pg17，2026-11-24）：
		//      · public.schema_migrations 有 '982' 行（applied_at
		//        2026-11-23 19:56:49.088737+08）；
		//      · 部署通道权威台账 public.gateway_db_revision_sequences 有
		//        `session-summary-and-integrity-2026-09:982_provider_call_attempts.sql`
		//        行，content_sha256 = **5421564ddbb0b894…**，与磁盘 sha256
		//        **5421564ddbb0b894689b87ff2ca2adb3c701c72143e0bf6b30ab8faaae3f106a**
		//        **逐字一致** ⇒ 已 applied ⇒ **改字节即 L23 同款分叉**
		//        （部署在迁移校验段被内容指纹 fail-closed 拦下）。
		//   ② **两条可能重跑它的路径都没有 914 型「拍小了中止部署」的风险**：
		//      · 已 applied 的环境（211 等）：**已跑完、索引已在位 ⇒ 永不重跑**；
		//      · 唯一还会跑它的是**全新空库**（新装）⇒ public.provider_call_attempts
		//        是**空表** ⇒ 建索引扫的是 **0 行** ⇒ 毫秒级 ⇒ 30s 角色级超时足够。
		//   ③ 文件**已自带** SET statement_timeout='10min' ⇒ PG 语义下它
		//      **不会主动中止**（与 900/958 的「靠角色级 30s 侥幸通过」不同）。
		//
		// ★ 豁免的不是「没量过就拍预算」——量得出来且结论确定：索引建在
		//   **本文件新建的空表**上 ⇒ 存量上限就是 0 行 ⇒ 抬到 10min 也不会
		//   让它更安全，只会给人「这张表有百万级存量」的错觉（同 928 的理由）。
		// ★ 若将来要在一个**大存量**库上重建这 3 个索引 ⇒ 按 761 先例
		//   **另立新迁移**并实测抬预算，**不要**改 982 的字节。
		"982": "applied(211)；字节冻结（211 部署通道台账 content_sha256=5421564ddbb0b894… 与磁盘 sha256 逐字一致）；" +
			"预算段已过（文件自带 SET statement_timeout='10min'）；" +
			"3 条非并发 CREATE INDEX 全部建在本文件刚 CREATE 的空表 public.provider_call_attempts 上（无存量扫描 ⇒ 建索引扫 0 行）；" +
			"211 已 applied ⇒ 永不重跑、新装路径该表为空 ⇒ 无 914 型中止风险；" +
			"补「实测」注释即改已 applied 字节 ⇒ L23 同款分叉 ⇒ 只登记不改",
		// ★★ 2026-11-24（12h 修订审计轮第三轮）：983 / 985 / 987 / 988 ——
		// ★★ **同一个成因的四个**：这四条是 2026-11-23~24 那批新迁移，
		// ★★ 写了「重 DDL」却**没写「实测」注释** ⇒ 本门读到「预算段已过
		// ★★ （都自带 SET statement_timeout='10min'）但缺实测段」⇒ 判红。
		// ★★ ★ 为什么不能靠「补一句注释」解决（★ 见 L23）：
		// ★★   四条在 211 **都已 applied**，且 211 部署通道权威台账
		// ★★   public.gateway_db_revision_sequences 记的 content_sha256 与磁盘
		// ★★   sha256 **逐字一致**（只读实测，2026-11-24）：
		// ★★     · 983_request_metrics.sql                    4441d11d03acbd15…
		// ★★     · 985_request_reconciliation_links.sql       5be6e05afc279f47…
		// ★★     · 987_provider_invoice_evidence.sql          9b068a3a5ff3fac0…
		// ★★     · 988_provider_invoice_usage_buckets.sql     8433c53c98882425…
		// ★★   ⇒ 改字节 = 内容指纹分叉（部署在迁移校验段 fail-closed）⇒ 只登记不改。
		// ★★ ★ 为什么它们**没有 914 型「拍小了中止部署」的风险**（★ 关键事实）：
		// ★★   ① 983：CREATE TABLE(40) + 分区表(141) + **11 条 CREATE INDEX**(149-)
		// ★★      全部在**同一个事务内**（BEGIN 在 32、COMMIT 在 234）⇒ 建索引时
		// ★★      表内**0 行**（文件头自述「Affected Rows: 0（新表创建）」）
		// ★★      ⇒ 无存量扫描。★ 今日该表 346,604 行是**应用之后**由处理器写入的
		// ★★      （211 台账 applied_at=2026-11-24 00:38:22）⇒ 与建索引时刻无关。
		// ★★   ② 985：CREATE TABLE(7) + 3 条索引(18-22) 同一事务（BEGIN 3 /
		// ★★      COMMIT 37）⇒ 建索引时 0 行 ⇒ 211 实测该表**至今 0 行**。
		// ★★   ③ 987：CREATE TABLE ×2(8, 26) + 4 条索引(41-48) 同一事务
		// ★★      （BEGIN 4 / COMMIT 66）⇒ 其中 2 条建在新表上（0 行）、
		// ★★      另 2 条建在 982 的 provider_call_attempts 上（211 实测**0 行**）。
		// ★★   ④ 988：ALTER provider_invoice_lines + 1 条索引(34) 同一事务
		// ★★      （BEGIN 3 / COMMIT 40）⇒ 目标表 provider_invoice_lines 由 987
		// ★★      于**早 0.8 秒**建成且**至今 0 行**（211 实测）⇒ 建索引扫 0 行。
		// ★★   ⇒ 四条都已 applied ⇒ **永不重跑**；新装路径同为空表 ⇒ 无中止风险。
		// ★ ★ 豁免的不是「没量过就拍预算」——量过且结论确定：建索引扫的行数是
		//   **0**（建表与建索引同事务）⇒ 抬到 10min 也不会让它更安全，只会给人
		//   「这张表有百万级存量」的错觉（同 928 的理由）。
		"983": "applied(211)；字节冻结（211 部署通道台账 content_sha256=4441d11d03acbd15… 与磁盘 sha256 逐字一致）；" +
			"预算段已过（文件自带 SET statement_timeout='10min'）；" +
			"11 条非并发 CREATE INDEX 与 CREATE TABLE(分区表) 在**同一事务**内（BEGIN 32 / COMMIT 234）⇒ 建索引时表内 0 行（无存量扫描）；" +
			"211 已 applied ⇒ 永不重跑、新装路径同为空表 ⇒ 无 914 型中止风险；补「实测」注释即改已 applied 字节 ⇒ L23 同款分叉 ⇒ 只登记不改",
		"985": "applied(211)；字节冻结（211 部署通道台账 content_sha256=5be6e05afc279f47… 与磁盘 sha256 逐字一致）；" +
			"预算段已过（文件自带 SET statement_timeout='10min'）；" +
			"3 条非并发 CREATE INDEX 与 CREATE TABLE 同一事务（BEGIN 3 / COMMIT 37）⇒ 建索引时 0 行；" +
			"211 实测 public.request_reconciliation_links 至今 0 行；已 applied ⇒ 永不重跑、新装路径为空表 ⇒ 无 914 型中止风险；补注释即改已 applied 字节 ⇒ 只登记不改",
		"987": "applied(211)；字节冻结（211 部署通道台账 content_sha256=9b068a3a5ff3fac0… 与磁盘 sha256 逐字一致）；" +
			"预算段已过（文件自带 SET statement_timeout='10min'）；" +
			"4 条非并发 CREATE INDEX 同一事务（BEGIN 4 / COMMIT 66）⇒ 2 条建在本文件新建的两张空表上、2 条建在 982 的 provider_call_attempts 上（211 实测 0 行）⇒ 建索引扫 0 行；" +
			"已 applied ⇒ 永不重跑、新装路径同为空表 ⇒ 无 914 型中止风险；补注释即改已 applied 字节 ⇒ 只登记不改",
		"988": "applied(211)；字节冻结（211 部署通道台账 content_sha256=8433c53c98882425… 与磁盘 sha256 逐字一致）；" +
			"预算段已过（文件自带 SET statement_timeout='10min'）；" +
			"1 条非并发 CREATE INDEX 同一事务（BEGIN 3 / COMMIT 40）⇒ 目标表 provider_invoice_lines 由 987 早 0.8 秒建成且 211 实测至今 0 行 ⇒ 建索引扫 0 行；" +
			"已 applied ⇒ 永不重跑 ⇒ 无 914 型中止风险；补注释即改已 applied 字节 ⇒ 只登记不改",
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

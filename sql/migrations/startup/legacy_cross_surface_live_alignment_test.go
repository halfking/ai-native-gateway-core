package startup

// legacy_cross_surface_live_alignment_test.go —— ★ 「legacy 跨面视图对」在**活库**上的
// 位置对齐核对（★ **纯只读**、★ 可指向生产）★
//
// ★★ 这道门防什么（★ 2026-11-23 在生产 211 / 252 上**只读实测**出来的）★★
//
//	★ 登记表 `legacyCrossSurfaceSelectStar`（★ 同包 hot_surface_select_star_contract_test.go）
//	★ 记着 11 条**历史写法**：★ 跨面视图用
//	★ `SELECT * FROM <t>_hot UNION ALL SELECT * FROM <t>` ⇒ ★ **按位置对齐**
//	★ ⇒ ★★ 两张表的列序一旦分叉、★ 这条迁移**再也重放不了**（★ PostgreSQL：
//	★ `UNION types integer and text cannot be matched`）★★。
//	★★ 而**仓里从来没有一道门核对过「这些历史迁移在今天这张库上还重放得了吗」** ★★
//	★★ ⇒ ★★ 本文件补的就是这一半（★ 另一半＝静态那半、★ 已由 contract 门守住）★★。
//
// ★★ ★★ 与【上游 L34 五轮/六轮那道 shell 门】的分工（★ 2026-11-24 · ★ 同题不同问、★ 不是重复实现 ★★）★★
//
//	★ 上游那道：`scripts/checks/legacy-select-star-column-order-gate.sh`（★ shell 门、★ 进 `make guards-select-star-order` ＋ `verify.sh`）★
//	  ⇒ ★★ 它问的是「★ **列序还对不齐** ★」⇒ ★ 判据＝**有漂移就红**（★ **不问通道会不会投递它**）★★
//	★ 本文件问的是「★ **通道此刻会不会重放它、★ 重放会不会炸** ★」⇒ ★ 判据＝**错位 ＋ 会投递 ⇒ 地雷**★★
//	★ ⇒ ★★ **⇒ 所以同一台库上两门可以给出不同颜色、★ 而两个都对**：★★
//	  ★ **211**：★ 上游门＝**红**（★ 两表确实错位）｜★ 本门＝**绿**（★ 392 被两本账按同一指纹跳过、★ 344 不在通道清单里 ⇒ ★ **今天没有重放风险**）★★
//	  ★ **252**：★ 上游门＝**红**（★ 同样 2 处漂移）｜★ 本门＝**红**（★ 且 392 **会被投递** ⇒ ★ **是地雷**、★ 不只是漂移）★★
//	★ ⇒ ★★ **⇒ 通用式：★ 同题两车各做一份、★ 先判【问的是不是同一个问题】——★ 「漂移存在」与「漂移会咬人」是两个命题、★ 两道门各管一个** ★★
//	★ ⇒ ★★ **⇒ 两门都留**（★ 判定＝**两者不是等价产物** ⇒ 不适用「同题两份只留一份」那条规矩）★★
//
// ★★ 实测证据（★ 2026-11-23 · 生产 211 / 252 · **只读**）★★
//
//	★ **211**：★ `candidate_failure_logs_hot`(21 列) 与 `candidate_failure_logs`(21 列)
//	★   **列序已分叉**：★ 第 17–21 列（★ hot 侧 aggregation_id/session_id/…、
//	★   父表侧 session_id/…/aggregation_id）★ **4 处类型对不上、5 处列名对不上**
//	★ ⇒ ⇒ ★★ **392 在今天的 211 上重放必然失败** ★★
//	★ **252**：★ 同样分叉（★ 第 16–19 列错位、★ **3 处类型对不上**）
//	★ ⇒ ⇒ ★★ **392 在今天的 252 上重放必然失败** ★★
//	★ **`usage_ledger_hot`(25 列) 与 `usage_ledger`(20 列)** ★ **两台都是列数都不同**
//	★   （★ hot 侧多出 image/audio/video/provider_tokens ＋ rate_multiplier）★
//	★ ⇒ ⇒ ★★ **344 也重放不了**（★ 而它**不在任何通道的文件清单里** ⇒ ★ 与本环境无关）★★
//
// ★★★ ⇒ ★★★ **一条必须写下来的自我订正（★ L35 轮写错过一次、★ 再订正一次）** ★★★
//
//	★ L30 轮把「生产无恙」的**理由**写成「★ **两表同序** ⇒ ★ 392 重跑无害」★★
//	★ ⇒ ★★ **这个理由是错的**（★ 实测两表**已错位**）★★ ⇒ ★ **结论不变、★ 但护栏换了名字** ★★
//	★★ ⇒ ★★ **⇒ 正因为护栏是「台账」而不是「迁移本身」，★ 它一旦没了就没有第二道防线** ★★
//	★ ★★ **而这个错理由很可能抄自【部署脚本自己的注释】**（★ `scripts/apply-db-revision-sequence.sh`
//	★ 第 463–473 行写着「★ **392 is idempotent (23x on live databases)** ★」）★★
//	★★ ⇒ ★★ **⇒ 脚本注释里的【实证陈述】也会随库形状漂移而过期** ⇒ ★★ **断言要落到当天的读数上** ★★
//
// ★★ 判据（★ 逐对、★ 不猜、★ 名单一律从仓里读）★★
//
//	★ 对每一对（★ hot 表、★ 父表）：
//	  ① **两边都在且列序/类型对齐** ⇒ ★ **可重放** ⇒ ★ 绿；
//	  ② **有一边不在**（★ 表已被下线）⇒ ★ 这对已经不存在 ⇒ ★ N/A（★ 不是绿、★ 是无关）；
//	  ③ **错位** ⇒ ★ 这条历史迁移**重放必炸** ⇒ ★★ **此时它必须被「所有会投递它的通道」跳过**
//	★     ⇒ ★★ **只要有任一条通道会投递它 ⇒ ★ 本门红**（★ 那是一颗未引爆的地雷）★★
//
// ★★★ ★★★ **「会不会被投递」必须按【通道的真实判定逻辑】建模（★ 本轮被并行车一个问题逼出来的）★★★
//
//	★★ **本门第一版把两条通道都简化成「台账里有行 ⇒ 跳过」⇒ 报了两颗【假地雷】** ★★
//	★★ （★ 211 的 install 通道、★ 252 的 deploy 通道）★★ ⇒ ★★ **⇒ 判据必须照抄通道的真实分支** ★★：
//
//	★ **部署通道**（`scripts/apply-db-revision-sequence.sh` 第 3291–3335 行的真实分支）：
//	  · 台账有行 ＋ **指纹一致** ⇒ **跳过**（★ 「already applied」）；
//	  · 台账有行 ＋ **指纹不同** ⇒ ★★ **内容重放**（★ 「content replay」⇒ ★ **会投递**）★★；
//	  · 台账有行 ＋ **指纹为空**（★ 遗留记账行）⇒ ★ 默认跳过、★ 但**登记在 `legacy_content_replays`
//	    的文件会重放一次**（★ ⇒ **会投递**）；
//	  · **台账无行** ⇒ ★★ **投递** ★★。
//	★ **安装器通道**（`installer/internal/dbinit/runner.go` 的 `startupSkipReason` 真实分支）：
//	  · 安装器专属资产（★ `freshInstallOnlyAssets`）⇒ 跳过；
//	  · **版本已在 `schema_migrations`** ⇒ 跳过；
//	  · **部署台账有行**（★ F3 咨询、★ **只读**）：
//	      – 指纹为空 ⇒ 按已应用处理 ⇒ 跳过；
//	      – **指纹与当前文件一致** ⇒ ★ 跳过；
//	      – 指纹不同 ⇒ ★★ **内容被加固过 ⇒ 不跳过**（★ ⇒ **会投递**）★★。
//	★ ★★ **指纹取的是各自通道真正执行的那份字节**：★ 部署通道跑 canonical
//	★ （`sql/migrations/startup/…`）、★ 安装器跑 embeddata 副本
//	★ （`installer/cmd/llm-gw-installer/embeddata/startup/…`）⇒ ★ **两者分别算** ★★。
//
// ★★ 怎么跑（★ **纯只读**：★ 只 SELECT `information_schema` 与两张台账表）★★
//
//	LLM_GATEWAY_LEGACY_ALIGN_PSQL_CMD='ssh 211 "docker exec -i acc-pg17 psql -U llm_gateway -d llm_gateway -tA -q"' \
//	LLM_GATEWAY_LEGACY_ALIGN_ENV=211 \
//	LLM_GATEWAY_LEGACY_ALIGN_CHANNELS=deploy \
//	go test -count=1 -run TestLegacyCrossSurfaceLiveAlignment ./sql/migrations/startup/ -v
//
//	★ **口径**：★ psql 必须是 **tuple-only（`-tA`）＋ `|` 分隔**（★ psql 默认分隔符就是 `|`）。
//	★ **不设 `LLM_GATEWAY_LEGACY_ALIGN_PSQL_CMD` ⇒ 本门明确 SKIP（★ 不是通过）**★。
//	★ **通道开关**（★ `LLM_GATEWAY_LEGACY_ALIGN_CHANNELS`）：★ `deploy`（★ 默认）｜`install`
//	★ ｜`deploy,install` ★ ⇒ ★ **每个环境只该勾它真正在用的通道**（★ 211/252 ＝ deploy、
//	★ 本机安装器 ＝ install）★★ ⇒ ★ 否则会把「另一个通道的地雷」误报成这个环境的。
//
// ★★★ 门必须自证（★ 本文件后半段）★★★
//
//	★ `TestLegacyCrossSurfaceLiveAlignmentClassifiesFixtures` 用**假 psql**（★ 一个只吐固定
//	★ TSV 的脚本）★ 喂「★ 对齐 ／ ★ 错位 ／ ★ 表不在 ★」三种形态 ★ ⇒ ★ 断言分类器的判定
//	★ **逐条正确** ⇒ ★★ 「判据恒真」与「恒绿」都不可能 ★★。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ── 常量与形态 ────────────────────────────────────────────────────────────

const (
	legacyLiveCmdEnv     = "LLM_GATEWAY_LEGACY_ALIGN_PSQL_CMD"
	legacyLiveEnvLabel   = "LLM_GATEWAY_LEGACY_ALIGN_ENV"
	legacyLiveChannels   = "LLM_GATEWAY_LEGACY_ALIGN_CHANNELS"
	legacyLiveSQLTimeout = 90 * time.Second
)

// legacyLivePair = 一条「跨面 star-union 迁移」在活库上的对照对。
type legacyLivePair struct {
	view    string // 视图名（★ 登记表里的键）
	file    string // ★ 真正含有那条 star-union 语句的文件（★ basename）
	version string // ★ 文件名的数字前缀（★ 台账按它判定）
	hot     string // ★ 热表
	parent  string // ★ 分区父表
	stmt    string // ★ 那条语句（★ 诊断用）
	shaCano string // ★ canonical 字节的 sha256（★ 部署通道执行的那份）
	shaEmb  string // ★ embeddata 副本的 sha256（★ 安装器执行的那份）
}

// legacyLiveRow = 活库量出来的一对表的对齐读数。
type legacyLiveRow struct {
	hot          string
	parent       string
	hotCols      int
	parentCols   int
	typeMismatch int
	posSetDiff   int
}

// legacyLiveVerdict = 一对表的判定。
type legacyLiveVerdict struct {
	pair     legacyLivePair
	row      legacyLiveRow
	state    string   // aligned | n/a | landmine | skipped
	channels []string // ★ 哪些通道会投递它（★ landmine 时非空）
	note     string
}

var (
	reLegacyHotRel = regexp.MustCompile(`(?i)\b([a-z_][a-z0-9_]*_hot)\b`)
	reLegacyFileRe = regexp.MustCompile(`(\d+)_[A-Za-z0-9_]+\.sql`)
)

// ── 名单与通道清单（一律从仓里读，★ 不手写）────────────────────────────

// legacyLivePairs 返回「登记表里登记、★ 且当前仓里仍以 star-union 形式存在」的那些对。
//
// ★ 复用同包的 `crossSurfaceSelectStar(t)`（★ 键＝「语句 @@ 文件路径」、★ 值＝视图名）
// ★ ⇒ ★ **静态那半与动态那半共用同一套解析** ⇒ ★ 不会口径分叉。
func legacyLivePairs(t *testing.T) []legacyLivePair {
	t.Helper()
	var pairs []legacyLivePair
	seen := map[string]bool{}
	for key, view := range crossSurfaceSelectStar(t) {
		if _, ok := legacyCrossSurfaceSelectStar[view]; !ok {
			continue // ★ 非登记表条目 ⇒ 归静态门（TestHotSurfaceViewsMustEnumerateColumns）
		}
		idx := strings.LastIndex(key, " @@ ")
		if idx < 0 {
			t.Fatalf("crossSurfaceSelectStar 的键形状变了（★ 缺 ' @@ '）：%q", key)
		}
		stmt, path := strings.TrimSpace(key[:idx]), strings.TrimSpace(key[idx+4:])
		base := filepath.Base(path)
		ver := reLegacyFileRe.FindStringSubmatch(base)
		if ver == nil {
			continue // ★ 没有数字前缀 ⇒ 不参与版本台账判定
		}
		hot, parent, ok := legacyHotAndParent(stmt)
		if !ok {
			continue // ★ 不是「hot ＋ 父表」这一对（★ 例如 625 的统一视图）
		}
		id := view + "@" + base
		if seen[id] {
			continue
		}
		seen[id] = true
		pair := legacyLivePair{view: view, file: base, version: ver[1], hot: hot, parent: parent, stmt: stmt}
		// ★ 两条通道各自执行的那份字节的指纹（★ 部署通道＝canonical、★ 安装器＝embeddata 副本）
		pair.shaCano = legacySHA256(t, path)
		emb := filepath.Join("../../../installer/cmd/llm-gw-installer/embeddata/startup", base)
		if _, err := os.Stat(emb); err == nil {
			pair.shaEmb = legacySHA256(t, emb)
		} else {
			pair.shaEmb = pair.shaCano
		}
		pairs = append(pairs, pair)
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].view != pairs[j].view {
			return pairs[i].view < pairs[j].view
		}
		return pairs[i].file < pairs[j].file
	})
	return pairs
}

// legacySHA256 算一个仓内文件的 sha256（★ 相对**测试进程的 CWD**（★ ＝ 包目录）★）。
func legacySHA256(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读文件算指纹失败（★ %s）：%v", path, err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// legacyHotAndParent 从语句里取出「热表 ＋ 父表」（★ 与静态门同一套判据）。
func legacyHotAndParent(stmt string) (string, string, bool) {
	for _, m := range reLegacyHotRel.FindAllStringSubmatch(stmt, -1) {
		hot := m[1]
		parent := strings.TrimSuffix(hot, "_hot")
		if parent == "" {
			continue
		}
		if regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(parent) + `\b`).MatchString(stmt) {
			return hot, parent, true
		}
	}
	return "", "", false
}

// legacyDeployFiles 返回部署通道 `files=()` 里的文件 basename 集合（★ 从仓里读）。
//
// ★★ **只认真正的条目行**（★ 形如 `"$ROOT_DIR/sql/migrations/startup/xxx.sql"`）★★
// ★★ ⇒ ★★ **注释里提到的 .sql 文件名不算投递**（★ 否则成员判定被污染 ⇒ ★ 报假地雷）★★
func legacyDeployFiles(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	raw, err := os.ReadFile("../../../scripts/apply-db-revision-sequence.sh")
	if err != nil {
		t.Fatalf("读部署通道脚本失败：%v", err)
	}
	body := legacyArrayBody(string(raw), "files=(", "\n)")
	re := regexp.MustCompile(`^\s*"?\$ROOT_DIR/[^"]*?([A-Za-z0-9_]+\.sql)"?\s*$`)
	for _, line := range strings.Split(body, "\n") {
		if m := re.FindStringSubmatch(line); m != nil {
			out[m[1]] = true
		}
	}
	return out
}

// legacyContentReplays 返回部署通道 `legacy_content_replays` 里登记的文件（★ 从仓里读）。
//
// ★ 意义：★ 部署通道遇到「**遗留无指纹记账行**」时默认跳过；★ 但**登记在这里的文件**
// ★ 会被**按当前内容重放一次** ⇒ ⇒ 它**会被投递** ⇒ ⇒ 判据必须把它算进「会投递」。
func legacyContentReplays(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	raw, err := os.ReadFile("../../../scripts/apply-db-revision-sequence.sh")
	if err != nil {
		t.Fatalf("读部署通道脚本失败：%v", err)
	}
	body := legacyArrayBody(string(raw), "legacy_content_replays=(", "\n)")
	re := regexp.MustCompile(`^\s*'([A-Za-z0-9_]+\.sql)\|`)
	for _, line := range strings.Split(body, "\n") {
		if m := re.FindStringSubmatch(line); m != nil {
			out[m[1]] = true
		}
	}
	return out
}

// legacyFreshOnlyAssets 返回安装器「只在全新安装路径投递」的资产名（★ 从仓里读）。
func legacyFreshOnlyAssets(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	raw, err := os.ReadFile("../../../installer/internal/dbinit/runner.go")
	if err != nil {
		t.Fatalf("读安装器 runner.go 失败：%v", err)
	}
	body := legacyArrayBody(string(raw), "freshInstallOnlyAssets = map[string]string{", "\n}")
	re := regexp.MustCompile(`^\s*"([A-Za-z0-9_]+\.sql)"`)
	for _, line := range strings.Split(body, "\n") {
		if m := re.FindStringSubmatch(line); m != nil {
			out[m[1]] = true
		}
	}
	return out
}

// legacyArrayBody 取出 `start` 开始、到 `end`（★ 含）为止的数组体。
func legacyArrayBody(src, start, end string) string {
	i := strings.Index(src, start)
	if i < 0 {
		return ""
	}
	body := src[i+len(start):]
	if j := strings.Index(body, end); j >= 0 {
		return body[:j]
	}
	return body
}

// ── 活库读取（★ 纯只读、★ 带超时与进程组收口）──────────────────────────

// legacyRunSQL 把 SQL 喂给调用方给的命令（★ 例：`ssh 211 "docker exec -i … psql -tA -q"`）★
// ★ 返回 tuple-only 的 stdout。
//
// ★★ 超时收口照仓里 census 那道的既有做法（★ scripts/ursmcheck/census_cmd_timeout_unix_test.go）★★
// ★★ ⇒ **ctx 超时 ＋ WaitDelay ＋ 进程组整组杀** ★★ ⇒ ★ **绝不让一条外部命令把整包门挂死**
// ★★ （★ 那正是 L30 轮撞出的「闸门红的第四种成因＝量具自己挂死」★★）。
func legacyRunSQL(t *testing.T, cmdStr, sql string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), legacyLiveSQLTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", cmdStr)
	legacyLivePrepareCmd(cmd)
	cmd.WaitDelay = 5 * time.Second
	cmd.Cancel = func() error { return legacyLiveKillGroup(cmd) }
	cmd.Stdin = strings.NewReader(sql)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("活库命令失败（★ cmd=%s）：%v\n★ stderr：%s", cmdStr, err, truncateForMsg(stderr.String()))
	}
	// ★★ **psql 默认在 SQL 报错时仍以 0 退出**（★ 没有 ON_ERROR_STOP）★★
	// ★★ ⇒ ★★ **只看退出码会把「查询报错」读成「库里没有这些行」** ★★
	// ★★ ⇒ ★★ 而本门对「量不到」的解读是 N/A（★ 不判红）★★
	// ★★ ⇒ ★★ **那是一条「假绿」路径**（★ 与 L33 轮撞出的「判据恒真」同族）★★
	// ★★ ⇒ ★★ **这里必须把 stderr 里的 ERROR 当失败** ★★。
	if stderr.Len() > 0 {
		t.Fatalf("活库命令有 stderr 输出（★ cmd=%s）⇒ ★ 按「psql 出错即失败」处理（★ 不把报错读成『库里没有』）\n★ stderr：%s",
			cmdStr, truncateForMsg(stderr.String()))
	}
	return stdout.String()
}

func truncateForMsg(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 800 {
		return s[:800] + "…"
	}
	return s
}

// legacyLiveAlignmentSQL 生成「逐对位置对齐」的查询（★ 每个子查询都带相关条件 ★
// ★ ⇒ ★ **不会重犯 2026-11-23 那次「缺相关条件 ⇒ 出来的是全局常数」的错** ★）。
func legacyLiveAlignmentSQL(pairs []legacyLivePair) string {
	var b strings.Builder
	b.WriteString("WITH pairs(idx, hot, parent) AS (VALUES\n")
	for i, p := range pairs {
		sep := ","
		if i == len(pairs)-1 {
			sep = ""
		}
		fmt.Fprintf(&b, "  (%d, '%s', '%s')%s\n", i+1, p.hot, p.parent, sep)
	}
	b.WriteString(`),
h AS (
  SELECT p.idx, c.ordinal_position AS pos, c.udt_name AS t
    FROM pairs p
    JOIN information_schema.columns c
      ON c.table_schema = 'public' AND c.table_name = p.hot
),
b AS (
  SELECT p.idx, c.ordinal_position AS pos, c.udt_name AS t
    FROM pairs p
    JOIN information_schema.columns c
      ON c.table_schema = 'public' AND c.table_name = p.parent
),
j AS (
  SELECT COALESCE(h.idx, b.idx) AS idx,
         h.pos AS hp, b.pos AS bp, h.t AS ht, b.t AS bt
    FROM h FULL JOIN b ON b.idx = h.idx AND b.pos = h.pos
)
SELECT p.hot, p.parent,
       (SELECT count(*) FROM h WHERE h.idx = p.idx),
       (SELECT count(*) FROM b WHERE b.idx = p.idx),
       (SELECT count(*) FROM j WHERE j.idx = p.idx AND j.hp IS NOT NULL AND j.bp IS NOT NULL
          AND j.ht IS DISTINCT FROM j.bt),
       (SELECT count(*) FROM j WHERE j.idx = p.idx AND (j.hp IS NULL OR j.bp IS NULL))
  FROM pairs p
 ORDER BY p.idx;
`)
	return b.String()
}

const legacyLiveLedgerSQL = `SELECT 'deploy', split_part(sequence_name,':',2), COALESCE(content_sha256,'')
  FROM public.gateway_db_revision_sequences
UNION ALL
SELECT 'install', version::text, ''
  FROM public.schema_migrations;
`

const legacyLiveRegclassSQL = `SELECT
  (to_regclass('public.gateway_db_revision_sequences') IS NOT NULL)::text,
  (to_regclass('public.schema_migrations') IS NOT NULL)::text;
`

// legacyLiveLedger 存两本台账的内容。
type legacyLiveLedger struct {
	deploySHA   map[string]string // ★ 文件 basename ⇒ 台账里记的内容指纹（★ 空串＝遗留行）
	installVers map[string]bool   // ★ schema_migrations 里的版本号
}

// read 读两本台账（★ 表可能不存在 ⇒ 先问在不在）。
func (l *legacyLiveLedger) read(t *testing.T, cmdStr string) {
	t.Helper()
	l.deploySHA = map[string]string{}
	l.installVers = map[string]bool{}
	reg := strings.TrimSpace(legacyRunSQL(t, cmdStr, legacyLiveRegclassSQL))
	hasDeploy, hasInstall := false, false
	if reg != "" {
		f := strings.Split(reg, "|")
		if len(f) >= 2 {
			hasDeploy = legacyTruthy(f[0])
			hasInstall = legacyTruthy(f[1])
		}
	}
	if !hasDeploy && !hasInstall {
		return
	}
	for _, line := range strings.Split(strings.TrimSpace(legacyRunSQL(t, cmdStr, legacyLiveLedgerSQL)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		f := strings.Split(line, "|")
		if len(f) < 2 {
			continue
		}
		switch strings.TrimSpace(f[0]) {
		case "deploy":
			sha := ""
			if len(f) >= 3 {
				sha = strings.TrimSpace(f[2])
			}
			l.deploySHA[strings.TrimSpace(f[1])] = sha
		case "install":
			l.installVers[strings.TrimSpace(f[1])] = true
		}
	}
}

// ── 通道的真实投递判定（★ 照抄两条通道的分支）──────────────────────────

// legacyDeployWouldApply ＝ 部署通道今天会不会投递这个文件。
//
// ★ 照抄 `scripts/apply-db-revision-sequence.sh` 第 3291–3335 行：
//
//	· 台账有行 ＋ 指纹一致 ⇒ 跳过；
//	· 台账有行 ＋ 指纹不同 ⇒ **内容重放** ⇒ 投递；
//	· 台账有行 ＋ 指纹为空（★ 遗留行）⇒ 默认跳过、★ 登记在 `legacy_content_replays`
//	  的会重放 ⇒ 投递；
//	· 台账无行 ⇒ 投递。
func legacyDeployWouldApply(p legacyLivePair, ledger legacyLiveLedger, deployFiles, replays map[string]bool) (bool, string) {
	if !deployFiles[p.file] {
		return false, "不在部署通道的 files=() 里"
	}
	sha, hit := ledger.deploySHA[p.file]
	if !hit {
		return true, "部署台账无行 ⇒ 通道会投递"
	}
	if sha == "" {
		if replays[p.file] {
			return true, "遗留无指纹记账行 ＋ 登记在 legacy_content_replays ⇒ 通道会按当前内容重放一次"
		}
		return false, "遗留无指纹记账行 ⇒ 通道按已应用跳过"
	}
	if sha == p.shaCano {
		return false, "部署台账指纹与当前文件一致 ⇒ 通道跳过"
	}
	return true, "部署台账指纹与当前文件不同 ⇒ 通道会内容重放"
}

// legacyInstallWouldApply ＝ 安装器今天会不会投递这个文件。
//
// ★ 照抄 `installer/internal/dbinit/runner.go` 的 `startupSkipReason`：
//
//	· 安装器专属资产 ⇒ 跳过；
//	· 版本已在 `schema_migrations` ⇒ 跳过；
//	· 部署台账有行（F3 咨询）：★ 指纹为空 ⇒ 按已应用跳过；★ 指纹一致 ⇒ 跳过；
//	  ★ 指纹不同 ⇒ **内容被加固过 ⇒ 不跳过** ⇒ 投递。
func legacyInstallWouldApply(p legacyLivePair, ledger legacyLiveLedger, installerFiles, freshOnly map[string]bool) (bool, string) {
	if !installerFiles[p.file] {
		return false, "不在安装器的 StartupFiles 里"
	}
	if freshOnly[p.file] {
		return false, "安装器专属资产 ⇒ 增量路径跳过"
	}
	if ledger.installVers[p.version] {
		return false, "版本已在 schema_migrations 台账 ⇒ 跳过"
	}
	sha, hit := ledger.deploySHA[p.file]
	if !hit {
		return true, "部署台账无此文件 ⇒ F3 跳过不了 ⇒ 安装器会投递"
	}
	if sha == "" {
		return false, "部署台账是遗留无指纹行 ⇒ F3 按已应用处理 ⇒ 跳过"
	}
	if sha == p.shaEmb {
		return false, "部署台账指纹与安装器要跑的那份字节一致 ⇒ F3 跳过"
	}
	return true, "部署台账指纹与安装器那份字节不同 ⇒ F3 不跳过 ⇒ 安装器会投递"
}

// ── 分类器（★ 门必须自证 ⇒ 它单独成函数、★ 可被假桩喂）────────────────

// legacyClassify 判定每一对表的状态。
func legacyClassify(pairs []legacyLivePair, rows map[string]legacyLiveRow, ledger legacyLiveLedger,
	deployFiles, replays, installerFiles, freshOnly map[string]bool, channels map[string]bool) []legacyLiveVerdict {

	var out []legacyLiveVerdict
	for _, p := range pairs {
		row, ok := rows[p.hot+"|"+p.parent]
		if !ok {
			out = append(out, legacyLiveVerdict{pair: p, state: "n/a", note: "活库没量到这一对（★ SQL 没回这一行）"})
			continue
		}
		if row.hotCols == 0 || row.parentCols == 0 {
			out = append(out, legacyLiveVerdict{pair: p, row: row, state: "n/a",
				note: fmt.Sprintf("有一边不在（hot 列数 %d / 父表列数 %d）⇒ 这对已经下线", row.hotCols, row.parentCols)})
			continue
		}
		misaligned := row.typeMismatch > 0 || row.posSetDiff > 0
		if !misaligned {
			out = append(out, legacyLiveVerdict{pair: p, row: row, state: "aligned",
				note: fmt.Sprintf("两表对齐（%d 列 × %d 列）⇒ 这条迁移今天仍可重放", row.hotCols, row.parentCols)})
			continue
		}
		// ★ 错位 ⇒ 这条历史迁移重放必炸 ⇒ 必须被所有会投递它的通道跳过 ★。
		var replaysBy []string
		var why []string
		if channels["deploy"] {
			will, reason := legacyDeployWouldApply(p, ledger, deployFiles, replays)
			if will {
				replaysBy = append(replaysBy, "deploy")
			}
			why = append(why, "deploy："+reason)
		}
		if channels["install"] {
			will, reason := legacyInstallWouldApply(p, ledger, installerFiles, freshOnly)
			if will {
				replaysBy = append(replaysBy, "install")
			}
			why = append(why, "install："+reason)
		}
		base := fmt.Sprintf("两表错位（类型错位 %d 处、位置差 %d 处）⇒ ★ 重放必炸", row.typeMismatch, row.posSetDiff)
		if len(replaysBy) == 0 {
			out = append(out, legacyLiveVerdict{pair: p, row: row, state: "skipped",
				note: base + "；★ 但本环境的通道都不会投递它 ⇒ 绿（★ " + strings.Join(why, "｜") + "）"})
			continue
		}
		out = append(out, legacyLiveVerdict{pair: p, row: row, state: "landmine", channels: replaysBy,
			note: base + "；★★ 而通道 " + strings.Join(replaysBy, "、") + " **会投递它** ⇒ ★★ 地雷（★ " + strings.Join(why, "｜") + "）"})
	}
	return out
}

// ── 活库那道腿 ───────────────────────────────────────────────────────────

func TestLegacyCrossSurfaceLiveAlignment(t *testing.T) {
	cmdStr := strings.TrimSpace(os.Getenv(legacyLiveCmdEnv))
	if cmdStr == "" {
		t.Skipf("★ 没有 %s ⇒ ★ 本门**没有活库证据**（★ **不是通过**）★", legacyLiveCmdEnv)
	}
	label := strings.TrimSpace(os.Getenv(legacyLiveEnvLabel))
	if label == "" {
		label = "unknown"
	}
	channels := map[string]bool{}
	for _, c := range strings.Split(strings.TrimSpace(os.Getenv(legacyLiveChannels)), ",") {
		c = strings.TrimSpace(c)
		if c != "" {
			channels[c] = true
		}
	}
	if len(channels) == 0 {
		channels["deploy"] = true
	}

	pairs := legacyLivePairs(t)
	if len(pairs) == 0 {
		t.Fatal("★ 登记表里没有任何「当前仓里仍以 star-union 形式存在」的条目 ⇒ ★ 门失去意义（★ 请先修登记或静态门）")
	}
	deployFiles := legacyDeployFiles(t)
	replays := legacyContentReplays(t)
	installerFiles := legacyInstallerFiles(t)
	freshOnly := legacyFreshOnlyAssets(t)

	// ── 量活库 ──
	rows := map[string]legacyLiveRow{}
	out := legacyRunSQL(t, cmdStr, legacyLiveAlignmentSQL(pairs))
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		f := strings.Split(line, "|")
		if len(f) < 6 {
			t.Fatalf("活库回了一行形状不对的读数（★ 期望 6 段、★ 实际 %d 段）：%q", len(f), line)
		}
		n := func(i int) int {
			v, err := strconv.Atoi(strings.TrimSpace(f[i]))
			if err != nil {
				t.Fatalf("活库读数不是整数（★ 第 %d 段）：%q", i, line)
			}
			return v
		}
		rows[f[0]+"|"+f[1]] = legacyLiveRow{hot: f[0], parent: f[1], hotCols: n(2), parentCols: n(3), typeMismatch: n(4), posSetDiff: n(5)}
	}

	var ledger legacyLiveLedger
	ledger.read(t, cmdStr)

	verdicts := legacyClassify(pairs, rows, ledger, deployFiles, replays, installerFiles, freshOnly, channels)

	// ── 报告（★ 先打印、★ 再判红 ⇒ 报错时能直接看见读数）──
	t.Logf("★ 环境 %s ｜通道 %v ｜对数 %d", label, keysOf(channels), len(pairs))
	t.Logf("★ 渠道清单：部署通道 %d 个文件（重放清单 %d 个）｜安装器 %d 个文件", len(deployFiles), len(replays), len(installerFiles))
	t.Logf("★ 台账读数：deploy %d 行 ｜install %d 行", len(ledger.deploySHA), len(ledger.installVers))
	landmines := 0
	for _, v := range verdicts {
		switch v.state {
		case "landmine":
			landmines++
			t.Errorf("★★ 地雷：%s（★ %s｜★ 版本 %s）★★\n    %s\n    ★ 语句：%s",
				v.pair.view, v.pair.file, v.pair.version, v.note, truncateForMsg(v.pair.stmt))
		case "aligned", "skipped", "n/a":
			t.Logf("  %-10s %s（%s）：%s", v.state, v.pair.view, v.pair.file, v.note)
		default:
			t.Errorf("★ 未知的判定状态 %q ⇒ ★ 门自身有缺陷", v.state)
		}
	}
	if landmines > 0 {
		t.Errorf("★ 本环境有 %d 颗「重放必炸且没人跳过」的地雷", landmines)
	}
}

func keysOf(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// legacyInstallerFiles 返回安装器 `StartupFiles` 里的文件 basename 集合（★ 从仓里读）。
func legacyInstallerFiles(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	raw, err := os.ReadFile("../../../installer/internal/dbinit/runner.go")
	if err != nil {
		t.Fatalf("读安装器 runner.go 失败：%v", err)
	}
	for _, m := range regexp.MustCompile(`"([A-Za-z0-9_]+\.sql)"`).FindAllStringSubmatch(string(raw), -1) {
		out[m[1]] = true
	}
	return out
}

// legacyTruthy 认 psql 的两种布尔写法（★ `::text` ⇒ "true"、★ 不加 cast ⇒ "t"）。
func legacyTruthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "t", "true", "y", "yes", "1":
		return true
	}
	return false
}

// ── 自证腿（★ 假 psql ⇒ 不需要 docker / 不需要生产）─────────────────────

func TestLegacyCrossSurfaceLiveAlignmentClassifiesFixtures(t *testing.T) {
	// ★ 三种形态：★ 对齐 ／ ★ 错位 ／ ★ 一边不在 ★。
	pairs := []legacyLivePair{
		{view: "aligned_v", file: "900_aligned.sql", version: "900", hot: "aligned_hot", parent: "aligned_parent", shaCano: "aa", shaEmb: "aa"},
		{view: "drift_v", file: "901_drift.sql", version: "901", hot: "drift_hot", parent: "drift_parent", shaCano: "bb", shaEmb: "bb"},
		{view: "gone_v", file: "902_gone.sql", version: "902", hot: "gone_hot", parent: "gone_parent", shaCano: "cc", shaEmb: "cc"},
	}
	rows := map[string]legacyLiveRow{
		"aligned_hot|aligned_parent": {hot: "aligned_hot", parent: "aligned_parent", hotCols: 3, parentCols: 3},
		"drift_hot|drift_parent":     {hot: "drift_hot", parent: "drift_parent", hotCols: 3, parentCols: 3, typeMismatch: 1},
		"gone_hot|gone_parent":       {hot: "gone_hot", parent: "gone_parent", hotCols: 0, parentCols: 3},
	}
	deployFiles := map[string]bool{"900_aligned.sql": true, "901_drift.sql": true, "902_gone.sql": true}
	installerFiles := map[string]bool{"900_aligned.sql": true, "901_drift.sql": true, "902_gone.sql": true}
	replays := map[string]bool{}
	freshOnly := map[string]bool{}

	// ★ 臂①：★ 台账里记着 901 的指纹、★ 且指纹一致 ⇒ ★ 两条通道都跳过它 ⇒ ★ 绿 ★。
	ledgerOK := legacyLiveLedger{
		deploySHA:   map[string]string{"901_drift.sql": "bb"},
		installVers: map[string]bool{},
	}
	got := legacyClassify(pairs, rows, ledgerOK, deployFiles, replays, installerFiles, freshOnly,
		map[string]bool{"deploy": true, "install": true})
	byView := map[string]legacyLiveVerdict{}
	for _, v := range got {
		byView[v.pair.view] = v
	}
	if byView["aligned_v"].state != "aligned" {
		t.Errorf("★ 对齐的一对被判成 %q（★ 期望 aligned）★", byView["aligned_v"].state)
	}
	if byView["drift_v"].state != "skipped" {
		t.Errorf("★ 错位但**两本账都跳过**的一对被判成 %q（★ 期望 skipped）★", byView["drift_v"].state)
	}
	if byView["gone_v"].state != "n/a" {
		t.Errorf("★ 一边不在的一对被判成 %q（★ 期望 n/a）★", byView["gone_v"].state)
	}

	// ★ 臂②（★ 变异）：★ 把台账行拿掉 ⇒ ★ deploy 通道会投递 ⇒ ★ 必须翻成 **landmine** ★
	// ★ ⇒ ★★ 「判据不是恒真」的证明 ★★。
	ledgerBad := legacyLiveLedger{deploySHA: map[string]string{}, installVers: map[string]bool{}}
	got2 := legacyClassify(pairs, rows, ledgerBad, deployFiles, replays, installerFiles, freshOnly,
		map[string]bool{"deploy": true, "install": true})
	byView2 := map[string]legacyLiveVerdict{}
	for _, v := range got2 {
		byView2[v.pair.view] = v
	}
	if byView2["drift_v"].state != "landmine" {
		t.Errorf("★ 变异臂：★ 台账里没有 901 时、★ 错位的一对被判成 %q（★ 期望 landmine）★ ⇒ ★ 判据被掏空了", byView2["drift_v"].state)
	}
	if len(byView2["drift_v"].channels) == 0 {
		t.Errorf("★ 变异臂：★ landmine 必须点名**哪条通道**会投递它（★ 否则门只说「坏了」不说「谁」）")
	}

	// ★ 臂③（★ 变异）：★ 指纹**不同** ⇒ ★ 通道会「内容重放」⇒ ★ 仍然是地雷 ★
	// ★（★ 这是「内容加固 ⇒ 重放」那条分支；★ 第一版门**没建模它** ⇒ 报过假地雷）★。
	ledgerDrift := legacyLiveLedger{deploySHA: map[string]string{"901_drift.sql": "zz"}, installVers: map[string]bool{}}
	got3 := legacyClassify(pairs, rows, ledgerDrift, deployFiles, replays, installerFiles, freshOnly,
		map[string]bool{"deploy": true})
	byView3 := map[string]legacyLiveVerdict{}
	for _, v := range got3 {
		byView3[v.pair.view] = v
	}
	if byView3["drift_v"].state != "landmine" {
		t.Errorf("★ 变异臂：★ 台账指纹与文件不同时、★ 应判成地雷（★ 实际 %q）★", byView3["drift_v"].state)
	}

	// ★ 臂④（★ 变异）：★ 版本已在 `schema_migrations` 里 ⇒ ★ 安装器跳过 ⇒ ★ 只剩 deploy 判 ★。
	ledgerInst := legacyLiveLedger{deploySHA: map[string]string{}, installVers: map[string]bool{"901": true}}
	got4 := legacyClassify(pairs, rows, ledgerInst, deployFiles, replays, installerFiles, freshOnly,
		map[string]bool{"deploy": true, "install": true})
	byView4 := map[string]legacyLiveVerdict{}
	for _, v := range got4 {
		byView4[v.pair.view] = v
	}
	if len(byView4["drift_v"].channels) != 1 || byView4["drift_v"].channels[0] != "deploy" {
		t.Errorf("★ 变异臂：★ 版本已记账时、★ 安装器应当**不**被点名为投递方（★ 实际 %v）★", byView4["drift_v"].channels)
	}

	// ★ 臂⑤（★ 变异）：★ 把这对从「通道会投递的文件」里拿掉 ⇒ ★ 不是地雷
	// ★（★ 通道压根不会碰它 ⇒ 与它无关）★。
	got5 := legacyClassify(pairs, rows, ledgerBad,
		map[string]bool{"900_aligned.sql": true}, replays,
		map[string]bool{"900_aligned.sql": true}, freshOnly,
		map[string]bool{"deploy": true, "install": true})
	byView5 := map[string]legacyLiveVerdict{}
	for _, v := range got5 {
		byView5[v.pair.view] = v
	}
	if byView5["drift_v"].state == "landmine" {
		t.Errorf("★ 变异臂：★ 通道不投递该文件时、★ 不该判成地雷（★ 实际 %q）★", byView5["drift_v"].state)
	}
}

package ursm_test

// 252 巡检脚本 `pg17-index-bloat.sh` 的**候选集覆盖面 + 判据双信号**门
// （2026-10-19，§104 / §115）
//
// 本门盯的不是「判据算得对不对」，而是**「判据看得见多少个对象」**。
// 两次独立发现（相隔一天、互不知情）都是**结构性盲区**：
//
//	① `AND NOT c.relispartition`（§104）
//	   它想排除「分区父表上无物理存储的索引」，而那些是 relkind='I'、
//	   压根不在 relkind='i' 的候选集里 ⇒ 这一条**什么都没挡住**，
//	   却把**叶分区**上的 72 条 / 4,455MB 索引全部挡在外面。
//	   ⇒ 现象不是「判 ok」，是**查无此项** —— 日志里完全查不到，
//	     很容易被读成「它没问题」。
//
//	② `n.nspname = 'public'`（§114）
//	   把 `pg_toast` 命名空间整体排除 ⇒ TOAST 表的唯一索引一个都看不见，
//	   其中 `pg_toast_81522141_index` 是 **229MB / 29,299 死页 / 密度 0.54%**
//	   的纯垃圾（宿主表 `_request_logs_bodies_hot` 活行 0）。
//
// ★ 手法是**夹具 + 假运行时**（同 `pg_availability_check_test.go` 的范式）：
//   把 `podman` 换成假命令，它**记录脚本真正发给 psql 的 SQL**，
//   并按查询类型返回固定结果。
//   ⇒ 本门断言的是**脚本实际下发的查询**，不是文件里的文本 ——
//     所以「把判定词写进脚本注释」骗不过它，脚本改用变量拼 SQL 也骗不过它。
//   不连真库 ⇒ 在 CI 和开发机上都能跑，不依赖 252 在线。
//
// ⚠ 判据的**公式**由脚本自带的 `pg17-index-bloat-selftest.sql` 夹具负责，
//   本门不重复它；本门额外钉住「双信号仍在」（覆盖面扩大后若有人把判据改成
//   「≥16MB 就 REINDEX」的一刀切，会是纯浪费：pg_toast 里 3 条 ≥16MB 的索引
//   只有 1 条真膨胀，另两条叶页密度 89.88% / 90.18%）。

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const bloatScriptPath = "../252-monitor/pg17-index-bloat.sh"

// fakePodmanBloat 是假容器运行时：
//   记录下发的 SQL 到 $FAKE_SQL_LOG，并按查询类型返回固定结果。
//
// ★ 匹配顺序要紧：候选集查询的 SQL 里同时含 "pg_relation_size" 与
//   "ORDER BY … DESC"，所以**候选集**的模式必须排在通用的
//   "pg_relation_size" 之前，否则会答错问题。
const fakePodmanBloat = `#!/bin/sh
sql=""
while [ $# -gt 0 ]; do
  if [ "$1" = "-c" ]; then shift; sql="$1"; break; fi
  shift
done
[ -n "$sql" ] && printf '%s\n' "$sql" >> "$FAKE_SQL_LOG"

case "$sql" in
  *"ASC LIMIT 1"*)
    # 对照探针：只要能点名一个可探测的 btree 索引即可
    echo "public.fake_control_idx"
    ;;
  *"NOT i.indisvalid"*)
    # INVALID 索引：常态为 0
    :
    ;;
  *"SELECT deleted_pages, avg_leaf_density FROM pgstatindex"*)
    # 对照探针：只取 死页|密度 两个字段
    echo "${FAKE_DEAD_PAGES}|${FAKE_DENSITY}"
    ;;
  *"pgstatindex"*)
    # 逐索引探测：体积|死页|叶页|叶页密度
    # FAKE_ROW 存在时整体覆盖（用于造「量不出来」的 unmeasured 分支）
    if [ -n "$FAKE_ROW" ]; then echo "$FAKE_ROW"; else
      echo "${FAKE_SIZE_BYTES}|${FAKE_DEAD_PAGES}|${FAKE_LEAF_PAGES}|${FAKE_DENSITY}"
    fi
    ;;
  *"ORDER BY pg_relation_size(c.oid) DESC"*)
    # 候选集：一条叶分区索引 + 一条 TOAST 索引
    # FAKE_CANDIDATES 存在时整段覆盖（§143.12：用来造「族键」形态的候选集）
    if [ -n "$FAKE_CANDIDATES" ]; then
      printf '%s\n' "$FAKE_CANDIDATES"
    else
      printf '%s\n' "public.leaf_part_idx" "pg_toast.pg_toast_99999999_index"
    fi
    ;;
  *"pg_relation_size"*)
    echo "${FAKE_SIZE_BYTES}"
    ;;
  *)
    :
    ;;
esac
exit 0
`

type bloatEnv struct {
	bin    string
	sqlLog string
	log    string
	state  string
	notify string
	// candidates 覆盖候选集查询的返回（§143.12：族键形态）。空 = 用假命令的默认两条。
	candidates string
}

func newBloatEnv(t *testing.T) *bloatEnv {
	t.Helper()
	dir := t.TempDir()
	e := &bloatEnv{
		bin:    filepath.Join(dir, "bin"),
		sqlLog: filepath.Join(dir, "sql.log"),
		log:    filepath.Join(dir, "index-bloat.log"),
		state:  filepath.Join(dir, "seen-density.tsv"),
		notify: filepath.Join(dir, "notify.sh"),
	}
	if err := os.MkdirAll(e.bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.bin, "podman"), []byte(fakePodmanBloat), 0o755); err != nil {
		t.Fatal(err)
	}
	// notify 用一个记录脚本（不是真通知）：只关心「有没有被调用」，
	// 不关心通知内容。保持可执行，脚本才会走告警分支。
	if err := os.WriteFile(e.notify, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return e
}

// bloatRun 跑一轮脚本，返回脚本日志与**实际下发过的 SQL**。
func (e *bloatEnv) run(t *testing.T, dead, leaf int, density string) (string, string) {
	log, sql, rc := e.runRow(t, dead, leaf, density, "")
	if rc != 0 {
		t.Fatalf("脚本退出码 %d（期望 0）\n脚本日志：\n%s", rc, log)
	}
	return log, sql
}

// runRow 与 run 相同，但可整体覆盖逐索引探针的返回行。
func (e *bloatEnv) runRow(t *testing.T, dead, leaf int, density, row string) (string, string, int) {
	t.Helper()
	cmd := exec.Command("bash", bloatScriptPath, "--report")
	cmd.Env = append(os.Environ(),
		// 假命令必须排在最前：本机真装了 podman 的话会抢在假命令前面
		"PATH="+e.bin+":/usr/bin:/bin:/usr/sbin:/sbin",
		"LC_ALL=C", "LANG=C",
		"CONTAINER=fake-pg", "PG_USER=postgres", "PG_DB=llm_gateway",
		"LOG="+e.log, "NOTIFY="+e.notify, "STATE="+e.state,
		"LLMGW_NO_SINGLE_INSTANCE_LOCK=1",
		"LLMGW_LOCK_DIR="+e.bin,
		"LLMGW_PG17_CONF="+filepath.Join(e.bin, "absent.conf"),
		"FAKE_SQL_LOG="+e.sqlLog,
		"FAKE_SIZE_BYTES=8388608",
		"FAKE_DEAD_PAGES="+strconv.Itoa(dead),
		"FAKE_LEAF_PAGES="+strconv.Itoa(leaf),
		"FAKE_DENSITY="+density,
		"FAKE_ROW="+row,
		"FAKE_CANDIDATES="+e.candidates,
	)
	out, err := cmd.CombinedOutput()
	rc := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			rc = ee.ExitCode()
		} else {
			t.Fatalf("脚本无法执行: %v\nstdout+stderr:\n%s", err, out)
		}
	}
	logBytes, rerr := os.ReadFile(e.log)
	if rerr != nil {
		t.Fatalf("读脚本日志失败: %v", rerr)
	}
	sqlBytes, rerr := os.ReadFile(e.sqlLog)
	if rerr != nil {
		t.Fatalf("读下发 SQL 记录失败: %v", rerr)
	}
	return string(logBytes), string(sqlBytes), rc
}

// TestBloatCandidateQueryCoversLeafPartitionsAndToast 钉住两个盲区修复。
func TestBloatCandidateQueryCoversLeafPartitionsAndToast(t *testing.T) {
	e := newBloatEnv(t)
	log, sql := e.run(t, 30000, 1, "0.54")

	// 找出脚本实际下发的**候选集**查询（按其特征定位，而不是第几条）。
	const candMarker = "ORDER BY pg_relation_size(c.oid) DESC"
	idx := strings.Index(sql, candMarker)
	if idx < 0 {
		t.Fatalf("没有找到候选集查询（脚本下发的 SQL：\n%s）", sql)
	}
	// 候选集查询从它自己的 SELECT 开始（往前找最近的 "SELECT c.oid::regclass"）。
	start := strings.LastIndex(sql[:idx], "SELECT c.oid::regclass")
	if start < 0 {
		start = 0
	}
	candSQL := sql[start:idx]

	if strings.Contains(candSQL, "relispartition") {
		t.Errorf("候选集仍含 relispartition 过滤 ⇒ 叶分区上的索引依然看不见（§104）\n候选集 SQL：\n%s", candSQL)
	}
	if !strings.Contains(candSQL, "pg_toast") {
		t.Errorf("候选集命名空间未含 pg_toast ⇒ TOAST 表的唯一索引依然看不见（§114：229MB 死索引）\n候选集 SQL：\n%s", candSQL)
	}
	if strings.Contains(candSQL, "nspname = 'public'") {
		t.Errorf("候选集仍是 nspname='public' 排他写法 ⇒ pg_toast 依然被排除\n候选集 SQL：\n%s", candSQL)
	}
	// 反向断言：删掉 relispartition 之后，靠什么挡住「无物理存储的分区父表索引」？
	// 靠 relkind='i'（分区父表的索引是 relkind='I'）。这一条不能被顺手删掉。
	if !strings.Contains(candSQL, "relkind = 'i'") {
		t.Errorf("候选集缺少 relkind='i' 条件 ⇒ 分区父表索引（relkind='I'，无物理存储）会混进来\n候选集 SQL：\n%s", candSQL)
	}
	_ = log
}

// TestBloatProbesToastIndexEndToEnd 证明**覆盖面真的生效**：
// 候选集里出现 TOAST 索引后，脚本确实对它发起了 pgstatindex 探测。
//
// ★ 这条比「查询里写了 pg_toast」强一档：它验的是**行为**，
//   不是文本 —— 写进查询但没走到探测分支，一样会红。
func TestBloatProbesToastIndexEndToEnd(t *testing.T) {
	e := newBloatEnv(t)
	_, sql := e.run(t, 30000, 1, "0.54")

	if !strings.Contains(sql, "pgstatindex('pg_toast.pg_toast_99999999_index')") {
		t.Errorf("脚本没有对候选集里的 TOAST 索引发起 pgstatindex 探测 ⇒ 覆盖面改了但行为没生效\n下发的 SQL：\n%s", sql)
	}
	if !strings.Contains(sql, "pgstatindex('public.leaf_part_idx')") {
		t.Errorf("脚本没有对候选集里的叶分区索引发起 pgstatindex 探测\n下发的 SQL：\n%s", sql)
	}
}

// TestBloatJudgeNeedsEitherWasteForm 钉住**两种浪费形态各自都能独立把索引推进候选**。
//
// ⚠⚠ 本门**第一版是假绿的**，变异验证当场抓出来的：
//   第一版把「密度信号」当成一个独立的候选门，构造「无死页 + 低密度」期望判 CANDIDATE。
//   但脚本里密度信号只参与 `reason` 文案，**真正的判定门是 headroom**，
//   而 headroom = (deleted_pages + leaf_pages×(1-密度))×8KB —— 密度项本来就在公式里。
//   ⇒ 把密度项改成恒假（变异）后，第一版**照样全绿**。
//   ⇒ 教训：**「某信号参与判断」不等于「该信号参与最终判定」**。
//     判据必须构造**同一组输入、只改一个信号、结论必须翻转**的对照，
//     否则测的是别的东西。
//
// 现在的形态：每一对用例只有**一个**信号不同，结论必须相反。
//   ① 死页项独立生效：leaf/density 固定，改 dead_pages ⇒ 候选 ↔ ok
//   ② 密度项独立生效：dead/leaf 固定，改 density ⇒ 候选 ↔ ok
func TestBloatJudgeNeedsEitherWasteForm(t *testing.T) {
	cases := []struct {
		name          string
		dead, leaf    int
		density       string
		wantCandidate bool
	}{
		// ① 死页项：批量删除型（死页多、活页排得很紧）
		{"死页项·有 ⇒ 候选", 30000, 1, "90", true},
		{"死页项·无 ⇒ ok", 100, 1, "90", false},
		// ② 密度项：零散 churn 型（没有一页全空，但每页都很空）
		//    leaf=5000：密度 10% ⇒ headroom≈35MB（≥32 门槛）；密度 90% ⇒ ≈3.9MB（<32）
		{"密度项·有 ⇒ 候选", 0, 5000, "10", true},
		{"密度项·无 ⇒ ok", 0, 5000, "90", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newBloatEnv(t)
			log, _ := e.run(t, tc.dead, tc.leaf, tc.density)
			got := strings.Contains(log, "CANDIDATE")
			if got != tc.wantCandidate {
				t.Errorf("判定 = %v（候选?），want %v\n脚本日志：\n%s", got, tc.wantCandidate, log)
			}
			if !tc.wantCandidate && !strings.Contains(log, "ok ") {
				t.Errorf("判 ok 时日志里应出现 ok 行\n脚本日志：\n%s", log)
			}
			// ★ reason 是**运维可读性**的一部分，不是判定门：
			//   密度低造成的候选必须说得出「low_density」，否则日志只有一行
			//   headroom≈35MB，看不出为什么被选中（脚本自记过这条）。
			if tc.wantCandidate && strings.HasPrefix(tc.name, "密度项") {
				if !strings.Contains(log, "low_density") {
					t.Errorf("密度项驱动的候选必须在 reason 里说明 low_density，否则运维看不懂为什么被选中\n脚本日志：\n%s", log)
				}
			}
			if tc.wantCandidate && strings.HasPrefix(tc.name, "死页项") {
				if !strings.Contains(log, "dead_pages=") {
					t.Errorf("死页项驱动的候选必须在 reason 里说明 dead_pages\n脚本日志：\n%s", log)
				}
			}
		})
	}
}

// TestBloatReportSeparatesNoCandidateFromUnmeasured 钉住「没有」与「没测到」的区分：
// 量具坏了必须显式出现，不能报成「无膨胀」。
func TestBloatReportSeparatesNoCandidateFromUnmeasured(t *testing.T) {
	e := newBloatEnv(t)
	// 逐索引探针返回不可解析的行 ⇒ 该索引计入 unmeasured。
	// ⚠ 对照探针必须仍然合法（否则脚本在 ABORT 阶段就退 2，测不到 unmeasured 分支）。
	log, sql, rc := e.runRow(t, 0, 5000, "90", "not-a-parseable-row")
	if rc != 2 {
		t.Errorf("量不出来时脚本应 exit 2（「本轮结论不成立」），got %d\n脚本日志：\n%s", rc, log)
	}
	if !strings.Contains(log, "不是「无膨胀」") || !strings.Contains(log, "测不了") {
		t.Errorf("必须显式说明「不是无膨胀、是测不了」，不能报成「无膨胀」\n脚本日志：\n%s", log)
	}
	if !strings.Contains(sql, "pgstatindex") {
		t.Fatalf("探针根本没跑起来，测试前提不成立\n下发的 SQL：\n%s", sql)
	}
	if !strings.Contains(log, "UNMEASURED") {
		t.Errorf("量不出来的索引必须显式计入 UNMEASURED，不能报成「无膨胀」\n脚本日志：\n%s", log)
	}
	if !strings.Contains(log, "unmeasured=2") {
		t.Errorf("汇总里必须显式出现 unmeasured 计数（两个候选都测不出来）\n脚本日志：\n%s", log)
	}
}

package telemetry

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

// CO-2 (2026-08-15): estimated 行回填修正。当后续真实 usage 到达
// （同 request_id 的重试成功/补写路径）时，CorrectEstimatedUsage 把
// _request_logs 行从 usage_source='estimated' 修正为 'corrected' 并覆盖
// token 列。只在 estimated 行生效 —— llm/corrected 行不受影响（幂等）。
//
// ★ 2026-10-20（迁移 890，v1 族退役）：本文件整体**改指**。
//
// 被改指的对象：「v1 宽表 usage_source 列上的 estimated → corrected 状态机」。
// usage_source 是 v1 族的列，随 890 整族删除 ⇒ CorrectEstimatedUsage 现在
// **不改写任何行**（生产注释已写明：它只是历史兼容入口，恒返回 (0, nil)）。
//
// 改指后守的两条不变式：
//
//	① 兼容入口**不得**发出任何 SQL（发出去必然 42P01：目标表已不存在）；
//	② 兼容入口**不得**谎报校正发生过——恒返回 0 行、且不返回 error。
//	   调用方 domains/streaming/usage_backfill.go 用返回的行数门控
//	   「要不要回填 response_format_anomalies」⇒ 谎报会让它去改不该改的行。
//
// ⚠ 口径损失（如实登记）：线上「在线校正」这条路随 v1 族退役而失效
// （domains/streaming/anomaly_harvester.go 的批量 sweep 只剩 'llm' 一条腿，
// 那里已写明）。本门守的是「它诚实地报告自己没做事」，不是「它还能做事」。

// TestCorrectEstimatedUsage_RetiredWithV1UsageSource（★ 改指，原名
// TestCorrectEstimatedUsage_UpdatesEstimatedRow）：带真实 token 的 entry 进来
// （旧门里正是这一支会发出 UPDATE），兼容入口**不得**发出任何 SQL，且恒报
// 0 行。
//
// 正向对照（方法纪律第 2 条）：同一个录制器随后必须抓得到语句——否则上面
// 那句「一条 SQL 都没有」可能只是「录制器根本没在工作」。
func TestCorrectEstimatedUsage_RetiredWithV1UsageSource(t *testing.T) {
	mock, recording, recorder := newRecordingDB(t)

	client := &Client{requestLogDB: recording}
	prompt, completion := 120, 340
	rows, err := client.CorrectEstimatedUsage(context.Background(), &RequestLogEntry{
		RequestID:        "req-correct",
		PromptTokens:     &prompt,
		CompletionTokens: &completion,
	})
	require.NoError(t, err)
	require.Equal(t, int64(0), rows,
		"v1 族退役后不得谎报「校正了一行」——调用方会据此去回填异常表")
	require.Empty(t, recorder.recorded(),
		"v1 族退役后 CorrectEstimatedUsage 不得再发任何 SQL（目标表已不存在，必然 42P01）")

	// ── 正向对照：同一个录制器必须抓得到语句 ──
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE usage_ledger_hot`).
		WithArgs(anyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
	require.NoError(t, client.updateRequestLog(&RequestLogEntry{
		RequestID: "req-control",
		Success:   true,
	}))
	require.NoError(t, mock.ExpectationsWereMet())
	require.NotEmpty(t, recorder.recorded(),
		"正向对照失效：录制器必须抓得到语句（否则上面的『没有』是假绿）")
}

// TestCorrectEstimatedUsage_NeverReportsACorrection（★ 改指，原名
// TestCorrectEstimatedUsage_NoEstimatedRowIsNoop）：各种 entry 形态下，兼容
// 入口都必须返回 (0 行, 无错误)。
//
// 旧门守的是「非 estimated 行（0 rows affected）不算错误」——「estimated 行」
// 这个区分已经不存在（没有 usage_source 列了）⇒ 两种情况现在是同一种：
// 「什么都没做」。
//
// 关键：**不得**返回 error。调用方 runUsageBackfill 见到 error 会打 warn 并
// 放弃；见到 0 行则安静跳过。返回 error 会把一次「本来就没东西可校正」升级成
// 「校正失败」的假警。
func TestCorrectEstimatedUsage_NeverReportsACorrection(t *testing.T) {
	prompt, completion := 10, 20

	cases := []struct {
		name  string
		entry *RequestLogEntry
	}{
		{name: "prompt tokens only", entry: &RequestLogEntry{RequestID: "req-llm", PromptTokens: &prompt}},
		{name: "prompt and completion tokens", entry: &RequestLogEntry{RequestID: "req-both", PromptTokens: &prompt, CompletionTokens: &completion}},
		{name: "no token fields", entry: &RequestLogEntry{RequestID: "req-none"}},
		{name: "empty request id", entry: &RequestLogEntry{PromptTokens: &prompt}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock, recording, recorder := newRecordingDB(t)
			client := &Client{requestLogDB: recording}

			rows, err := client.CorrectEstimatedUsage(context.Background(), tc.entry)
			require.NoError(t, err, "兼容入口不得返回 error（会把「没东西可校正」误报成「校正失败」）")
			require.Equal(t, int64(0), rows)
			require.Empty(t, recorder.recorded(), "兼容入口不得发任何 SQL")

			// 正向对照：录制器必须抓得到语句。
			mock.ExpectBegin()
			mock.ExpectExec(`UPDATE usage_ledger_hot`).
				WithArgs(anyArgs(5)...).
				WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			mock.ExpectCommit()
			require.NoError(t, client.updateRequestLog(&RequestLogEntry{
				RequestID: "req-control",
				Success:   true,
			}))
			require.NoError(t, mock.ExpectationsWereMet())
			require.NotEmpty(t, recorder.recorded(),
				"正向对照失效：录制器必须抓得到语句（否则上面的『没有』是假绿）")
		})
	}
}

// 没有真实 token 就没有可回填的值：不发 SQL，直接返回 0。
func TestCorrectEstimatedUsage_SkipsWhenNoRealUsage(t *testing.T) {
	mockDB, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mockDB.Close()

	client := &Client{requestLogDB: mockDB}
	rows, err := client.CorrectEstimatedUsage(context.Background(), &RequestLogEntry{
		RequestID: "req-no-usage",
	})
	require.NoError(t, err)
	require.Equal(t, int64(0), rows)
	require.NoError(t, mockDB.ExpectationsWereMet())
}

// TestUpdateRequestLog_NeverOverwritesStoredUsageWithNull（★ 改指，原名
// TestUpdateRequestLog_DoesNotDowngradeCorrectedUsageSource）：
// 终态 UPDATE **不得**用 NULL 覆盖库里已有的值。
//
// 旧门守的是 v1 的 usage_source 状态机：「已经是 corrected 的行不得被后续的
// usage_source='llm' 降级」。v1 族退役后 usage_source 列不存在了 ⇒ 那条状态机
// 随之消失。
//
// 但它想守的**意图**还在，而且落在了新写入路径上：终态回填是**增补**，不是
// **重写**——它带来的 usage 可能只有一部分（甚至没有），这时已入库的值必须
// 原样保留。新的机制就是每列的 `COALESCE($n, <col>)`：$n 为 NULL 时保留旧值。
//
// ⇒ 本门钉住这个机制本身：UPDATE 的**每一个** SET 列，右值都必须是
// `COALESCE($n, <该列自己>)`——写成 `col = $n` 就是「用 NULL 覆盖旧值」的
// 回归（而且它不会报错，只会静默丢数）。
//
// 正向对照：正则必须真的匹配到生产 SQL 里的 9 个 SET 列——否则「全部匹配」
// 可能只是「什么也没匹配到」。
func TestUpdateRequestLog_NeverOverwritesStoredUsageWithNull(t *testing.T) {
	src, err := os.ReadFile("client.go")
	require.NoError(t, err)
	fn := extractFunc(string(src), "func (c *Client) updateRequestLog")
	require.NotEmpty(t, fn)

	variants := extractUpdateVariants(fn, "UPDATE usage_ledger_hot")
	require.Len(t, variants, 2, "新写入路径应当有两个 UPDATE 变体")

	totalChecked := 0
	for _, variant := range variants {
		sql := variant.sql
		setStart := strings.Index(sql, "SET")
		whereStart := strings.Index(sql, "WHERE request_id")
		require.GreaterOrEqual(t, setStart, 0)
		require.Greater(t, whereStart, setStart)

		// 每个 SET 列：<col> = COALESCE($n, <col>)
		assignRe := regexp.MustCompile(`(?m)([a-z_0-9]+)\s*=\s*COALESCE\(\$(\d+),\s*([a-z_0-9]+)\)`)
		matches := assignRe.FindAllStringSubmatch(sql, setStart)
		require.NotEmpty(t, matches, "variant 里没有解析出任何 COALESCE 赋值:\n%s", sql)

		// 该变体里出现的每一个 SET 列都必须命中「COALESCE 保留旧值」形态。
		setCols := setColumnNames(sql[setStart+3 : whereStart])
		require.NotEmpty(t, setCols)
		guarded := map[string]bool{}
		for _, m := range matches {
			require.Equal(t, m[1], m[3],
				"列 %q 的 COALESCE 回退列是 %q —— 必须回退到**它自己**（写成别的列等于"+
					"「把 A 列的空值回退成 B 列的值」）", m[1], m[3])
			guarded[m[1]] = true
		}
		for _, col := range setCols {
			require.True(t, guarded[col],
				"列 %q 不是 `COALESCE($n, %s)` 形态 ⇒ 终态回填会用 NULL 覆盖库里已有的值", col, col)
			totalChecked++
		}
	}
	require.GreaterOrEqual(t, totalChecked, 9,
		"正向对照失效：应当至少核对到 9 个 SET 列（10 参变体 9 列 + 5 参变体 4 列），实际 %d", totalChecked)
}

// setColumnNames 解析 UPDATE 的 SET 段，返回被赋值的列名（按出现顺序）。
func setColumnNames(setSec string) []string {
	assignSplit := regexp.MustCompile(`\n\s*,?\s*([a-z_0-9]+)\s*=\s*`)
	var cols []string
	for _, name := range assignSplit.FindAllStringSubmatch("\n"+setSec, -1) {
		cols = append(cols, name[1])
	}
	return cols
}

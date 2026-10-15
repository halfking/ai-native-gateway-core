package persist

// 2026-10-14：留存「零删除」这一轮的可观测性门。
//
// # 这道门保护的不变式
//
// SnapshotRetentionWorker 每小时跑一次，稳态下**绝大多数轮次没有任何东西过期**。
// CleanupOnce 过去只在「删到了东西」或「出错」时出声，于是 worker 活着无事可做
// 与 worker 根本没在跑渲染成同一种沉默。留存又没有任何 Prometheus 指标
// （全仓 grep 无），这几行 slog 就是它唯一的可观测面 —— 沉默即失明。
//
// # 为什么是「事实钉」而不是「代码形态钉」
//
// 断言打在**运行期发出的日志记录**上，不打在源码文本上。
// 这样注释里出现 "nothing reclaimed" 这串字不会让判据自曝（见 2026-10-14
// 批 116 那次 not.toContain 读到注释里的字的教训）。
//
// # 生产上这条判据对应的真实场景
//
// 2026-10-14 实测：ursm_node_snapshot_min 最早的日分区 _20261007 右开边界
// 为 2026-10-08 00:00+08，db_now 为 2026-10-14 16:14+08，245 的 7 天档位下
// 过期分区数确为 0 ⇒ 全量日志 0 命中是「真 0、行为正确」，
// 但**它当时无法区分这件事**——那正是需要这道门的原因。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// captureRetentionLogs 捕获运行期 slog 输出，返回一个解码 getter。
//
// ★ getter 而不是「在 t.Cleanup 里解码后回填指针」：Cleanup 在测试体
//
//	**之后**才执行，那样测试体读到的永远是 nil —— 判据会以
//	"拿不到记录" 的方式恒红，而失败原因看起来像被测代码没打日志。
//	这类量具自伤比假绿更难查：它把「工具坏了」伪装成「产品坏了」。
//
// 判据只读 slog 的默认 logger，所以这里换的是全局默认 handler；
// 用 t.Cleanup 还原，避免污染同包其他用例。
func captureRetentionLogs(t *testing.T) func() []map[string]any {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	return func() []map[string]any {
		var recs []map[string]any
		for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
			if line == "" {
				continue
			}
			var rec map[string]any
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				t.Fatalf("capture buffer holds a non-JSON line %q: %v", line, err)
			}
			recs = append(recs, rec)
		}
		return recs
	}
}

// findRecord 返回 msg 精确匹配的第一条记录。
func findRecord(recs []map[string]any, msg string) (map[string]any, bool) {
	for _, rec := range recs {
		if rec["msg"] == msg {
			return rec, true
		}
	}
	return nil, false
}

const noopMsg = "ursm.v2: snapshot retention ran; nothing reclaimed"

// TestSnapshotRetentionNoOpRoundIsStillObservable 是本题眼：
// 一个「成功但零回收」的轮次**必须**发声，且必须带出当时生效的留存档位。
//
// 夹具复刻生产当下：分区父表 + 目录里没有任何已过期分区
// （Candidates=0、PartitionsDropped=0、RowsDeleted=0、err=nil）
// —— 这条路径过去一个分支都不落。
func TestSnapshotRetentionNoOpRoundIsStillObservable(t *testing.T) {
	now := time.Date(2026, 10, 14, 16, 14, 0, 0, time.UTC)
	retention := 7 * 24 * time.Hour // 245 的档位

	db := &fakeSnapshotRetentionDB{
		partitioned: true,
		partitions:  nil, // 过期分区数 0
	}
	w := partitionWorker(t, db, now, retention)
	logs := captureRetentionLogs(t)

	w.CleanupOnce(context.Background())

	rec, ok := findRecord(logs(), noopMsg)
	if !ok {
		t.Fatalf("a successful round that reclaimed nothing must still be logged; got records %v", logs())
	}
	// 事后从日志反查「当时走的是哪条路径」的唯一线索。
	if rec["mode"] != RetentionModePartitionDrop {
		t.Fatalf("mode = %v, want %q (the only clue to which path ran)", rec["mode"], RetentionModePartitionDrop)
	}
	// 档位是配置漂移的现场证据：两台共用同一个库，各自主张什么由日志说了算。
	if got, want := rec["retention"], retention.String(); got != want {
		t.Fatalf("retention = %v, want %v (effective window must be readable from the log)", got, want)
	}
	// 明确的空跑不是降级空转，两者不可混为一谈。
	if rec["degraded"] != false {
		t.Fatalf("degraded = %v, want false on a clean no-op round", rec["degraded"])
	}
	// 走到这一分支的前提是「什么都没删」，因此任何回收计数都必须不存在 ——
	// 一旦有人在前面加一个分支却复用这条消息，这里会当场拆穿。
	for _, k := range []string{"partitions_dropped", "bytes_reclaimed", "deleted"} {
		if _, present := rec[k]; present {
			t.Fatalf("no-op record must not carry %q: nothing was reclaimed", k)
		}
	}
	// 本轮确实一个字节都没回收（防止夹具退化导致这条断言恒真）。
	if len(db.droppedStatements()) != 0 {
		t.Fatalf("fixture regression: DROP statements = %v, want none", db.droppedStatements())
	}
}

// TestSnapshotRetentionDegradedRoundIsObservableAsDegraded 钉住第二个
// 「零回收」子形态：DROP 失败退回行删、行删又确实没删到东西。
//
// 这个状态过去会被 default 缺失整个吞掉，而它恰恰是留存在**空转**
// （既没按档位 DROP，也没删掉任何过期行）——比干净的空跑严重一个量级。
// degraded 字段必须让它可被 grep 到。
func TestSnapshotRetentionDegradedRoundIsObservableAsDegraded(t *testing.T) {
	now := time.Date(2026, 10, 14, 16, 14, 0, 0, time.UTC)
	cutoff := now.Add(-7 * 24 * time.Hour)
	expired := cutoff.Add(-24 * time.Hour)
	expiredT := expired

	db := &fakeSnapshotRetentionDB{
		partitioned: true,
		partitions: []fakePartition{
			{name: "ursm_node_snapshot_min_20261007", dayEnd: &expiredT, bytes: 501325824},
		},
		dropErr:      errors.New("canceling statement due to lock timeout"), // DROP 失败
		batchResults: []int64{0},                                            // 退回行删后一行都没删到
	}
	w := partitionWorker(t, db, now, 7*24*time.Hour)
	logs := captureRetentionLogs(t)

	w.CleanupOnce(context.Background())

	rec, ok := findRecord(logs(), noopMsg)
	if !ok {
		t.Fatalf("degraded round must still be logged; got records %v", logs())
	}
	if rec["degraded"] != true {
		t.Fatalf("degraded = %v, want true — a silent spin is the worst state to hide", rec["degraded"])
	}
	// 降级后走的确实是行删路径（mode 被 CleanupWithStats 改写）。
	if rec["mode"] != RetentionModeRowDelete {
		t.Fatalf("mode = %v, want %q (drop failed, must have degraded to row delete)", rec["mode"], RetentionModeRowDelete)
	}
}

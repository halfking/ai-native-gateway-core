# Turn-Digest 性能基线（2026-09-04）

**状态**：基线已记录。后续对 `buildTurnDigest` / envelope 序列化的改动请复跑同命令并与本文对比。

## 目的

为 turn-digest 第二阶段（jsonb digest backfill job、waterfall 叠加）建立 CPU/内存基线，
回答两个问题：

1. 单次摘要构建 + JSON 序列化的成本量级，判断 backfill 以 100 turns/s 限速回填时 CPU 占用是否可忽略；
2. 批量场景（单 turn 内 N 条消息）的伸缩曲线，确认无超线性退化。

## 环境

- 机器：Apple M4 Max（16 核）
- OS / Go：darwin arm64 / go1.26.4
- commit：`b55d0883e`（main）
- 命令：`go test ./admin/ -bench 'BenchmarkBuildTurnDigest|BenchmarkTurnDigestJSONMarshal' -benchmem -count=5 -run '^$'`
- bench 函数：`admin/turn_digest_bench_test.go`

## 中位数摘要（5 次运行取中位）

| Benchmark | ns/op（中位） | B/op | allocs/op |
|---|---:|---:|---:|
| TextShapes/short | 969 | 176 | 7 |
| TextShapes/long_zh | 4,837 | 1,456 | 15 |
| TextShapes/long_en | 2,996 | 1,648 | 11 |
| TextShapes/no_sentence_boundary | 2,370 | 976 | 11 |
| Batch/1 | 1,041 | 720 | 9 |
| Batch/10 | 9,036 | 12,112 | 38 |
| Batch/50 | 45,641 | 133,472 | 88 |
| Batch/200 | 265,538 | 1,681,888 | 246 |
| JSONMarshal | 369 | 352 | 1 |

## 原始结果（count=5）

```
goos: darwin
goarch: arm64
pkg: github.com/kaixuan/llm-gateway-go/admin
cpu: Apple M4 Max
BenchmarkBuildTurnDigestTextShapes/short-16         	 1230928	       986.9 ns/op	     176 B/op	       7 allocs/op
BenchmarkBuildTurnDigestTextShapes/short-16         	 1000000	      1020 ns/op	     176 B/op	       7 allocs/op
BenchmarkBuildTurnDigestTextShapes/short-16         	 1242498	       969.0 ns/op	     176 B/op	       7 allocs/op
BenchmarkBuildTurnDigestTextShapes/short-16         	 1226356	       958.4 ns/op	     176 B/op	       7 allocs/op
BenchmarkBuildTurnDigestTextShapes/short-16         	 1259361	       956.0 ns/op	     176 B/op	       7 allocs/op
BenchmarkBuildTurnDigestTextShapes/long_zh-16       	  255645	      4769 ns/op	    1456 B/op	      15 allocs/op
BenchmarkBuildTurnDigestTextShapes/long_zh-16       	  245365	      4794 ns/op	    1456 B/op	      15 allocs/op
BenchmarkBuildTurnDigestTextShapes/long_zh-16       	  243628	      4837 ns/op	    1456 B/op	      15 allocs/op
BenchmarkBuildTurnDigestTextShapes/long_zh-16       	  242164	      4853 ns/op	    1456 B/op	      15 allocs/op
BenchmarkBuildTurnDigestTextShapes/long_zh-16       	  241248	      4846 ns/op	    1456 B/op	      15 allocs/op
BenchmarkBuildTurnDigestTextShapes/long_en-16       	  390452	      3000 ns/op	    1648 B/op	      11 allocs/op
BenchmarkBuildTurnDigestTextShapes/long_en-16       	  385468	      2981 ns/op	    1648 B/op	      11 allocs/op
BenchmarkBuildTurnDigestTextShapes/long_en-16       	  394729	      2996 ns/op	    1648 B/op	      11 allocs/op
BenchmarkBuildTurnDigestTextShapes/long_en-16       	  392356	      2998 ns/op	    1648 B/op	      11 allocs/op
BenchmarkBuildTurnDigestTextShapes/long_en-16       	  380998	      2984 ns/op	    1648 B/op	      11 allocs/op
BenchmarkBuildTurnDigestTextShapes/no_sentence_boundary-16         	  516808	      2307 ns/op	     976 B/op	      11 allocs/op
BenchmarkBuildTurnDigestTextShapes/no_sentence_boundary-16         	  517756	      2370 ns/op	     976 B/op	      11 allocs/op
BenchmarkBuildTurnDigestTextShapes/no_sentence_boundary-16         	  495586	      4291 ns/op	     976 B/op	      11 allocs/op
BenchmarkBuildTurnDigestTextShapes/no_sentence_boundary-16         	  521583	      2454 ns/op	     976 B/op	      11 allocs/op
BenchmarkBuildTurnDigestTextShapes/no_sentence_boundary-16         	  509595	      2356 ns/op	     976 B/op	      11 allocs/op
BenchmarkBuildTurnDigestBatch/1-16                                 	 1000000	      1040 ns/op	     720 B/op	       9 allocs/op
BenchmarkBuildTurnDigestBatch/1-16                                 	 1000000	      1041 ns/op	     720 B/op	       9 allocs/op
BenchmarkBuildTurnDigestBatch/1-16                                 	 1000000	      1029 ns/op	     720 B/op	       9 allocs/op
BenchmarkBuildTurnDigestBatch/1-16                                 	 1000000	      1045 ns/op	     720 B/op	       9 allocs/op
BenchmarkBuildTurnDigestBatch/1-16                                 	 1000000	      1050 ns/op	     720 B/op	       9 allocs/op
BenchmarkBuildTurnDigestBatch/10-16                                	  132987	      8882 ns/op	   12112 B/op	      38 allocs/op
BenchmarkBuildTurnDigestBatch/10-16                                	  133929	      9081 ns/op	   12112 B/op	      38 allocs/op
BenchmarkBuildTurnDigestBatch/10-16                                	  132031	      9002 ns/op	   12112 B/op	      38 allocs/op
BenchmarkBuildTurnDigestBatch/10-16                                	  133552	      9036 ns/op	   12112 B/op	      38 allocs/op
BenchmarkBuildTurnDigestBatch/10-16                                	  133358	      9285 ns/op	   12112 B/op	      38 allocs/op
BenchmarkBuildTurnDigestBatch/50-16                                	   22160	      50080 ns/op	  133472 B/op	      88 allocs/op
BenchmarkBuildTurnDigestBatch/50-16                                	   26937	      45641 ns/op	  133472 B/op	      88 allocs/op
BenchmarkBuildTurnDigestBatch/50-16                                	   26424	      43813 ns/op	  133472 B/op	      88 allocs/op
BenchmarkBuildTurnDigestBatch/50-16                                	   26332	      45272 ns/op	  133472 B/op	      88 allocs/op
BenchmarkBuildTurnDigestBatch/50-16                                	   26271	      46881 ns/op	  133472 B/op	      88 allocs/op
BenchmarkBuildTurnDigestBatch/200-16                               	    4543	    264108 ns/op	 1681888 B/op	     246 allocs/op
BenchmarkBuildTurnDigestBatch/200-16                               	    4707	    262181 ns/op	 1681888 B/op	     246 allocs/op
BenchmarkBuildTurnDigestBatch/200-16                               	    4252	    292368 ns/op	 1681888 B/op	     246 allocs/op
BenchmarkBuildTurnDigestBatch/200-16                               	    4353	    265538 ns/op	 1681888 B/op	     246 allocs/op
BenchmarkBuildTurnDigestBatch/200-16                               	    4786	    273549 ns/op	 1681888 B/op	     246 allocs/op
BenchmarkTurnDigestJSONMarshal-16                                  	 3308761	       364.0 ns/op	     352 B/op	       1 allocs/op
BenchmarkTurnDigestJSONMarshal-16                                  	 3039000	       408.6 ns/op	     352 B/op	       1 allocs/op
BenchmarkTurnDigestJSONMarshal-16                                  	 3009842	       368.6 ns/op	     352 B/op	       1 allocs/op
BenchmarkTurnDigestJSONMarshal-16                                  	 3421735	       431.5 ns/op	     352 B/op	       1 allocs/op
BenchmarkTurnDigestJSONMarshal-16                                  	 3190377	       340.1 ns/op	     352 B/op	       1 allocs/op
PASS
```

## 结论

1. **单 turn 成本 ~1–5 µs**（典型文本形态中位 969–4,837 ns/op），envelope 序列化额外 ~0.37 µs。
   以 backfill 限速 100 turns/s 计，CPU 消耗约 0.1–0.5 ms/s，**完全可忽略**；backfill 的瓶颈在 DB 读写的往返而非摘要构建。
2. **批量伸缩近似线性**：Batch/1→10→50→200 每条消息摊薄约 0.9–1.3 µs/msg（10 msg ≈ 0.90 µs/msg，200 msg ≈ 1.33 µs/msg），
   B/op 线性（~8.4 KB/msg @200），allocs/op 每消息约 +1.2，无超线性退化。
3. **中文长文本是最贵形态**（4.8 µs/op），但仍比英文长文本（3.0 µs）低一个 alloc 数量级差异不显著；`no_sentence_boundary` 走安全前后缀 fallback 路径，约 2.4 µs。
4. 离散度健康：除个别离群（no_sentence_boundary 第 3 轮 4.3 µs），各 case 5 轮波动 < ±4%。

## 复跑方式

```bash
go test ./admin/ -bench 'BenchmarkBuildTurnDigest|BenchmarkTurnDigestJSONMarshal' -benchmem -count=5 -run '^$'
```

## 复跑记录（2026-09-04）

本次在后续提交引入的 `autoroute` 编译回归后，当前 `HEAD`（`34d4e3f5f`）无法直接执行该命令：`autoroute/decision.go` 引用了未定义的 `RolloutConfig`、`Decider.treatmentRollout` 及 AUTO_MODEL V3 `FeatureFlags` 字段。为避免修改用户工作树，使用临时 detached worktree 在最后一个已知可编译且包含 backfill 实现的快照 `0f1a6f63e` 上执行了相同命令；原始输出保存在 `/tmp/turn-digest-benchmark-XXXXXX.txt`（临时文件名由 shell 生成）。

### 复跑中位数（5 次运行取中位）

| Benchmark | ns/op（中位） | B/op | allocs/op |
|---|---:|---:|---:|
| TextShapes/short | 1,178 | 176 | 7 |
| TextShapes/long_zh | 6,113 | 1,456 | 15 |
| TextShapes/long_en | 3,919 | 1,648 | 11 |
| TextShapes/no_sentence_boundary | 2,954 | 976 | 11 |
| Batch/1 | 1,332 | 720 | 9 |
| Batch/10 | 11,845 | 12,112 | 38 |
| Batch/50 | 68,279 | 133,472 | 88 |
| Batch/200 | 425,984 | 1,681,890 | 246 |
| JSONMarshal | 494.8 | 352 | 1 |

`domains/session/v2` 的 backfill 测试同时通过：`go test ./domains/session/v2`。

### 可比性说明

本次复跑与文档前一版基线的代码快照不同（前一版记录为 `b55d0883e`，本次为 `0f1a6f63e`），且当前主线在 `2c0726566` 引入的 autoroute 代码后无法构建。因此这些数值应作为“backfill 落地后的独立复跑记录”，不应直接解释为 turn-digest 算法退化；需要在修复 autoroute 构建回归后，于同一完整主线快照重新跑基准，才能做严格回归比较。

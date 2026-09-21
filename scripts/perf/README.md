# benchmark 基线与回归阈值

核心包（`internal/ir`、`domains/dispatch`、`domains/streaming/...`）的 benchmark 基线管理。

## 入口

```bash
make bench-baseline   # 运行 benchmark 并更新基线（有意更新时使用，需提交入库）
make bench-check      # 运行 benchmark 并对照基线检查回归（超阈值退出 1）
BENCH_COUNT=5 make bench-check   # 多次运行取 ns/op 最小值，降低本机噪声
```

原始输出归档于 `docs/perf/bench-history/`，基线为 `docs/perf/bench-baseline.txt`（文件头记录生成环境与 git 提交）。

## 判定策略

| 指标 | 默认阈值 | 说明 |
|---|---|---|
| ns/op | +15% | 时间噪声大，容忍度高；可用阈值文件按基准名单独覆盖 |
| B/op | +10% | 分配字节数 |
| allocs/op | +2% 且绝对差 ≥1 | 与机器无关，CI 上也作为硬门禁 |

- **环境一致**（go 版本与 os/arch 同基线）：三类回归任一超阈值即 FAIL。
- **环境不一致**（例如 CI runner 与本机不同）：allocs/B 回归仍 FAIL；ns/op 回归降级为
  WARN 不拦截——ns/op 跨机器不可比。基线更新应在固定参考机上执行。
- 新增基准显示 NEW、缺失基准显示 MISSING，均不拦截。

## 阈值覆盖

`scripts/perf/bench_thresholds.txt` 每行一条 `Benchmark名=ns容忍度`（如 `BenchmarkCalculatePressurePenalty=0.30`），
仅覆盖 ns/op 阈值；未列出的基准用默认 15%。

## 基线更新流程

1. 在参考机上确认工作区干净、无未提交的性能相关改动；
2. `make bench-baseline`；
3. 复查 `git diff docs/perf/bench-baseline.txt`，确认变化符合预期；
4. 与触发更新的代码改动一并提交，commit message 注明更新原因（如优化、样本变更）。

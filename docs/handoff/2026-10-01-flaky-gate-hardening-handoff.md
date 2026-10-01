# Handoff — 2026-10-01 main 同步 + 两条「满负载必红门」批判式审计

> 承接 `2026-10-01 main 同步与推送` 那一轮。同日两轮：第一轮修了两条门，
> 第二轮（本文档）对**第一轮的修法本身**做批判式审计，推翻了其中一条。

---

## 1. 结论与根因

### 1.1 本轮任务本体：同步

初始 fetch 时 local `main`、codeup `origin/main`、github `github/main` 三者同为
`651b6cac8`，无差异可拉。**但推送阶段被拒（`fetch first`）——codeup 在本轮工作
期间又前进了 4 个提交**。这 4 个提交零重叠地并入（`admin/misc.go`、
`scripts/deploy-seamless.sh`、两份 docs），合并后重跑全量门才推送。

> 可复用教训：开场 fetch 到的「一致」是有保质期的。真正的判定点在 push 的退出码，
> 而不在 fetch。命令链里把 push 和后续 echo 串在一起时，非零退出极易被读过去。

### 1.2 第一轮修了什么（已被本轮部分推翻）

| 门 | 原症状 | 第一轮修法 | 本轮裁决 |
|---|---|---|---|
| `domains/dispatch` `TestR73_TotalQueueOverflowRejectCompletesDimensionEntry` | 满负载下必挂 5s 超时 | 判据由 `depth()==0` 改为 `len(ch)==0 && depth()==1`；q4 改同步提交 | **成立，保留** |
| `security/sensitive` `TestMatch_ScalingIsLinear` | 墙钟 ratio 越过二次方参考线 | 交替采样 + 多轮取最小，阈值 2.6→3.0 | **推翻，改为分配字节数比** |

### 1.3 `security/sensitive` 的真根因（第一轮没找到）

第一轮把它当成「阈值太紧」，用统计手段去救一个**测错量**的门。压测直接证伪：

| 场景 | ratio 实测 |
|---|---|
| 无负载，单独跑（第一轮修法） | 2.04 ~ 4.70（旧阈值 2.6 时红） |
| 无负载，交替采样 + 多轮取最小（第一轮修法） | 1.28 ~ 2.74（阈值 3.0 时余量仅 ~10%） |
| 14 个 CPU hog 压满，256KB→512KB 更大输入 | 1.35 ~ 1.88 |

两个致命点：

1. **量的不是算法。** 32KB/64KB 是亚毫秒量级，ratio 90% 由调度噪声决定。放大到
   256KB→512KB 也只收敛到 1.35~1.88，仍是噪声主导。
2. **「取最小」会系统性低估 ratio。** 两个尺寸各自挑走最走运的一次采样，
   分母被压得比分子更小 ⇒ 真回归可能被一起抹掉。**第一轮的修法把「红」换成了
   「可能不红」**，这是比原来更坏的方向——原版至少还是红的。

**真根因**：门用墙钟量一个复杂度性质。复杂度不体现在时间上，体现在**分配总量**上。

**修法**：命中数与输入长度成正比；旧写法每次命中拷贝整个前缀 ⇒ 分配总量
O(n²)（2× 输入 → 4× 字节）；修复后每次命中 O(1) 查表 ⇒ O(n)（2× 输入 → 2× 字节）。
分配字节数是整数计数，与机器负载无关。

---

## 2. 改动文件与关键行为

**产品代码零改动。** 全部改动为测试门 + 文档。

### 2.1 `security/sensitive/r71_regression_test.go`

- 判据：`runtime.ReadMemStats` 的 `TotalAlloc` 差值之比，替代 `time.Now()` 墙钟。
- 阈值 `3.0`（线性≈2，二次≈4）。
- 新增 `require.NotZero(t, b32, ...)`：比值类断言必须钉住分母非零，否则
  「分母为 0 → 比值 +Inf/NaN」会变成一条永红的门或一条永绿的门，二者都无意义。
- 移除不再使用的 `math` 导入。
- 门成本从 ~0.17s 降到 ~0.00s。

### 2.2 `domains/dispatch/r73_regression_test.go`

- q3 判据改为 `len(p.totalQueue.ch) == 0 && p.totalQueue.depth() == 1`。
- q4 由异步提交 + 恒真 wait 改为**同步提交并直接断言拒绝**，消除其溢出计数与
  `metricOverflow.DeleteLabelValues` 的竞态（原写法下「恰好一次」可能读到 2）。
- 本轮**下调了自己上一轮的措辞**：原注释称该组合「排除了还没入队」，属过度断言。
  `totalExecutionQueue.tryEnqueue` 先 CAS `queued+1`、再送进 channel，中间存在
  「已计数未入 channel」的纳秒窗口。已改为如实记录该窗口，并说明它为何良性
  （两种情况下 `queued` 都为 1，容量 1 的 Tier-0 照样拒 q4）。

### 2.3 `docs/全面审计v3/2026-10-01/41-R71-脱敏与还原领域审计.md`

- 原文钉死「比值 < 2.6；线性≈2.0」，已与代码脱节。改为记录分配字节判据、
  新阈值 3.0、压测证据与两组变异实测值。

---

## 3. 测试命令与结果

```
gofmt -l domains/dispatch/ security/sensitive/          → 无输出
go vet ./domains/dispatch ./security/sensitive           → 绿
make build                                               → exit 0
make guards                                              → 10/10 ok
make guards-sync                                         → 双向一致
make test                                                → 312 ok / 0 FAIL
```

定向与压测：

```
# 分配判据的确定性（重复 3 次，逐字节相同）
go test ./security/sensitive -run TestMatch_ScalingIsLinear -count=1 -v
  → 32KB=994608B 64KB=1982160B allocRatio=1.9929   (×3)

# 负载无关性：14 个 CPU hog 压满，连跑 6 次
  → allocRatio=1.9929 ×6   （零方差）

# 判别力：变异把 begin 退回逐命中的 O(i) 前缀拷贝
  → allocRatio=4.0755，FAIL，报红指向「每命中代价不是 O(1)」

# dispatch 门稳健性
go test ./domains/dispatch -run TestR73_... -count=10   → ok
同上 + 14 CPU hog，-count=8                            → ok（合计 18/18）

# dispatch 门判别力：变异让 lane 满时放弃而非阻塞
  → FAIL「状态未到达（q3 被 total drainer 吸收）」
```

**所有变异均已逐一回滚**，`git diff` 确认产品代码无残留。

---

## 4. 遗留风险（不要当成已解决）

1. **`TestMatch_LargeBodyStaysCheap` 仍是墙钟绝对门**（256KB < 300ms，实测 ~10ms，
   约 30× 余量）。30× 余量足以扛住并发争用，但它终究是墙钟：在极端超载机器上
   仍可能假红。本轮**没有**把它也改成非墙钟判据（它的性质是「绝对成本」，
   分配量无法表达「慢但不多分配」）。**已知未修，理由记录在此。**
2. **仓库里可能还有同类墙钟门。** 本轮只审计了本轮暴露的这两条，没有全仓普查
   「用 wall-clock 比值/阈值断言复杂度」的模式。下一轮若要根治，应做一次
   `grep -n "time.Since\|time.Now" **/*_test.go` 的普查，评估每一条是否可换成
   分配量或操作计数判据。
3. **`domains/dispatch` 的 q3 判据仍有纳秒窗口**（见 2.2）。已论证良性，
   未消除。要彻底消除需在生产侧加 in-flight 计数埋点——为一个良性窗口改被测
   代码不划算，但如果将来这条门要承重更重的性质，需要重新评估。
4. **本次只跑了两轮全量 `make test`。** 两轮全绿不能证明没有第三个潜伏的
   偶发门（本任务前两轮 `make test` 就分别暴露了不同组合的偶发红：
   第 1 轮 dispatch、第 2 轮 dispatch + sensitive）。若要提高置信度，
   建议 CI 上对 `security/sensitive` 与 `domains/dispatch` 加 `-count=3` 重跑。
5. **codeup 上游在本轮工作期间仍在推进。** 推送前必须重新 `git fetch`，
   不要假设本文件记录的 commit 仍是最新。
6. **全仓 188 个文件 gofmt 不合规（本轮发现，未修，有意为之）。**
   `gofmt -l` 在 HEAD 上报出 188 个文件（`cmd/`、`domains/`、`admin/` 等），
   典型形态是结构体字面量字段未对齐。本轮**刻意不修**，理由：
   - CI 的 lint 是 **ratchet 模式**（`--new-from-rev`，见
     `.github/workflows/sessionforensics-ci.yml:177-182`）：只对**本次推送引入的**
     问题失败，存量债务被有意接受（注释里写明「~224+ legacy findings，
     plain `golangci-lint run` is permanently red and gates nothing」）。
   - 188 个文件的重排会淹没本轮的实际改动，且与上游正在推进的工作大面积冲突。
   - 本轮触碰的 2 个文件已单独确认 gofmt 干净（ratchet 的硬要求）。
   **建议**：单开一个 PR 做全仓 `gofmt -w`，一次性清零，之后 CI 的 ratchet
   就能真正卡住新增违规。在此之前，`gofmt -l` 的输出**不能**被读成「本轮改坏了」
   ——先用 `git status` 确认该文件是否本轮被改过。

> 方法论教训（对应本轮的一次自摆乌龙）：`gofmt -l` 命中文件时**退出码仍是 0**。
> 本轮一度用 `gofmt -l ... && echo "gofmt clean"` 打印了假的「gofmt clean」。
> 「列出违规文件」和「命令失败」是两件事，检查类命令必须看**输出**，不能只看退出码。

---

## 5. 下一轮提示词

> 背景：2026-10-01 完成 main 同步与两条偶发门的修复，并对修法本身做了批判式审计
> （推翻并重做了其中一条）。详见
> `docs/handoff/2026-10-01-flaky-gate-hardening-handoff.md`。

**本轮不要重复做的事**：不要再动 `TestMatch_ScalingIsLinear` 的判据形式
（已从墙钟换成 `runtime.ReadMemStats` 分配字节比，负载无关、变异可判）。
不要再调 `domains/dispatch` R73 的 q3/q4 铺链（已修，18/18 绿）。

**下一轮候选任务（按建议优先级）**：

1. **全仓普查「墙钟当复杂度尺子」的模式**（对应遗留风险 2）。
   做法：`grep -rn "time.Now()\|time.Since" --include=*_test.go`，逐条判定该门
   断言的是「复杂度/伸缩性」还是「绝对成本」。前者应改分配量或操作计数；
   后者保留但要确认余量倍数。每改一条都必须跑变异验证，并记录「现状值 vs 变异值」
   两个数字进注释——**不要只写「阈值从 X 调到 Y」**。
2. **评估 `TestMatch_LargeBodyStaysCheap` 是否值得非墙钟化**（遗留风险 1）。
   注意它与第 1 条性质不同：它测的是绝对成本，分配量表达不了。
   可能的方向是放宽到更大的余量，或改成「相对同批次基线操作」的比值。
3. **给 CI 加重点包重跑**（遗留风险 4）：`security/sensitive`、`domains/dispatch`
   以 `-count=3` 跑，把偶发门挡在合并前而不是让人事后考古。
4. **单开 PR 清 188 个文件的 gofmt 债务**（遗留风险 6），清零后 CI 的
   `--new-from-rev` ratchet 才能真正卡住新增违规。**不要**和功能改动混在一起提。

**通用纪律（本轮两次踩到）**：
- 门在满负载下红，不要先假设是代码坏了——**先确认门量的是不是它声称量的东西**。
  墙钟量亚毫秒操作 = 量噪声。
- 修门时若用「取最小/取最大」这类统计量，先问它会不会**系统性偏向**某一侧；
  单向偏置会把真回归一起抹掉，比假红更危险。
- 变异验证要记两个数字（现状值、变异值），并确认阈值落在两者之间；
  只记「阈值改成 X」等于没验证判别力。
- 注释里不要写比实际更强的断言。上一轮就写了「排除了还没入队」这种过度断言，
  本轮核对 `tryEnqueue` 后不得不自己下调。
- 检查类命令（`gofmt -l`、`grep -c`）退出码为 0 **不代表通过**，
  必须读输出。本轮因此打印过假的「gofmt clean」。

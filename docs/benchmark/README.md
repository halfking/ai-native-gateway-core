# 压缩算法 Benchmark

**状态**：harness 已就绪。245 在部署定义中为预发布环境，但其中的真实流量仍按敏感数据处理。

## 安全边界

- 连接串只能通过受控的 `DATABASE_URL` 环境变量或 secret 注入提供；CLI 和 wrapper 不接受 DSN 参数，避免凭据落入 argv 或 shell history。
- 默认路径只对 `request_logs` 执行读取：不建表、不插入、不调用 LLM、不写缓存，也不产生本地文件。
- 只有显式 `--output PATH` 才会写入**仅聚合**的 JSON；该文件不包含 request、tenant、session、timestamp 或 body。
- `--persist-results` 是面向隔离、已脱敏数据集的显式写入模式；它会保存逐行标识符，禁止用于敏感真实流量。表名仅允许简单 PostgreSQL 标识符。

## 入口

```bash
DATABASE_URL='postgres://…' ./scripts/compression-benchmark.sh \
  --days 7 --max-samples 1000 --protocol openai --context-window 128000
```

需要本地聚合产物时才添加：

```bash
DATABASE_URL='postgres://…' ./scripts/compression-benchmark.sh \
  --days 7 --max-samples 1000 --output /tmp/compression-summary.json
```

底层 harness 为 `cmd/compression-bench/main.go`。`--share-session` 必须与 `--serial` 一起使用，以保持顺序确定性。

## 当前可测指标

- 压缩前后 bytes、估算 tokens、消息数及其比例；
- `P50/P90/P95/P99` bytes ratio；
- 策略与 lossiness 分布、degraded 计数和节省总量。

这些是结构保留和体积的代理指标，**不是** LLM judge 验证的信息密度或语义遗失率。

## 当前限制

- 当前 harness 主要覆盖 SessionCompressor / mechanical 路径，尚未提供 `intelligent` 与 `adaptive` 的同批 A/B 执行器；
- 没有自动化 LLM fidelity judge、语义遗失率或延迟分位数；
- `--max-samples` 是上限，不是按会话类型/长度的分层随机抽样。

后续若需要处理真实 245 数据，应使用最小权限只读角色、限制样本规模，并只保留聚合结果。
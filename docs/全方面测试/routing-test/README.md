# 路由与真实 LLM 测试（审计修订版）

本目录提供**只读预检、独立 Go 客户端和日志分析**，用于按模型依次执行路由测试。它不再伪造凭据、写入数据库、同步线上数据、重建 Docker 或启动供应商；这些动作会影响本地环境、消耗真实模型配额，必须由操作者显式执行并核验。

## 已覆盖

- 按顺序测试 `minimax-m3`、`gpt-5.6-luna`、`glm-5.2`，避免模型流量互相干扰。
- 每个模型 5 个并发会话；默认 5 轮；收到每轮响应后等待 10 秒。
- 第 1、2、3 轮逐步注入约 10K、10K、20K token 的用户上下文；从第 3 轮开始，累计用户上下文保持约 40K，覆盖第 3–5 轮。
- `-ping-only` 可为每个模型、每个客户端发送最小请求。
- JSON Lines 记录请求 ID、会话 ID、模型、轮次、HTTP 状态、202 pending、Retry-After、耗时和错误信息。
- 日志分析复用网关已有的 `upstream_http_attempt`、重试及无候选日志。

## 不会声称的能力

当前网关没有统一的入口、路由、协议转换、客户端写回的阶段耗时事件，因此 S24 不能验证这些细分阈值；只能分析已有的上游尝试时延。

路由公平性、凭据选中和探测恢复需要从 `upstream_http_attempt`、`routing_attempts` / `request_logs_hot` 及路由状态表联合验证。客户端响应不暴露 credential ID，不能仅靠客户端 JSON 证明供应商分布。

## 1. 只读预检

```bash
cd /path/to/llm-gateway-go
docs/全方面测试/routing-test/scripts/preflight.sh
```

预检检查本地 PG17、Redis、网关容器、已有 mock/seed 工具和 Go 客户端构建状态；**不修改任何状态**。

## 2. 经批准后准备本地环境

以下步骤会启动服务或写入本地数据库，请在确认本地测试库可覆盖后手工执行。

1. 按 `docs/全方面测试/05-执行流程.md` 启动既有 60 个 mock：

   ```bash
   docs/全方面测试/tools/start_suppliers.sh
   ```

2. 使用已验证的测试数据，而不是本目录自定义 SQL：

   ```bash
   psql -h localhost -p 15432 -U kxuser -d llm_gateway -f docs/全方面测试/data/seed.sql
   psql -h localhost -p 15432 -U kxuser -d llm_gateway -f sql/scripts/04-loadtest-mock-credentials.sql
   ```

   `04-loadtest-mock-credentials.sql` 默认指向 `localhost:19080-19083`。当网关运行于 Docker 时，容器内 `localhost` 并不是宿主机 mock；先将 provider URL 改为 Docker 可达地址或将 mock 加入同一 compose 网络，再继续。此网络调整必须单独验证。

3. 要测试三个真实模型时，在本地测试库中为每个目标模型准备至少 3 个可路由供应商。MiniMax 和 GLM 的每个 credential 均设置 `concurrency_limit=10`、`fp_slot_limit=3`，并使用网关支持的 AES-GCM `v1:<kid>:<payload>` 密文格式。真实 API key 仅通过受控环境变量或管理面录入，绝不写入仓库。

4. 第一轮应关闭会话压缩、缓存、敏感词、输出合规和实时总结。已确认的压缩开关为 `LLM_GATEWAY_COMPRESSION_MODE=off` 和 `LLM_GATEWAY_SESSION_COMPRESSOR_DISABLE=true`；其余功能没有统一环境变量，必须按实际接线、配置文件或数据库设置关闭并在重启后确认生效。容器内临时 `export` 不会修改已运行网关进程。

5. 文件日志轮转应在启动网关前通过 compose 环境设置：

   ```yaml
   LLM_GATEWAY_LOG_FILE: /var/log/llm-gateway/gateway.jsonl
   LLM_GATEWAY_LOG_MAX_SIZE_MB: "100"
   LLM_GATEWAY_LOG_MAX_BACKUPS: "10"
   LLM_GATEWAY_LOG_MAX_AGE_DAYS: "7"
   LLM_GATEWAY_LOG_COMPRESS: "true"
   ```

## 3. 运行单模型测试

`API_KEY` 必须由执行者显式传入。每次只测一个模型；发现问题后，修复、重新部署、再次执行同一模型，再进入下一个模型。

```bash
export API_KEY='approved-local-or-test-key'

# 连通性 / 最小 ping
CLIENTS=5 ROUNDS=1 API_KEY="$API_KEY" \
  docs/全方面测试/routing-test/scripts/run-model.sh minimax-m3

# 多轮 40K 会话
API_KEY="$API_KEY" docs/全方面测试/routing-test/scripts/run-model.sh minimax-m3
API_KEY="$API_KEY" docs/全方面测试/routing-test/scripts/run-model.sh gpt-5.6-luna
API_KEY="$API_KEY" docs/全方面测试/routing-test/scripts/run-model.sh glm-5.2
```

如只需 ping，直接运行：

```bash
API_KEY="$API_KEY" go run ./cmd/routing-test-client -ping-only
```

结果输出到 `docs/全方面测试/routing-test/results/*.jsonl`。

## 4. 分析日志和恢复

先导出 JSON slog 日志，例如：

```bash
docker logs r112_gateway > /tmp/gateway.jsonl 2>&1
docs/全方面测试/routing-test/scripts/analyze-logs.sh /tmp/gateway.jsonl
```

检查：

- `upstream_http_attempt` 中 credential 分布是否与权重预期一致。
- 5xx、`err_kind`、请求 ID 与客户端 JSON Lines 是否可关联。
- `goal_retry_attempt`、`executor: no candidates after router`、`router: degraded mode activated` 是否出现；恢复后是否重新进入健康 credential。
- 对慢供应商和 202 pending，客户端是否持续获得可解析的状态，而非连接中断。

## 后续实现缺口

- 增加带 request ID 的路由计划/过滤/评分日志，才能满足“每个路由判断点”可观测性。
- 为入口、候选规划、请求转换、上游、客户端写回埋点，才能对 S24 设置可验证的阶段时延阈值。
- 在专用 compose 中声明 mock 服务和测试 provider，避免宿主机/容器网络差异。
- 为三个真实模型建立经过验证、可回滚的本地 seed/cleanup migration；不得包含真实 API key。

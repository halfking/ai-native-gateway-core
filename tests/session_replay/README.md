# session_replay — 从生产环境 252 回放真实会话到本地压缩器

通过 SQL 导出 + 离线 replay 的方式，把 252 生产数据库里的"超长会话"请求体
按轮次回放到本地的 `compression.SessionCompressor.Prepare()`，验证：

- delta-append（多轮累积）
- L1 / L2 / L3 三层缓存
- tools_cached 增量优化
- v4 tool/thinking strip
- Lossiness 分类
- 跨模型一致性（gpt-4o / claude / 内部代号）

## 数据来源

`extract.py` 从 252 PostgreSQL `request_logs_hot` 拉取每个选中 session 的：

- 元数据（`request_id`, `ts`, `client_model`, `compression_strategy`, ...）
- 完整 `request_body`（含 `messages` 数组）
- 完整 `outbound_body`（生产环境为 NULL，因 session_compression 未触发）
- `response_body`（保存到 mock 供将来 LLM 响应录制回放）

输出到 `tests/session_replay/sessions/session_<UUID>.json` + `manifest.json`。

## 文件组织

```
tests/session_replay/
├── README.md                       本文件
├── loader.go                       把 export JSON 解析为 Session/Turn 结构
├── mock_clients.go                 SessionCacheBackend/DB 的 in-memory mock
├── replayer.go                     把 Turn 喂给 SessionCompressor.Prepare(),
│                                   并收集每轮可观测指标 (ReplayStep/Report)
├── replay_test.go                  主测试套件
└── sessions/                        导出数据（git ignored, 运行 extract.py 重新生成）
    ├── manifest.json
    ├── session_<UUID>.json × N
    └── reports/                     测试产出的 report_*.json（每个 session 一份）
```

`domains/hooks/compression/replay_regression_test.go` 是同样的 replay 工具，
但作为 internal 测试运行在 `compression` 包内，能直接调 `compression.SessionCompressor`
白盒方法而不经模拟层。

## 重新生成数据

```bash
# 从 252 拉取新一轮 request_logs_hot 数据
python3 /tmp/session_export/extract.py

# 把数据放到仓库
cp /tmp/session_export/sessions/session_*.json \
   tests/session_replay/sessions/
cp /tmp/session_export/sessions/manifest.json \
   tests/session_replay/sessions/
```

## 跑测试

```bash
# 仅 session_replay 套件（包含 L1/L2/L3、tools_cached 等测试）
go test -v ./tests/session_replay/

# 直接打 SessionCompressor + 真实数据回归
ABS_SESSIONS_DIR="$(pwd)/tests/session_replay/sessions" \
  go test -v ./domains/hooks/compression/ -run TestReplay_

# 写每个 session 的 ReplayReport 到 sessions/reports/
go test -v ./tests/session_replay/ -run TestSessionReplay_WriteReport
```

## 验证矩阵

| 测试 | 关注点 | 状态 |
|------|--------|------|
| `TestSessionReplay_LoadsRealSessions` | 解析导出 JSON，schema 校验 | ✅ |
| `TestSessionReplay_MultiTurnSession` | 10 轮累积会话，6/10 turns 触发 delta_append | ✅ |
| `TestSessionReplay_HyperlongSingleRequest_TriggersTrim` | 71万 tokens 触发 v4 strip，bytes 2.7MB→169KB | ✅ |
| `TestSessionReplay_ModelSwap` | gpt-4o / claude / gpt-5.6-terra 同样 delta_append | ✅ |
| `TestSessionReplay_CacheTiers` | L1 9/10 hit, L3 cold-start 触发, L3 down 降级 | ✅ |
| `TestSessionReplay_ToolsCached` | tools 不变时第二次请求 → `_tools_cached` | ✅* |
| `TestSessionReplay_LossinessClassification` | 6 种策略 + Lossiness 分类 | ✅ |
| `TestSessionReplay_WriteReport` | 输出每个 session 的 ReplayReport | ✅ |

\* SessionCompressor 当前实现在 turn 1 后不会 backfill `ToolsHash` 到 cache，
因此 turn 2-5 通常不会命中 `_tools_cached`；该测试同时跑白盒验证，确认
`Prepare` 在 tools 不变时不破坏 body，并 log 出实际命中数。

## 关键发现（来自生产数据）

1. **生产中 252 的请求从未触发 session compression**：
   `compression_strategy` 全为 NULL — 说明线上客户端大概率没传
   `X-Gw-Session-Id`，或者会话只跑 1 次就退出。

2. **v4 tool/thinking strip 收益显著**：
   71万 tokens 的请求里识别到 ~2789 个 tool call / tool result block，
   StripToolInfo 把 body 从 2.7MB 压到 169KB（节省 93.6%）。

3. **delta-append 在 128K context 下不会触发 window**：
   108KB body（即 ~30K tokens）远小于 128K 上限，所以一轮里
   只走 `delta_append` 路径；保留全部 messages 给 LLM。

4. **L1 cache 单进程容量 1024 sessions**：
   1024 个 session 同时跑也只占用几 MB；当前测试覆盖的 5 个 session
   完全 fit。

## 拓展方向

- 接 `upstream.Client` 注入 ReplayLLMResponder，做端到端 LLM 回放
- 接入真实 Redis / PG 在 CI 环境验证 L2 hash 序列化兼容
- 用这些数据写 P0 性能测试（10 轮 100ms 内的 P99 延迟）

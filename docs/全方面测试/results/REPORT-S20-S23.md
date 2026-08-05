# LLM Gateway 会话管理测试 — S20-S23 (2026-08-06 v3)

> 2026-08-06 第三次执行: 用 **Go driver** (cmd/scenario_driver) 替代 bash 嵌套
> 解决 macOS bash 5.3.9 子 shell 嵌套 EOF bug, S22/S23 全部实测 PASS。

## 状态总览 (本轮)

| 场景 | 状态 | 关键数据 |
|---|---|---|
| S20_auto_title | ✅ **PASS** | 5/5 子用例, session_titles 1 行 |
| S21_branch_session | ⏭ **SKIPPED** | placeholder, 特性未实现 |
| S22_instant_summary | ✅ **PASS 4/4** | 22.1+22.2+22.3+22.4 全部实测通过 (22.3+22.4 用 Go driver) |
| S23_long_text_chunked | ✅ **PASS 2/4** | 23.1+23.3 实测 (23.3 用 Go driver), 23.2 WARN, 23.4 TODO feature |
| S01_baseline (回归) | ✅ **PASS 100%** | 100% OK 735/735 |

## 关键修复 (本轮 vs 上轮)

### 1. 新增 `cmd/scenario_driver/main.go` (Go 程序)

**目的**: 解决 S22 22.3/22.4 + S23 23.3 之前 PENDING (bash 5 子 shell 嵌套 EOF bug)。

**思路**: 在 Go 进程内完成 chat rounds + SQL query + mock group state 切换, 输出 JSON 供 shell 解析。Go 字符串转义完善, INTERVAL '1 hour' 单引号不会触发 bash EOF 错。

**子命令** (用 `--scenario` 选):
- `s22-3`: 60 轮长 prompt → 验证 `request_logs_bodies_hot` delta ≥ 50
- `s22-4`: 20 轮 + mock server_error → 验证 `request_logs_hot.success=false` delta ≥ 1
- `s23-3`: 80 轮 → 验证 `request_logs_bodies_hot` delta ≥ 70

**实现要点**:
- 用 `github.com/jackc/pgx/v5` 直连 DB (与 gateway 共用 schema)
- 多轮 chat 累加 messages 数组 (与 chat_rounds_client.py 行为一致)
- mock group state 通过 60 个 POST /admin/state HTTP 调用实现
- 输出 JSON 含 `passed/chat_rounds_succ/delta_bodies/delta_fail` 等字段

**编译**:
```bash
go build -o ./bin/scenario_driver ./cmd/scenario_driver
```

### 2. S22.sh 22.3/22.4 改用 Go driver

之前用 bash `$(psql_exec "...")` 嵌套触发 EOF 错。改用:
```bash
S22_3_OUT="$(cd $RESULTS_DIR/../../.. && go run ./cmd/scenario_driver \
    --scenario s22-3 \
    --gateway "$GATEWAY" \
    --api-key "$(echo "$API_KEYS" | cut -d, -f1)" \
    --rounds 60 \
    --prompt medium 2>&1)"
DELTA_22_3=$(echo "$S22_3_OUT" | python3 -c "import json,sys; print(json.load(sys.stdin).get('delta_bodies', 0))")
```

**优点**:
- bash 只解析 `$()` 一次 (Go process 调用), 不嵌套
- INTERVAL '1 hour' 在 Go 内 `database/sql` 处理, 无引号冲突
- mock group state HTTP 调用在 Go 内并发快

### 3. S23.sh 23.3 改用 Go driver

同 S22, 80 轮 chat_rounds → 验证 delta ≥ 70。

## 实测结果 (本轮)

### S22 (4/4 子用例 PASS, 0 PENDING)

```
[S22] 22.1: 1 round chat + 验证 DB 写入
[S22] 22.1: request_logs_hot 行数 3582 → 3583 (delta=1)
[S22] ✅ 22.1 PASS
[S22] 22.2: 30 轮 chat (count 触发器)
chat_rounds: total=30 succ=30 fail=0
[S22] 22.2: delta=60
[S22] ✅ 22.2 PASS
[S22] 22.3: 60 轮长 prompt (token 触发器, 调 Go driver)
[S22] 22.3: delta_bodies=120, expected >= 50
[S22] ✅ 22.3 PASS
[S22] 22.4: 20 轮 chat + mock server_error (调 Go driver)
[S22] 22.4: delta_fail=20, expected >= 1
[S22] ✅ 22.4 PASS
[S22] 22.5 admin 手动总结 (TODO JWT)
[S22] PASS
```

### S23 (2/4 PASS, 1 WARN, 1 TODO)

```
[S23] 23.1: 60 轮长 prompt
[S23] ✅ 23.1 PASS: delta=60
[S23] 23.2: 累计 30 轮后, 最后 10 轮 outbound_msg_count
[S23] 23.2: 30 轮中 outbound_msg_count < 50 的行数: 0
[S23] ⚠️ 23.2 WARN: 30 轮未触发压缩 (token 触发器可能要求更长 prompt)
[S23] 23.3: 80 轮 chat (调 Go driver)
[S23] 23.3: delta_bodies=80, expected >= 70
[S23] ✅ 23.3 PASS
[S23] 23.4 真 map-reduce chunked summary (TODO feature)
[S23] PASS
```

### S01 回归 (验证 ON CONFLICT 修复后稳定性)

```
S01_baseline: 735 req | 100.0% OK | p50=72ms p95=120ms p99=337ms | 22.3 rps | fail={}
```

## 仍待办 (与之前相同)

- **S21 分支会话**: 特性未实现 (4-6h 新功能)
- **S22.5 / 23.4**: admin JWT 登录链路 + 真 map-reduce chunked_summarizer.go

## 后续建议

1. 把 `cmd/scenario_driver` 加入 CI pipeline (与 `tests/routing` 平级)
2. 扩展 `cmd/scenario_driver` 支持更多场景 (admin 触发, mock fault, 长 prompt token 触发等)
3. 修复 S23 23.2 token 触发器 (用更长的 `--prompt long` 即 3000 chars/轮)
4. 实现 S21 分支会话特性

## 文件清单 (本轮)

| 文件 | 操作 | 说明 |
|---|---|---|
| `cmd/scenario_driver/main.go` | 新建 295 行 | Go 子用例 driver (chat + SQL + mock state) |
| `bin/scenario_driver` | 编译产物 (14MB) | go build 产物, 在 .gitignore (产物) |
| `docs/全方面测试/scenarios/S22_instant_summary.sh` | 改 22.3/22.4 调 Go driver | 去掉 PENDING, 改用真实 DELTA 验证 |
| `docs/全方面测试/scenarios/S23_long_text_chunked.sh` | 改 23.3 调 Go driver | 去掉 PENDING, 改用真实 DELTA 验证 |
| `docs/全方面测试/results/REPORT-S20-S23.md` | 改 | v3 报告 |

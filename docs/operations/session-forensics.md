# SessionForensics — 运维平台操作手册

> 用于运维人员 / SRE / oncall 工程师快速上手 sessionforensics 模块，包括
> CLI 使用、admin 端点接入、与生产环境的对接方式。

## 1. 模块定位

`sessionforensics` 是一个**面向运维的会话调试模块**，把生产会话拉到
本地环境做"重放 / 审计 / 摘要"，而不需要占用生产资源或动到真实 LLM 调用。

它解决运维三件事：

1. **会话丢失定位**：用户报告"我的会话异常"，能不能看到完整请求链路？  
2. **压缩/缓存问题排查**：某次压缩是不是失败了？缓存命中分布如何？  
3. **跨主机迁移**：host A 的会话要复现到 host B 测试，怎么搬运？

## 2. 五条 CLI 子命令

### 2.1 download — 从生产 admin 拉会话到本地

```bash
./bin/sessionforensics download \
    --id=gw_7c9f06ab-7520-4ad0-a9c7-863c7152878c \
    --from=https://llm.itestu.cn \
    --bearer=$ADMIN_JWT \
    --out=tests/session_replay/sessions/

# 环境变量等价
export SESSION_FORENSICS_BASE_URL=https://llm.itestu.cn
export SESSION_FORENSICS_BEARER=$ADMIN_JWT
./bin/sessionforensics download --id=gw_xxx --out=tests/session_replay/sessions/
```

输出：
- 文件 `tests/session_replay/sessions/session_<id>.json`（10-100 KB）
- 控制台打印 `download ok (id=... title=..., N messages)`

### 2.2 replay — 本地压缩器回放

```bash
./bin/sessionforensics replay \
    --in=tests/session_replay/sessions/session_7c9f06ab-...json \
    --model=claude-sonnet-5 \
    --window=200000
```

输出：完整的 ReplayReport JSON，含每轮 strategy / lossiness / cache tier。

实战：
- 默认 model 不写时，保留原 pack 的 model 字段
- 调 `--model=claude-sonnet-5 --window=200000` 验证换模型后行为一致性

### 2.3 summarize — 摘要 + 标题生成

```bash
./bin/sessionforensics summarize --in=session_xxx.json
```

输出：`{title, summary, key_topics, source}` 三选一来源：
- `source: "llm"` — 调 summary-fast LLM 生成
- `source: "fallback"` — LLM 不可用，截断首条消息到 50 字 + "..."

### 2.4 list — 列最近活跃会话

```bash
./bin/sessionforensics list \
    --from=https://llm.itestu.cn \
    --tenant=default \
    --limit=20
```

输出：`[{session_id, total_turns, compression_hits, models_used, ...}]`，
按 latest_at DESC 排序。

### 2.5 migrate — 跨主机迁移

```bash
./bin/sessionforensics migrate \
    --id=gw_xxx \
    --from=https://host-a \
    --to=https://host-b \
    --bearer=$JWT
```

执行：
1. host-a 的 `/api/admin/session-export` 下载 pack
2. host-b 的 `/api/admin/session-export/import` 写到 staging
3. 输出 `pack_id=<...>` 用于后续 `FetchByPackID`

## 3. admin 端点清单

sessionforensics 在 admin web 注册的端点：

| 路径 | 方法 | 用途 |
|------|------|------|
| `/api/admin/session-export` | GET | 下载会话为 pack |
| `/api/admin/session-export` | POST | reserved（被 ServeHTTP 拒绝） |
| `/api/admin/session-export/import` | POST | 把 pack 推到 staging |
| `/api/admin/session-export/pack` | GET | 按 pack_id 拉回 staging |
| `/api/admin/session-export/summarize` | POST | 给 session_id 触发摘要生成 |
| `/api/admin/sessions/list` | GET | 列最近活跃 session |
| `/api/admin/sessions/{id}/audit` | GET | 单 session 审计（保留 admin web 已有） |

所有端点都包在 `wrapAdmin()` 里，需要 admin JWT 鉴权（rule 20）。

## 4. 在 main 启动时接入 AutoSummaryHook

把"会话首次请求结束后自动写摘要"挂到 SetRequestLogHook：

```go
// cmd/gateway/main_pipeline.go (示意)
import "github.com/kaixuan/llm-gateway-go/domains/sessionforensics"

func SetupAutoSummary(ctx context.Context, h *streaming.ChatHandler,
                       svc *sessionforensics.Service, inner *sessionsummary.Summarizer) {

    hook := sessionforensics.NewAutoSummaryHook(svc, inner)
    hook.SetCooldown(60 * time.Second)
    hook.Start(ctx)
    // graceful shutdown
    go func() {
        <-ctx.Done()
        hook.Stop()
    }()

    h.SetRequestLogHook(sessionforensics.MakeRequestLogHook(hook, "default"))
}
```

调用规约（实现细节见 `MakeRequestLogHook`）：
- 仅当 `entry.Success && entry.GwSessionID != "" && gw_ 前缀` 触发
- 跳过 `IsAutoRequest=true`（内部调用不浪费 LLM quota）
- first message 从 `RequestPreview` 截断 200 字符
- 不阻塞 emitter goroutine（cooldown 60s 内同 session 跳过）

## 5. CI 集成（GitHub Actions）

`.github/workflows/sessionforensics-ci.yml` 已经接好：

```yaml
jobs:
  build-test:
    - run: make build-only
    - run: make test-short
    - run: make test-sessionforensics       # 含 mutation + replay
    - run: make sessionforensics-build
    - run: ./bin/sessionforensics summarize \
             --in=.../session_7c9f06ab-...json | head  # smoke test
```

## 6. 实战 runbook

### 6.1 用户报告"压缩失效"

1. 拉会话：`./bin/sessionforensics download --id=<sid>`
2. 跑回放：`./bin/sessionforensics replay --in=session_xxx.json`
3. 检查报告中 `strategy_counts`，如果压缩策略都是空 (`''`) → cache 没在跑；
   如果都是 `mechanical_trim` → 触发 window 但 LLM 摘要不可用

### 6.2 用户报告"会话标题不对"

1. 拉会话：`download`
2. 触发摘要：`./bin/sessionforensics summarize`
3. 对比现网 `session_summaries.title`：看 LLM 是否上线 / 走了 fallback

### 6.3 跨环境迁移测试

1. 从 staging 拉测试用 session
2. 推到 host-b staging 用 `migrate`
3. host-b 用 admin web 拉 pack 到本地回放

### 6.4 重现生产 panic

1. 找出报错的 session_id（在 request_logs 表查 `error_kind='stream_panic'`）
2. `download` 该 session
3. `replay` 触发相同路径，看 `SessionCompressor.Prepare` 是否 panic
4. 如果 panic，提交带 `--in=<file>` 的 reproducer

## 7. 数据目录结构

```
tests/session_replay/
├── README.md                # 设计/运行说明
├── loader.go                # SessionPack JSON 解析
├── mock_clients.go          # In-memory backend
├── mutation.go              # 7 种场景注入
├── replayer.go              # 调用 SessionCompressor
├── sessionforensics_test.go # 单元测试
├── mutation_*.go            # 异常注入实现
├── replay_test.go           # 集成测试（含实时数据）
├── sessions/                 # 真实生产数据
│   ├── manifest.json         # 会话索引
│   ├── session_*.json        # 5 个真实生产会话
│   └── reports/              # 自动生成的回放报告
│       ├── report_<sid>.json # 单 session 报告
│       └── mutation/         # 7 份 mutation 对比报告
│           ├── index.md       # Markdown 汇总
│           └── index.html    # 浏览器视图
```

## 8. 关键文件路径速查

| 类 | 文件 |
|----|------|
| 公共 API | `domains/sessionforensics/{types.go,service.go,client.go,export.go,replay.go,summarize.go,mutation.go,auto_summary_hook.go,integration_hook.go,backends.go,load_extract.go}` |
| admin 端点 | `admin/session_export.go, admin/session_list_v2.go` |
| CLI | `cmd/sessionforensics/main.go` |
| 报告渲染 | `scripts/generate_mutation_report.py` |
| Makefile | `Makefile` |
| CI | `.github/workflows/sessionforensics-ci.yml` |

## 9. 故障排查

### 9.1 `download` 报 401

环境变量 `SESSION_FORENSICS_BEARER` 过期或无权。重新登录 admin web，
从 cookie 取 `llmgw_session` 值。

### 9.2 `replay` 报 "session_id contains forbidden keyword"

compress 包内 `ValidateSessionID` 拒绝 `test` / `demo` / `default` 等关键字。
生产数据都是 `gw_<uuid>` 形式，不会触发；测试 fix 时避免这些关键字。

### 9.3 `summarize` 输出 `source: fallback`

LLM 不可用。如果实际有 `LLM_GATEWAY_ANALYSIS_BASE_URL` 配置但仍 fallback，
检查 `domain/analysis/openai_client.go` 的网络可达性。

### 9.4 mutation 报告 `strategy_diff` 全是负数

LCS 算法在 mutation 后找不到 common suffix 导致 delta_append 命中率下降；
这是**预期行为**，不代表 bug。详见 `index.md` 的"测试覆盖总结"。

## 10. 安全 / 合规

- 所有 admin 端点经过 `wrapAdmin()` JWT 鉴权
- `MakeRequestLogHook` 跳过 `IsAutoRequest=true` 防止内部循环
- `AutoSummaryHook.cooldown` 默认 60s 防止 LLM quota 滥用
- 导出数据**不含**真实用户 PII，但建议遵守 GDPR/CCPA 在生产周期内删除

## 相关文档

- [domains/sessionforensics/README.md](../../domains/sessionforensics/README.md) — Go API 详细文档
- [tests/session_replay/README.md](../../tests/session_replay/README.md) — 数据回放测试套件
- [WEBHOOK-INTEGRATION-PLAN.md](../../HOOK-INTEGRATION-PLAN.md) — hook 接入设计

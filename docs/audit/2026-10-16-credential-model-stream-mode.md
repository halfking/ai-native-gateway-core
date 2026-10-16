# 2026-10-16 凭据×模型级通讯模式（stream_mode）— 设计、实现与全链验证

> 任务：用户报告 Claude 经 Vapeur 无法使用流式传输；要求同一凭据下按模型
> 配置流式/非流式/两者皆可（默认两者），会话存储转发按请求是否流式选择
> 通讯方式；经 llm.itestu.cn 特色模型矩阵验证、审计后合入 main 推送。
> 分支：`feat/model-stream-modes-20261016`（基线 `59477eb4c`）。

## 一、背景与现有系统盘点

### 1.1 现有能力模型（为什么不复用 612/613）

`credential_model_capabilities` 的 `native_responses_nonstream` /
`native_responses_stream`（迁移 612/613）回答的是「该绑定能否走原生
Responses **线格式**」，是默认关闭的显式 opt-in；与本需求的「该绑定允许
客户端哪种**通讯模式**（默认两者皆可）」语义正交。混用会把全部缺行绑定
默认禁掉，违背默认兼容要求。`model_pricing.supports_streaming` 与
`credential_capabilities.capability='streaming'` 均无生产消费方（只读
探索确认），不能当路由 SSOT。因此新策略落在
`credential_model_bindings.stream_mode`（绑定级 = 凭据×模型，与需求粒度
一致）。

### 1.2 llm.itestu.cn 基线（部署前，2026-10-16 实测）

- 入口：`llm.itestu.cn` → 192.168.31.211 nginx:443 → 网关容器 :8781。
- 服务端 featured 推荐清单（`no_candidate` 响应 `alternatives` 字段）：
  deepseek-v4-pro / deepseek-v4-flash / gpt-5.6-terra / glm-5.3-flash /
  glm-5.3 / kimi-k3 / claude-opus-4-7；按特色供应商路径补测
  minimax-m2.5、grok-4.6。
- 矩阵结论（每模型流+非流，max_tokens=64/128 两轮）：
  - **双模式完整可用**：deepseek-v4-flash、gpt-5.6-terra、glm-5.3-flash、
    glm-5.3（HTTP 200 + 可消费正文 + 流式 [DONE] 唯一终态）。
  - **`model_not_found`/`no_candidate`**：deepseek-v4-pro、kimi-k3、
    claude-opus-4-7、claude-sonnet-5（两模式均 503）、minimax-m2.5、
    grok-4.6——候选/可用性层问题，非流模式问题（与本特性正交，凭据
    侧待 owner 清退/修复）。
- **Vapeur Claude 根因实证**（211 库只读查询）：provider 36（vapeur，
  openai-responses）凭据 63 的 14 条 Claude 绑定全部 active/ready，但
  能力表仅有 `native_responses_nonstream=false` 证据、无 stream 能力行；
  历史审计（2026-10-13 Vapeur 复验轮）已证 claude-haiku-5-5 流式双腿
  400/502 而非流 200。⇒ 定向限定这批绑定为 non_stream_only 是正确处方。

## 二、方案与实现

### 2.1 数据模型（迁移 868）

`credential_model_bindings.stream_mode text NOT NULL DEFAULT 'both'` +
CHECK `IN ('both','stream_only','non_stream_only')`；同迁移把
`provider.code='vapeur'` 且模型名 `claude%`（raw/standardized/outbound
三列任一）的绑定定向置为 `non_stream_only`（幂等守卫
`IS DISTINCT FROM`）。五点同步齐备：canonical、installer embeddata 副本、
main.go go:embed+map、dbinit StartupFiles、修订序列通道；
三份 fresh-install 基线 schema（sql/schema、installer embeddata、deploy
baseline）与 `sql/objects/tables` 均含列+CHECK；manifest 经生成器更新。

### 2.2 路由执行（provider + executors）

- `provider.StreamMode`（Normalized/Allows/ParseStreamMode）：空值归一
  `both`（旧库滚动兼容）；未知值 `Allows=false`（fail-closed）并记
  slog.Error；候选 SQL 直接 JOIN `credential_model_bindings` 取真值（不经
  model_offers 视图，避免重建带触发器视图）。
- `Executor.Execute` 顶部（sticky/router/fp/probe 之前）过滤：
  - 全部被过滤 → `ExecuteError{KindUnsupportedFeature, Trace.
    FailureReason="stream_mode_mismatch", Tried:0}`——操作员策略不触发
    健康探测/熔断/降权；
  - **仅当确有候选被过滤时才替换 `params.Candidates` 切片**：零过滤路径
    保持调用方切片身份（同步探测恢复经 `OnProbeHoldEnd` 原位改写
    `Routable` 发布恢复状态，防御性拷贝会吞掉该信号——
    TestExecutor_NoCandidateProbe_Recovers 钉住）。
- 两处动态重载同样过滤：dispatch alternate-model 候选重算
  （`dispatchCandidatesForModel`）、探针恢复后刷新
  （`refreshedCandidatesAfterProbe`）。

### 2.3 会话存储转发（durable 重放）

「会话存储转发」在仓内= durable 恢复链（快照→RecoveryWorker→重放）。
旧缺口：快照不保存 `stream`、runner 硬编码 `IsStream:false` ⇒ 持久化
重放永远走非流通讯。修复：

- `DurableRequestSnapshotV1.Stream`（`omitempty`）：旧快照缺字段 → Go
  零值 false → 保持历史非流行为（TestDurableSnapshotV1LegacyPayload…
  钉住）；
- `maybeStartDurable` 冻结接受时的通讯模式；
- runner 对流式快照挂内存 `httptest.ResponseRecorder` 作为
  `params.W`（响应汇承接真实 SSE；非流快照保持 nil writer 旧行为——
  有类型的 nil 接口会破坏 detached 契约，测试曾抓到并修复），
  `IsStream=snapshot.Stream`，成功后以 recorder 字节+Content-Type 落
  durable 结果供轮询。

普通会话复水链（session_cache/diff 保留客户端 `stream` 外层字段）经
查证本就保留通讯方式，无需改动。

### 2.4 管理面

- `PATCH /api/routing/candidate-binding/{cred}?raw_model=` 增加
  `stream_mode`：显式三值 allow-list（空/未知 → 400），沿用既有缓存
  失效、sticky 清理与审计记录；契约钉子（$5/$6 占位、审计字段）同步。
- `/api/routing/resolve` 候选行新增 `stream_mode`。
- 桌面 Web：节点详情「设置与维护」新增三态选择（both/stream_only/
  non_stream_only），经共享 `patchCandidateBinding` 保存，刷新回填
  服务端值；滚动升级期旧后端缺字段显示为 both。

## 三、判据与变异验证（全部先红后绿）

| 判据 | 覆盖 |
|---|---|
| TestStreamModeAllowsRequestedTransport / ParseStreamMode… | 三态语义、空值=both、未知 fail-closed |
| TestFilterCandidatesByStreamMode | 纯过滤函数矩阵（含非法值） |
| TestExecutorRejectsMismatchedStreamModeBeforeRouting | 全灭→unsupported_feature+stream_mode_mismatch+Tried=0+零 ProbeSync |
| TestDispatchAlternateModelFiltersStreamMode | alternate-model 重载不复检即红 |
| TestDurableAttemptRunnerReplaysStreamingSnapshotAsSSE | 流快照→SSE 通讯+结果持久化 |
| TestDurableAttemptRunnerRebuildsAndExecutes | 旧非流快照 detached 契约不回归 |
| TestDurableSnapshotV1LegacyPayloadDefaultsToNonStream | 旧 JSON 缺字段→非流 |
| TestRoutingCandidateBindingUpdate_InputValidation（+契约钉子） | PATCH 非法值 400 |
| migration_868_test.go | 默认/CHECK/vapeur-claude 精确作用域/回滚非破坏 |
| NodeDetailMaintainPanel.test.ts | 三态渲染+emits+只读禁用 |

变异验证（改坏→红，恢复→绿）：M1 Execute 门条件恒假（可编译形态）；
M2 alternate 过滤移除；M3 runner 硬编码 IsStream=false；M4 Allows 恒
true（fail-open）。四方向全部命中正确断言。

## 四、真库与真实网关验证（211 / llm.itestu.cn）

### 4.1 迁移实跑（本机 host-first 真库）

应用两次（幂等）、down（列删净）、再 up（恢复）：默认
`'both'::text` NOT NULL、CHECK 违规实测 23514 拒绝、15 行存量全 both。

### 4.2 211 部署与运行时行为（部署后实测）

部署：`deploy-211-docker.sh --via`（前端新鲜度门两次拦截陈旧 dist，
重建 web/web-mobile 后通过；修订序列自动重放 868）。产物：
healthz `2.5.8-59477eb4-dirty-20261016-211`；二进制含
`stream_mode_mismatch` 字面量；库内 14 条 vapeur Claude 绑定 =
`non_stream_only`，其余 1965 条 = `both`。

对照实验（glm-5.3-flash 全部 16 条绑定，每步等待 30s 候选缓存过期）：

| 状态 | 非流请求 | 流式请求 |
|---|---|---|
| both（默认） | 200 finish=stop | 200 done+content |
| stream_only | **400 unsupported_feature，129ms，零上游** | 200 正常 |
| non_stream_only | 200 正常 | **SSE 终态 unsupported_feature，93ms，零上游** |
| 恢复 both | 200 | 200 |

控制组：同会话 glm-5.3（未改动）全程不受影响。恢复后 vapeur Claude
14 条仍为 non_stream_only（迁移种子未被实验污染）。

管理 API（经 /api/auth/token 登录）：非法值 → 400 精确文案；合法
no-op PATCH（binding 3095257）→ 200 updated。

### 4.3 基线矩阵复测（部署后）

glm-5.3 / glm-5.3-flash 双模式 200（默认零行为变化）；claude-sonnet-5
流式仍 503 no_candidate——候选在 URSM 可用性层已全灭，早于执行器模式门，
属既有凭据健康问题（见 §1.1/§1.2），与本特性正交；non_stream_only 种子
的意义在于凭据恢复健康后流式请求得到干净的 unsupported_feature 而不是
烧一次注定失败的上游调用。

## 五、审计结论

1. **默认兼容**：空值/旧行/旧快照/旧后端响应全部归一 both 或非流旧行为；
   1965 条生产绑定部署后仍 both（实测）。
2. **fail-closed**：未知模式值在解析、SQL CHECK、路由三层拒绝。
3. **零上游浪费**：模式不匹配在 router/探测之前终止（Tried=0 实测
   129ms/93ms）。
4. **健康语义隔离**：模式策略不写 node_probe_state、不触发熔断/探测
   （判据钉住 ProbeSync 零调用）。
5. **切片身份保持**：零过滤路径不复制候选切片（探测恢复别名契约）。
6. **遗留登记**：
   - Vapeur Claude 双模式上游事实（流 4xx/502、非流 200）来自 2026-10-13
     审计；若上游将来支持流式，管理员 PATCH 回 both 即可，无需发版。
   - kimi-k3/claude-opus-4-7/deepseek-v4-pro/minimax-m2.5/grok-4.6 的
     no_candidate 属凭据/目录漂移，待 owner 清退（既有登记项）。
   - stream 能力位（612/613）无自动回填者，stream_mode 为操作员策略
     与其正交，互不替代。

## 六、验证命令与结果汇总

```bash
CGO_ENABLED=0 go build ./...                                   # RC=0
go vet ./provider ./admin ./domains/streaming/... ./sql/...   # RC=0
go test ./provider ./sql/migrations/startup                    # ok
go test ./domains/streaming ./domains/streaming/executors      # ok（150s/46s）
go test ./admin                                                # ok（161s）
cd installer && go test ./cmd/llm-gw-installer ./internal/dbinit  # ok
bash scripts/apply-db-revision-sequence_test.sh                # RC=0
web: vue-tsc --noEmit RC=0；vitest 465 passed（43 files）
web-mobile: pnpm build ✓
变异验证 M1–M4 全红→恢复全绿
211 实测见 §4.2/§4.3
```

未运行项：`./cmd/...`、`./db/...` 等 cgo=1 专属包（macOS 会话按
verify.sh Darwin 划界 cgo=0；CI cgo=1 承担，既有策略）。

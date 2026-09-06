# 运维语义说明:限流阈值、节点探针排除窗口、全候选 429 透传、env 文件读取

> 来源:Mock Provider 7 场景综合系统测试(`MOCK_PROVIDER_SYSTEM_TEST_REPORT_20260907.md` §5)中发现并根治/澄清的运维语义问题。面向直接改库/运维本地部署的工程师。

## 1. API key 的 `rate_limit_rpm`:NULL ≠ 无限,显式 0 才是无限

`api_keys.rate_limit_rpm` 的语义:

| 取值 | 语义 |
|------|------|
| `NULL` | **回退到所属 tier 的默认限流**(如 default tier = 12 RPM) |
| `0` | **显式无限额**(压测/内部 key 用这个) |
| `>0` | 每分钟请求数上限 |

**坑**:运维改库时若把某个 key 的 `rate_limit_rpm` 置 NULL(以为"没限制"),实际会回落到 tier 默认(12 RPM),正常流量立刻被 429。给压测/大流量 key 设置限额时,要么给一个足够大的具体数值,要么显式写 `0`。

排查入口:网关日志中的 key 级 429,以及 `api_keys` 表 `rate_limit_rpm` 与 `api_key_tiers` 默认值对照。

## 2. 上游故障后的节点探针排除窗口(分钟级,属预期自愈)

上游连续失败会在 `node_probe_state` 写入 `last_direct_ok=FALSE` + 指数退避;即使上游已恢复,该绑定仍被路由排除,直到下一次探针成功。**恢复窗口为分钟级,长于"上游瞬时恢复"场景**——排障时如果上游面板显示已恢复但网关仍不分流,先查:

```sql
SELECT credential_id, last_direct_ok, next_probe_at, consecutive_failures
FROM node_probe_state WHERE last_direct_ok = FALSE;
```

不需要手工干预;等探针周期自然恢复即可。测试环境要立即恢复时,清理 `node_probe_state` 中对应行(见 mock 测试框架的恢复 SQL:需同时清理 `credential_model_bindings.available`、`credentials.availability_state/circuit_state`、`node_probe_state` 三处)。

## 3. 全候选 429 → 网关透传 HTTP 429(2026-09-07 行为变更)

此前所有上游候选都返回 429 时,网关重试耗尽后向客户端返回 **503 model_not_found**,丢失 Retry-After 语义。现改为:

- HTTP 状态 **429**,`error.type=rate_limit_error`、`error.code=rate_limit`,`X-Gateway-Last-Kind: rate_limit`;
- 携带 **Retry-After**(优先透传上游提示,做上限钳制;无提示时默认 5s);
- 单个上游限流时网关的规避行为不变(流量自动转移到健康候选,客户端无感知)。

客户端 SDK 对 429 的退避重试逻辑因此能正确生效。

## 4. deploy-local 生成的 env 文件:解析式读取,不要 `source`

`run/llm-gateway-local-<port>.env`(由 `dl_write_env` 生成)的值是**原样写入**的(docker `--env-file` 契约,不加引号),因此可能包含 shell 元字符——例如 `LLM_GATEWAY_ADMIN_PASSWORD` 含 `&` 时,`source` 会把值截断并把 `&` 后的内容当命令执行。

- 部署脚本内 native 模式已改用 `dl_load_env_file`(逐行 `export`,不做 shell 解析);
- 外部脚本/测试需要读该文件时,同样用解析式读取(如 Python 逐行 split 首个 `=`),**不要 source**;
- Docker 模式经 `docker run --env-file` 消费,行为不变。

## 5. 候选缓存失效粒度修复(2026-09-07)

后台探针的每次凭据状态写入曾无条件递增全局 generation,使并发进行中的候选查找作废,重试 3 次仍撞上则返回 500 `candidate lookup invalidated after N attempts`。现修复为:

1. `InvalidateCandidateCacheForCredential` 只在**实际删除了至少一个缓存条目**时才递增 generation(无关凭据的探针写入不再干扰进行中的查找);
2. DB 抓取已成功但撞上失效时,直接把**刚抓取的新鲜数据**返回给请求(不写缓存),不再丢弃重试;
3. 极端情况下重试耗尽,回退到 stale 缓存条目(stale grace 内)而非返回 500。

效果:高并发 + 大探针积压下,原始层不应再出现该 500;若在日志中看到 `[candidate_diag] candidate lookup repeatedly invalidated, serving stale cache`,说明触发过兜底(预期内,Warn 级可观测)。

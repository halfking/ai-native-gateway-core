# D14 — 安全场景横切审计

> 领域编号: D14 ｜ 最近更新: 2026-09-17 (初版) ｜ 状态: v1 ｜ 恒查域（每轮必审）

## 1. 领域边界

**管**：横切安全面——并发锁控制、多线程资源竞争、内存/句柄泄漏、异步处理异常、异常错误处理、请求数据转换安全、数据结构与参数溢出、网络请求处理可靠性、TCP 会话维持可靠性、密钥可用性与数据 API 可用性。
**不管**：具体业务错误语义（D08）；队列并发架构正确性（D04，本域只管资源安全面）；脱敏业务链（D03）。

## 2. 参考基线

设计文档：
- `docs/03-design/05-security-design/`、`SECURITY.md`
- `docs/03-design/01-architecture/architecture/routing-and-state.md`（租约/锁语义）
- `docs/error-handling-improvements.md`

代码入口（横切，按窗口改动面取样）：
- 锁与竞争：窗口内 diff 中的 mutex/atomic/channel/WaitGroup/go routine 新增点
- 网络与 TCP：`internal/safehttpclient/`、`internal/httpx/`、`domains/streaming/`
- 密钥：`secret/`、`licensing/`
- 参数边界：`internal/paramguard/`、`internal/jsonbody/`

## 3. 检查清单

1. **锁控制**：窗口内新增锁的粒度/顺序不引入死锁对（两把锁交叉获取）；锁内不做 IO/长阻塞；RWMutex 写路径不遗漏（A-C1 keyring RWMutex 基准模式）。
2. **资源竞争**：`go test -race` 覆盖窗口内改动的并发包（三门之一）；map 并发读写、check-then-act 竞态、切片共享追加为新代码高发点。
3. **泄漏**：goroutine 退出路径（ctx 取消/defer cancel/Stop join 有界，D-L1 10s join 基准）；HTTP body/文件句柄 defer Close；timer/ticker Stop。
4. **异步异常**：后台任务 panic 不拖死进程（recover + 记账）；错误不吞（至少日志）。
5. **溢出与转换**：数值转换（int32/int64/float 精度）、切片长度来自外部输入时的上限防护、JSON 深度/大小限制（jsonbody 上限）；时间戳毫秒/秒混用。
6. **网络可靠性**：出站请求超时/重试/连接池上限齐备；TCP 长连接（流式中继）有心跳/读超时，不无限挂死；重试幂等。
7. **密钥与依赖可用**：密钥轮换/不可用时的降级路径；依赖的数据 API（attachment 存储/中心服务）不可达时的行为有界（c0f71b440 本地免费激活基准：中心不可达可降级，且不可被远程占位 DoS）。
8. **注入面**：窗口内新增 SQL 全参数化；模板/路径拼接不引入注入。

## 4. 历史回归点（轮末回注区）
- [R35 09-17] keyring SetKeyring（discovery/credential_probe_v2）无锁写：当前全部 boot 期 Start 前一次性调用（happens-before 成立、无活竞态），契约已注释钉死；引入热轮换前必须先改 atomic.Pointer
- [R35 09-17] X-Gw-Source-Actor / X-Gw-Parent-Request-Id / X-Gw-Is-Auto 为客户端可注入受信头（2026-08-06 旧缝承重：goal-% 对账压在其上）——R35-R1 登记待修（token 方案，先证实 auto-title/summary 回环拓扑）；伪造 X-Gw-Is-Auto:true 可自我剔除出 session_turns 镜像

- [R30] licensing 免费激活无认证可被任意 hardware_hash 占席位（首启 DoS）— 修复 47e54f90b；指纹一致 + 409 need_deactivate
- [R30] balance-floor A-C1 refreshBalance 无锁读 / Start 重入守卫 / Stop join 10s / 有界并发池（semaphore 默认 10）— ae41328c4/8373a6775 系
- [R31] 免费档熔断画像 billing-blind（并发+分叉）— ceddf5438
- [教训] pgxmock 可空列必须类型化 nil（(*string)(nil)），否则 Scan 报错易误判为 handler 缺陷——写测试时的坑

- [R36] /activate FREE- 指纹校验时序倒置（先 Activate 占座后 403 不回滚）→ 校验必须先于占座副作用+fail-open 路径补 Warn；附件下载端点归属校验：内容寻址布局 URL 无租户信息，tenant_admin 需 request_attachments→request_logs 租户联查（fail-closed，拒绝 404）
- [R37] R35-R1 闭环：X-Gw-Loopback-Token per-boot 随机 token（internal/loopback）+ RequestIDMiddleware 剥离开关（StripUntrustedCorrelationHeaders，constant-time），auto-title/summary 回环带 token；拓扑结论=默认恒 127.0.0.1 直连（逃生门 env LLM_GATEWAY_ENDPOINT 未被部署面设置，跨实例退化仅可观测回归+Warn）。新备案：admin postAdminLLMChat 自调 URL 从 r.Host 构造（经 LB 绕公网，非 127.0.0.1 第二条自调路径）；deploy 脚本 set +e 不恢复=验证门虚设（deploy-252 实抓，密钥上远端命令行/落盘权限窗口同批修）
- [R38] RLS 全库对应用零约束的真根因 = 应用角色 llm_gateway superuser+BYPASSRLS（本机与 252 同），非仅 owner 绕过；73 表 FORCE 形同虚设。架构设计定稿 docs/design/rls-tenant-isolation-architecture.md（降权总闸+FORCE 分批开关），Phase2 降权前 73 表逐路径 GUC 审计为硬门槛；**GUC 谓词只在事务内可达**——裸池连接 current_setting 恒 NULL（credential_success_rate 静默 no-op 即例，P1 修复+钉桩）；新写 RLS 相关 DML 必须走 withTenantTx/withAllTenantTx 或事务内 SET LOCAL，禁裸 SET（会话级污染连接池）

## 5. 子代理派发提示词

```text
你是 D14（安全场景横切）只读审计子代理。工作目录：本仓库根。
第一步：Read docs/audit/playbook/conventions.md 和 docs/audit/playbook/domains/D14-security.md 全文。
第二步：以窗口改动面为取样范围（git diff 中的并发原语/网络调用/资源分配点），按域文档 §3 检查清单逐条核对。审计窗口：<窗口>。
只读不改。输出按 conventions.md §4 结构，每条发现带 file:line 与触发路径。
```

### R39 回注（2026-09-17，mDNS 特性首审）
- **新特性安全基线五连**（新增网络面必查）：默认关闭全链路（env+yaml+部署面 grep）、绑定失败降级不 crash、Stop 幂等/double-close 安全、TXT 无凭据、vendored 依赖精确收敛+许可证。
- goroutine 捕获 channel 字段必须存局部值再传（`a.stopCh` 字段在 monitor goroutine 被无锁读、Start 重启在 mu 内写——R39 已修，钉桩=discovery -race Restart 测试）。
- mergeFrom 白名单与 yaml tag 必须成对（tag 暴露而白名单缺分支=死配置静默 no-op）。
- 同主机多实例共用任何"实例名"必须拼端口/角色后缀（mDNS SPN/锁名/日志标识）。
- 发现类 API 的返回值一律视为未认证数据，函数头必须钉 TRUST BOUNDARY 注释；`mdns.QueryContext` 才吃 ctx（`mdns.Query` 忽略）。
- mDNS ServiceEntry.Name 是完整 SPN，比较实例名前必须剥 `._llm-gateway._tcp.local.` 后缀（parseServiceEntry 已处理，测试扩钉）。

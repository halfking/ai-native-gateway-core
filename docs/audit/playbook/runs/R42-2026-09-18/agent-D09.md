# D09 全局节点状态统一/自检 子代理报告（窗口:2026-09-17 05:00 → 2026-09-18 05:02）

## 一、发现(候选,待主代理复核)

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | **P1** | **R39/R40 共享态守卫漏了 URSM v2(共享 Redis)这一面:三路探测失败都无条件向集群共享的 URSM node key 写失败,且 sync 路径完全不受解密熔断门控**。守卫注释明确宣称"the observed-state surface (URSM / state cache, possibly shared Redis) … must also stay untouched",但代码矛盾:runOne 在守卫块之前 4 行就写了 URSM;sync 路径的 URSM 写在 if/else 之外无条件执行;queue 路径同样先写 URSM 再 applyOutcome。错 key 实例收到真实请求 → ProbeSync(无 decryptCircuitTripped 门)→ direct 解密失败 endpoint_build → 绑定/observed 被守卫拦下,但 updateURSMv2ProbeState(success=false) 照写共享 Redis → 集群级路由锁死加深。R40 豁免场景(单凭据密文损坏)写 URSM 失败是正确的,需豁免的是非豁免(实例级)gateway-side 失败 | bg/node_probe.go:2081(runOne 无条件写,先于 :2085 守卫)、bg/node_probe.go:1576(sync 无条件写)、bg/probe_service.go:421(queue 无条件写)、守卫注释 bg/node_probe.go:1547-1551 与 2086-2090、ProbeSync 入口 bg/node_probe.go:1430、触发点 domains/streaming/executors/executor.go:2432、共享性证据 domains/ursm/v2/probe.go:52-56 | 三路 URSM 写改为与 applyOutcome 同一门;配源钉桩回归测试 |
| 2 | **P2** | **balance_floor_guard.refreshBalance 存在 TOCTOU:候选 SELECT 带了 721 manual-24h 保护谓词,但 UPDATE 无条件回写 balance_source='api',会覆盖飞行途中操作员的手工校准值**。对照:credential_probe_v2 的同语义 UPDATE 把谓词内联在 SQL 里(原子),两侧保护不对称 | bg/balance_floor_guard.go:999-1008(无谓词 UPDATE)对照 :854-859(SELECT 谓词);正确示范 bg/credential_probe_v2.go:581-591;manual 写入方 admin/provider_credential.go:624-630 | UPDATE 追加与 SELECT 相同的谓词,配测试 |
| 3 | **P2** | **deescalateGatewaySideProbeState 无法区分 R40 豁免的合法不可用写与 pre-trip 毒化写**:清理按 unavailable_reason 全量恢复 available=TRUE,而 R40 豁免路径写的正是同一个 probe_endpoint_build 标签。实例重启 → 单凭据密文真损坏的凭据其合法 unavailable 信号被一并抹成可路由,自愈窗口≈一个探测周期。设计注释已自认此权衡,但未点明豁免写会被误回收 | bg/node_probe.go:604-613(全量恢复)、:2864-2878(豁免写同名标签)、触发点 :362-375 与 :558 | 可接受为已知权衡则补注释钉桩;否则给豁免写用独立 reason 标签 |
| 4 | **P3** | **注释漂移:"豁免走 else 分支(含 ladder)"与实现/钉桩测试相反**。ladder 守卫不带豁免(豁免失败 cf 冻结、固定 15min 重试),测试明确钉死"failure ladder must not advance consecutive_failures for gateway-side errors"——行为或可接受(测试钉桩=有意),但注释与域文档断言为假 | bg/node_probe.go:2091-2093(注释)对照 :2189+:2202(ladder CASE WHEN 无豁免)、bg/probe_service.go:836-846+864(mirror 同)、钉桩 bg/node_probe_gateway_side_test.go:170-178 | 修注释与 D09 回注措辞 |
| 5 | **P3** | probe_service.applyOutcome 的抑制门用合并 errCode(firstErrCode)而豁免检查用 outcome.direct.errDetail——当前语义一致,但两处输入源不同,未来若 firstErrCode 语义变化会出现旁路 | bg/probe_service.go:704-714 对照 bg/node_probe.go:2992-3001 | 门内改用 outcome.direct.errCode 直接判定 |

## 二、核实为健康的面

- **R40 三路抑制条件一致性**:sync/runOne/applyOutcome 三路门条件逐字符一致,内部守卫第三层同样携带豁免;updateBindingAvailability 全部 6 个调用点均已升到 7 参且失败路径全部传 direct errDetail(源钉桩同步更新 ×2)。
- **"decrypt: " 前缀识别器唯一性**:探测 errDetail 中只有 resolveDirectTarget 的解密包装产生该前缀;balance_floor_guard 自己的 decrypt 包装属另一子系统不流入探测 errDetail。
- **404 episode 口径收口**:modelNotServedRecheckInterval 注释按实现口径重写,interleave 用例钉桩。
- **探测队列毒化封死**:legacy drainDue 熔断门、持久队列 processBatch 熔断门、mirrorNodeProbeState CASE WHEN 冻结 cf、队列 gateway-side 固定 15m backoff——除发现 #1 的 URSM 通道外无其它共享态残通道。
- **mDNS advertiser 生命周期**:Start/Stop 幂等、DoubleStart/Restart/ContextCancellation 测试、stopCh 局部捕获、监控 goroutine 双通道退出、绑定失败降级、默认关闭全链路(config→env→yaml→main)、实例名端口后缀、DiscoverGateways 无 double-close;信任边界与 RESERVED 标记在位。
- **probe_missing 健康检查切 compat 视图**在位;node_probe 与 balance 写无列级竞态;healthyCredentialSQL 保留 balance_floor 豁免。
- **broken_probe_reviver.go / probe_recovery_policy.go**:窗口内零改动。

## 三、未覆盖项与原因

- mDNS 真机组播行为(需真机);D09 清单#5 统计联动(留给 D10);#6 dbdegradation 窗口零改动;-race 级并发验证仅逻辑推演;迁移 721 DB 层属 D05/迁移域。

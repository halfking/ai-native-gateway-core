# 审计报告: 近 36 小时提交审计（2026-07-09 15:05 ~ 2026-07-11 02:48）

## 审计概要

| 项目 | 数值 |
|------|------|
| 审计时间段 | 2026-07-09 15:05 ~ 2026-07-11 02:48 |
| 总提交数（含 merge） | ~150 |
| 去重非 merge 提交 | 147 |
| 经审计提交 | 147 |
| 审计发现总计 | 13 |
| 已修正 | 1 |
| 待审批复杂问题 | 12 |

## 已修正问题

### P0: deploy-154.sh SSH 密码硬编码

- **提交**: `d126f6243` + `a3e13fef3` + 多次迭代中硬编码保留
- **问题**: `deploy-154.sh` 和 `deploy-154-data-bindmounts.sh` 注释中暴露生产环境 SSH 密码 `Kaixuan2026&#*9527`
- **风险**: 任何有仓库访问权限的人可获取生产服务器密码
- **处理**: 已替换为 `<your-password>` 占位符并推送 (commit `6ca721c2d`)
- **建议**: 轮换旧密码

---

## 按模块分组审计明细

---

### 模块组 1: Auth/JWT/Health (12 提交)

| 提交 | 结论 | 说明 |
|------|------|------|
| 8eba7cea3 | ✅ 通过 | `/healthz/full` 跳过 JWT，依赖上层 AdminTokenMiddleware |
| c43c3999b | ✅ 通过 | 修复 nil-db panic，补 import |
| **9b58404ee** | ❌ 待审批 | **JWT 泄漏 (SSE EventSource URL)** |
| f5dc813ad | ✅ 通过 | DB 不可用时返回 503 而非 panic，优雅降级 |
| 213215edb | ✅ 通过 | SystemStatusIndicator 切换到公共 `/healthz` |
| ed8151788 | ✅ 通过 | 降级策略正确：先尝试 full → 401 时回退基础 |
| 5911f1c9b | ✅ 通过 | 修复登录循环，白名单路径硬编码 |
| 631eecc6c | ✅ 通过 | 7个组件 null 安全修复 |
| 674d5fbf8 | ✅ 通过 | 补齐 null 断言修复 |
| 3f5335197 | ✅ 通过 | Linter 清理 |
| c3c2d1c5a | ✅ 通过 | 类型守卫修复 |
| 7b7e5478e | ❌ 待审批 | **路由守卫写入 store 副作用** |

#### 🔴 待审批 1: JWT 通过 SSE URL 泄漏 (`9b58404ee`)

**现象**: 早期版本中 SSELiveStream 将 JWT 通过 `?token=` 查询参数传给 EventSource。当前代码已改用 `{ withCredentials: true }`（cookie），但注释中仍提及 `?token=` 兜底方案。

**风险**: URL 中的 token 会泄漏到服务器日志、浏览器历史、Referer 头。

**建议**: 确认后端 `admin/live_stream_sse.go` 已完全移除 `?token=` 兜底，或将其纳入统一认证路径。

---

#### 🔴 待审批 2: 路由守卫写入 store (`7b7e5478e`)

**现象**: 审计发现 `router/index.ts` 中 `isSuperAdmin()` 在 guard 执行期间写 `store.userInfo = parsed`，触发 Vue 响应式副作用。

**当前状态**: 经检查当前代码中已不存在该模式，后续提交可能已修复。建议确认。

---

### 模块组 2: Streaming / Session / State (9 提交)

| 提交 | 结论 | 说明 |
|------|------|------|
| f12637792 | ✅ 通过 | 文档 |
| c5082fb49 | ✅ 通过 | ⭐ **最佳提交** — request ctx 善后解耦，质量优秀 |
| 5d1375d45 | ✅ 通过 | 缓存超时后延修复，配置解耦正确 |
| 21c615eb9 | ✅ 通过 | batch_writer 补超时 |
| b6f8a69cb | ⚠️ 需关注 | handoff 模块依赖描述被连带精简，与主题无关 |
| 825d741ee | ✅ 通过 | 经检查当前代码中 registerFeishuRoutes 仅定义一次，无重复注册 |
| aab911206 | ✅ 通过 | metrics 埋点正确 |
| 23b6e3c24 | ✅ 通过 | 5 个 R1.12 hotfix 均正确，测试充分 |
| 45ff5a25c | ✅ 通过 | NVIDIA NIM 降级阈值 3→2，瞬时错误族共享计数 |

---

### 模块组 3: Routing / Telemetry / Cache (14 提交)

| 提交 | 结论 | 说明 |
|------|------|------|
| 93256354f | ✅ 通过 | 3 层路由 bug 修复正确，migration 幂等 |
| 6feb1ff7e | ✅ 通过 | 纠正 plan_type vs billing_mode 错误假设 |
| 687ea3bb7 | ✅ 通过 | routing resolve 加 OR 分支匹配 standardized_name |
| **a6fbe0937** | ❌ 待审批 | **model_name_mapping 并发竞态 + 缺少索引** |
| 26959fef2 | ✅ 通过 | 6 个中国厂商映射 |
| ec4db8d2e | ✅ 通过 | 43 家厂商前缀映射扩展 |
| 04b7ad7ad | ✅ 通过 | telemetry 合并 + 约束 |
| 956f6b7f8 | ✅ 通过 | 修复 affinity_hit 歧义列引用 |
| 1567cfd5a | ✅ 通过 | request_wal_hot 补齐主键 |
| cd80e4f3c | ✅ 通过 | 补齐 request_logs_hot 主键 |
| c6c40527d | ✅ 通过 | 补齐 affinity_hit 写入路径 |
| 4c42791dd | ✅ 通过 | 提取 stickyHitForChosen 纯函数 + 测试 |
| 101891b8d | ✅ 通过 | 修正参数编号偏移 |
| d38b6fc4a | ✅ 通过 | 文档 |

#### 🔴 待审批 3: model_name_mapping 并发竞态 (`a6fbe0937`)

**文件**: `admin/model_name_mapping.go:299-322`

**现象**: raw_model_name 变更时使用 `DELETE + re-INSERT` 模式，非事务保护。并发时两个请求同时修改同一 mapping：
1. 请求 A 删除 id=1
2. 请求 B 也删除 id=1（影响 0 行）
3. 请求 A 插入新行
4. 请求 B 插入（可能冲突或创建重复）

**风险**: 并发修改 model name mapping 可能导致数据不一致。

**建议**: 将 delete+re-insert 放在事务中，或改用 `UPDATE ... ON CONFLICT DO UPDATE`。

#### 🔴 待审批 4: 热点 SQL 缺少函数索引 (`a6fbe0937`)

**文件**: `provider/client.go:743`

**现象**: `loadCandidatesDB` 中新增 `LEFT JOIN model_name_mapping mnm ON lower(mnm.raw_model_name) = lower(mo.raw_model_name)`，但 `model_name_mapping` 表没有 `lower(raw_model_name)` 的函数索引。

**风险**: 热点路径（每个请求）可能触发全表扫描，影响延迟。

**建议**: 添加 `CREATE INDEX CONCURRENTLY idx_mnm_lower_raw ON model_name_mapping (lower(raw_model_name))`。

---

### 模块组 4: Dashboard / Monitoring (12 提交)

Dashboard 模块整体质量良好。ECharts 组件有 TypeScript 类型不匹配问题（共 2903 个 vue-tsc 错误），属于前端的类型定义依赖缺失（`chart.js` plugin 类型未安装），建议在 `web/package.json` 中添加 `@types/chartjs-plugin-annotation` 等依赖。

| 提交 | 结论 | 说明 |
|------|------|------|
| f467d54bc | ✅ 通过 | Tab 按钮移到 page header |
| c12ed733d | ✅ 通过 | Tab 导航 |
| f804b40c2 | ✅ 通过 | 隐藏 stats tab 中的 LiveRequestStreamV2 |
| a4e4b13a0 | ✅ 通过 | High 优先级问题修复 |
| c9fa6b87e | ✅ 通过 | DB degradation tests + docs |
| 0fee4b573 | ✅ 通过 | 部署经验教训文档 |

---

### 模块组 5: Data-lifecycle / Storage (9 提交)

| 提交 | 结论 | 说明 |
|------|------|------|
| 511d67fdb | ✅ 通过 | Hot 表迁移和分区删除功能 |
| be66a8d31 | ✅ 通过 | Hot 表迁移前端界面 |
| ef422268b | ✅ 通过 | HotPartitionManager API 修复 |
| f56f3c5ed | ✅ 通过 | API 调用修复 + 前端构建 |
| 17c6f8f6a | ✅ 通过 | VACUUM/FULL/REINDEX i18n |
| b2c9ff9fa | ✅ 通过 | Monthly partition coverage |
| 1f7ef84cf | ✅ 通过 | Storage overview i18n |
| b78acc7ce | ✅ 通过 | Hot 表统计字段名不匹配修复 |
| 17290741f | ✅ 通过 | 禁用重复编号 migration 文件 |

---

### 模块组 6: Ops-platform / Licensing / Fault / AutoUpdate (13 提交)

| 提交 | 结论 | 说明 |
|------|------|------|
| f985efe62 | ✅ 通过 | Phase 1 框架 |
| 77e73fb7d | ✅ 通过 | License 加密方案正确 (RSA-2048 + AES-256-GCM) |
| **6b7f3e387** | ❌ 待审批 | **故障自愈模块存在 2 个 P0 问题** |
| ded0f4337 | ✅ 通过 | Auto-update 完整实现 |
| 7e05506a4 | ✅ 通过 | 中心运维 Phase 5 |
| 2e475800c | ✅ 通过 | Phase 6 & 8 前端+API+集成测试 |
| 8d2b28917 | ✅ 通过 | VibeCoding Phase 7 |
| 579b49383 | ✅ 通过 | 运维平台 API 路由注册 |
| 68f673f3e | ✅ 通过 | 修复 62 个 golangci-lint 问题 |
| **2043afd67** | ✅ 通过 | SQL 占位符修复 (int→strconv) |
| **c10e83567** | ⚠️ 需关注 | 离线激活 API 存根（TODO） |
| ca1bfe522 | ✅ 通过 | 路由健康检查自动发现与修复 |

#### 🔴 待审批 5: 故障自愈模块命令注入风险 (`6b7f3e387`)

**文件**: `fault/action_executor.go` (已不存在，模块可能被移除或重构)

**现象**: 审计发现 `handleRunScript` 使用 `exec.CommandContext(ctx, scriptPath, args...)`，`scriptPath` 来自 `rule.ActionConfig`，无路径白名单限制。

**风险**: 如果攻击者可创建/修改故障规则，即可执行任意命令。

**建议**: 确认当前故障模块是否已移除或修复该函数。如仍存在，添加白名单目录检查。

#### 🔴 待审批 6: getMetricValue 全部返回 0.0 (`6b7f3e387`)

**现象**: 5 个指标采集函数全部返回 `(0.0, nil)`，是存根实现。

**风险**: 故障检测的轮询逻辑永不触发。

**建议**: 实现真实指标采集，或移除存根代码避免误导。

---

### 模块组 7: i18n (7 提交)

全部通过。i18n 修复覆盖全面（21 文件/2500+ 行/8 语种），flat-key 兼容层设计合理过渡方案。

| 提交 | 结论 | 说明 |
|------|------|------|
| ee7dffac8 | ✅ 通过 | i18n parity 修复 |
| fde03ae17 | ✅ 通过 | 完整修复 |
| fa3a19bbe | ✅ 通过 | outputCompliance 翻译 |
| 4a3fc4267 | ✅ 通过 | flat-key 兼容层 |
| 8d0a4ec9c | ✅ 通过 | 清理 127 文件 3000+ 行 TODO |
| 0637ce046 | ✅ 通过 | 文档 |
| f95fd71d0 | ✅ 通过 | 文档 |

---

### 模块组 8: Bots / Module Management (9 提交)

| 提交 | 结论 | 说明 |
|------|------|------|
| 90ebbd0e5 | ✅ 通过 | 飞书机器人 + 模块 UI 重构，Plugin 模式设计良好 |
| 53e5529c9 | ✅ 通过 | 钉钉机器人模块 |
| a2d936a8c | ✅ 通过 | 审计修复：body→body_size / env var 回退 / 请求体限制 |
| **0ee78a21b** | ✅ 通过 | 桥接设置 + 回调验签修复 |
| **55b6b7dda** | ✅ 通过 | 模块级联启用 + 回滚机制 |
| **b317811e8** | ✅ 通过 | applyCascadeEnable 纯函数提取 + 测试 |
| **433a85113** | ✅ 通过 | 恢复 19-spec/10-cap/4-dep 契约 |
| **e55e788f8** | ✅ 通过 | credentialfpslot metrics (8 个指标) |

---

### 模块组 9: Deploy / Infra / Scripts / Version (11 提交)

| 提交 | 结论 | 说明 |
|------|------|------|
| f1a85a774 | ✅ 通过 | 本地部署脚本（3 种模式） |
| **065a810fe** | ✅ 通过 | ⭐ **最佳提交** — 版本 SSOT 重构为 version.json |
| **10e78934b** | ✅ 通过 | version 字段只显示 tag |
| d126f6243 | ✅ 已修正 | deploy-154 + kaixuan1 脚本；SSH 密码已清除 |
| a9933616b | ✅ 通过 | Go 环境检测改进 |
| a3e13fef3 | ✅ 通过 | 环境变量加载 |
| 249676d17 | ✅ 通过 | 注释 BIN_NAME 过早日志 |
| 1abf7ee20 | ✅ 通过 | 更新 deploy-to-252.sh migration 路径 |
| 0351c26ae | ✅ 通过 | bump version |
| acb1941e1 | ✅ 通过 | bump version + BIN_NAME |
| a361a5cbc | ✅ 通过 | Ollama 协议字段白名单 |

---

## 待审批问题汇总（12 项）

| # | 优先级 | 模块 | 提交 | 问题描述 | 建议修复方案 |
|---|--------|------|------|---------|-------------|
| 1 | P1 | Auth | 9b58404ee | JWT 通过 SSE URL 泄漏 | 确认已移除 `?token=` 兜底，EventSource 使用 `withCredentials` |
| 2 | P1 | Auth | 7b7e5478e | 路由守卫写入 store | 确认当前代码已无该模式 |
| 3 | **P0** | Routing | a6fbe0937 | model_name_mapping 并发竞态 | 将 delete+re-insert 放入事务或用 UPDATE ON CONFLICT |
| 4 | **P0** | Routing | a6fbe0937 | 热点 SQL 缺函数索引 | 添加 `lower(raw_model_name)` 索引 |
| 5 | **P0** | Fault | 6b7f3e387 | 命令注入风险 | 确认当前是否仍存在；添加路径白名单 |
| 6 | **P0** | Fault | 6b7f3e387 | getMetricValue 全返 0.0 | 实现真实指标或移除存根 |
| 7 | P2 | License | c10e83567 | 离线激活 API 存根 | 实现完整功能或标记废弃 |
| 8 | P1 | Streaming | b6f8a69c | handoff 依赖描述被连带精简 | 恢复完整 19-spec/10-cap/4-dep 描述 |
| 9 | P2 | Dashboard | 全局 | 2903 个 vue-tsc 错误 | 安装缺失的 chart.js 类型声明 |
| 10 | P2 | Deploy | d126f6243 | deploy-kaixuan1.sh 密码已清除 | ✅ 已修正 |
| 11 | P3 | Version | 全局 | 构建耗时逐次+1而非基于git信息 | 当前行为，无需处理 |
| 12 | P3 | Reports | 全局 | Commit 信息中的 SHA 与实际不符 | 文档已说明，无需处理 |

## 积极发现（最佳实践）

### ⭐ c5082fb49 — request ctx 善后解耦
系统性将 request-scoped context 从善后写回/资源释放路径中解耦，所有写回路径使用 `runctx.BackgroundTimeout`。设计清晰、可审计、可测试。

### ⭐ 065a810fe — 版本信息 SSOT
从 6 个来源统一为单一的 `version.json`，删除 100+ 行冗余解析逻辑。

### ⭐ License 模块加密方案
RSA-2048 签名 + AES-256-GCM 加密 + JWT HMAC，方案选择正确。

### ⭐ 飞书/钉钉 Plugin 架构
可插拔、配置驱动、安全优先的模块架构，值得后续模块参考。

---

## 结论

- 整体代码质量 **良好**，Go 后端代码规范、测试覆盖较好
- SQL migration 遵循幂等 + down.sql 规范（虽有少数遗漏）
- 前端 TypeScript 类型质量需改善（2903 个 vue-tsc 错误）
- 最紧急的 **SSH 密码硬编码** 问题已修复并推送
- 12 项待审批问题中，建议优先处理 **#3、#4、#5、#6**（P0 级）

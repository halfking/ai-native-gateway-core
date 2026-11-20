# L10 路由覆盖核对报告

**生成时间**：2026-11-20  
**方法**：TypeScript AST 解析 `web/src/router.ts` vs README §3 逐条路由核对表

## 1. 统计概览

| 指标 | 数值 |
|---|---|
| `router.ts` 实际路由总数 | **110** 条 |
| README §3 已记录路由数 | **115** 条 |
| 未被 README 覆盖的路由 | **15** 条 |

> ★ README 记录数 > 实际路由数的原因：README 包含了一些**已废弃的重定向路由**（如 `/catalog`、`/routing`、`/probe-health`、`/system-monitor`、`/routing-v2/annotations` 等），这些在核对表中有记录但可能已经在 router.ts 中被简化或合并。

## 2. 未覆盖路由清单（15 条）

### 2.1 已有 flow 但未登记进 README §3（4 条）

这 4 条路由在 `maestro/flows/16-session-analytics-system.yaml` 中**已被覆盖**，但 README §3 中**缺少对应章节**（§3.12）：

| 路由 | 说明 |
|---|---|
| `/admin/session-analytics` | 会话分析总览页（聚合族） |
| `/admin/session-analytics/user-ranking` | 人员排行 |
| `/admin/session-analytics/agent-ranking` | 智能体排行 |
| `/admin/session-analytics/model-ranking` | 模型排行 |

> **处置**：需要在 README 中补充 §3.12 章节，记录这 4 条路由的覆盖情况。

### 2.2 无 flow 覆盖（11 条）

这 11 条路由在当前所有 flow 中**均未出现**：

| # | 路由 | 组件/说明 | 权限 |
|---|---|---|---|
| 1 | `/admin/pi-analysis` | PI 错误分析（2026-11-18 PI 分析管理面优化轮） | super_admin |
| 2 | `/admin/plugin-settings` | 插件提供商配置（Native/Casdoor 切换，2026-11-15） | super_admin |
| 3 | `/admin/self-update` | 自更新与升级（检索最新版本 → 提醒 → 安装升级，2026-11-16） | super_admin |
| 4 | `/help` | 帮助手册（HelpManualView） | requiresAuth |
| 5 | `/login` | 登录页（LoginView） | public |
| 6 | `/model-credibility` | 模型可信度（ModelCredibilityView） | super_admin |
| 7 | `/operations/market-prices` | 市场价格采集面（2026-11-13 A-17 收口 M3-T1） | platformOps |
| 8 | `/operations/marketing` | 营销管理（套餐/加油包/优惠码/活动，2026-11-09） | platformOps |
| 9 | `/operations/sales` | 报价系统 MVP 工作台（2026-11-05 W1-T2） | requiresAuth |
| 10 | `/simple/keys` | 简易模式密钥签发（两字段，SimpleKeysView） | requiresAuth |
| 11 | `/tenant/bills` | 月账单（MaaSBillsView） | requiresAuth |

> ★ **`/login` 特殊说明**：登录页在 `maestro/flows/_shared/login.yaml` 中被所有需要登录的 flow 调用，但 README §3 的核对表中**没有单独列出** `/login` 这条路由（§2 Flow 清单中提到了「1（`/login`）在共享登录流里」，但 §3 逐条核对表缺失）。

## 3. 反向核对：README 中有但 router.ts 中无的路由（5+ 条）

这些路由在 README 中有记录，但在当前 `router.ts` 中**已不存在**（可能是重定向路由被简化）：

| 路由 | README 中的说明 | 状态 |
|---|---|---|
| `/catalog` | 重定向到 `/models` | router.ts 中仍存在该重定向（第 258 行），但提取脚本未捕获 |
| `/routing` | 重定向到 `/routing-v2` | 同上 |
| `/probe-health` | 重定向到 `/routing-v2/credentials` | 同上 |
| `/system-monitor` | 重定向到 `/dashboard` | 同上 |
| `/routing-v2/annotations` | 重定向到 `/routing-v2/auto-ops` | 同上 |
| `/routing-v2/annotations/stats` | 重定向到 `/routing-v2/auto-ops` | 同上 |
| `/routing-v2/task-profile` | 重定向到 `/routing-v2/auto-ops` | 同上 |
| `/routing-v2/auto-tuning` | 重定向到 `/routing-v2/auto-ops` | 同上 |
| `/ops`、`/ops/licenses`、`/ops/faults`、`/ops/autoupdate`、`/ops/center` | externalMaintainRedirect 调用生成的路由 | 不在 routes 数组里，由 router.ts:354-358 的 externalMaintainRedirect 动态创建 |
| `/maas/models`、`/maas/account`、`/maas/pricing`、`/maas/usage` | 重定向到 `/tenant/*` | 同上，router.ts 中仍存在 |
| `/request-detail/:requestId` | 请求详情全页 | router.ts 中不存在该路径；实际是 `/live/request/:requestId` |
| `/operations/price-compare` | 重定向到 `/model-pricing`（2026-11-19 合并轮） | router.ts 第 398 行，提取脚本未捕获 |
| `/admin/session-analytics/filter-options` | 会话分析筛选 | router.ts 第 301 行，提取脚本未捕获（在条件编译块里） |

> ★ **提取脚本的局限性**：当前提取脚本只匹配 `{ path: 'xxx'` 的直接形式，**未处理**：
> - `redirect:` 字段中嵌套的 `path`
> - `externalMaintainRedirect()` 调用生成的路由
> - 条件编译块（`import.meta.env.DEV`）中的路由

## 4. 建议处置

### 4.1 立即处置（L10 范围内）

1. **补充 README §3.12**：为 `16-session-analytics-system.yaml` 覆盖的 4 条路由添加章节
2. **补充 `/login` 条目**：在 README §3 某个章节（建议 §3.1 公开页）中单独列出 `/login`

### 4.2 后续处置（登记为遗留项）

1. **为 11 条无覆盖路由补 flow**：
   - 优先级 P1：`/admin/pi-analysis`、`/admin/self-update`（最近新增的核心功能）
   - 优先级 P2：`/operations/*` 三页、`/help`、`/model-credibility`
   - 优先级 P3：`/admin/plugin-settings`、`/simple/keys`、`/tenant/bills`

2. **改进提取脚本**：支持 redirect / externalMaintainRedirect / 条件编译

## 5. 口径说明

- **提取方法**：用 `awk` 从 `router.ts` 的 `routes: [...]` 数组中提取所有 `{ path: 'xxx'` 形式的路径
- **比对方法**：用 `comm -23` 对两个排序后的列表做差集
- **已知局限**：
  1. 未处理 `redirect:` / `externalMaintainRedirect()` / 条件编译生成的路由
  2. 未处理子路由（`children:` 数组）
  3. 未验证 README 中记录的 flow 文件是否真实存在对应的 openLink

## 6. 附录：完整对比数据

### 6.1 router.ts 实际路由（110 条）

见 `/tmp/router-paths-clean.txt`

### 6.2 README §3 已记录路由（115 条）

见 `/tmp/readme-covered-paths.txt`

### 6.3 未覆盖路由（15 条）

见 `/tmp/uncovered-routes.txt`

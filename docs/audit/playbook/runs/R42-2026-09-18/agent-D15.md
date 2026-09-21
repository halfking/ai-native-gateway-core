# D15 可观测性/数据展示统一性/菜单/交互一致性 子代理报告(窗口:0a015af51^..b75c91900)

## 一、发现(候选,待主代理复核)

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | **P0 候选**(CI/verify 门禁必挂) | 721 五个新 i18n key 只加进 zh-CN 与 en-US,其余 6 语言全部缺失。web/src/i18n/parity.test.ts 的门禁测试"every locale explicitly defines all zh-CN leaf keys"与"every locale resolves zh-CN module keys through the configured English fallback"断言每个非 zh-CN locale 的 leaf key ⊇ zh-CN,无任何 allowlist——两测试必挂。verify.sh:67 跑 pnpm run test,verify-ci.yml:50 跑 ./verify.sh --web → 全红。运行时副作用:fallbackLocale 'en' 使 6 语言 UI 新余额区块混排英文(交接文档自述只跑了 vue-tsc/vite build,漏了 vitest) | web/src/i18n/parity.test.ts:37,191-245;web/src/locales/zh-CN/providers.ts:218-222;6 locale providers.ts 缺 key;verify.sh:67;.github/workflows/verify-ci.yml:50 | 实跑复核后把 5 key 补进其余 6 语言(机翻即可) |
| 2 | P2 | **⟳ 的 HTTP 4xx/5xx 错误态对用户完全静默**:balanceRefreshError 是只写状态,模板从未渲染。req() 对非 2xx 抛 ApiError,走 catch 只写该死状态。对无余额 API 厂商点 ⟳ → 400 → 界面无任何反馈,操作者误以为刷新成功。为该场景写的 balanceUnsupported key 在全部 src 中零引用,是死 key。同页面既有错误惯例是 error.value 横幅,本函数未复用 | web/src/views/ProvidersView.vue:524,544-545,1391-1393;web/src/api/_core.ts:176-183;admin/provider_credential_balance.go:70-74 | catch 与 else 统一写入 c.balance_error(已渲染),400 用 balanceUnsupported;删除死状态 |
| 3 | P2 | **无差别 manual 戳导致 24h 自动余额刷新被静默豁免**:抽屉保存 saveCredential 总是携带 balance_usd 字段(即使未改动),而 updateCredential 对任意 req.BalanceUSD != nil 一律盖 manual 戳;floor guard 与 probe_v2 据此跳过 24h。操作者只改 tag/备注 → 保存 → 受保护凭据自动探测停摆 24h。同根:⟳ 成功(source='api')后任一次抽屉保存即翻转为 manual | web/src/views/ProvidersView.vue:501;admin/provider_credential.go:620-624;bg/balance_floor_guard.go:858;bg/credential_probe_v2.go:588 | 后端仅在值实际变化时盖 manual 戳,或前端仅改动时携带 |
| 4 | P3 | 交接遗留#4 确认未恶化但不一致面扩大为三处:CredsTab.vue 手写表格完全不展示 balance_usd/⟳/元数据;ErrorDetailTab.vue 裸显 balance_usd+currency 无来源/时间/错误;ProvidersView 抽屉是唯一全量展示面 | web/src/views/provider-detail/CredsTab.vue:487,906,1243;ErrorDetailTab.vue:132 | CredsTab 复用 refreshCredentialBalance+同款元数据行 |
| 5 | P3 | refresh-balance 的最常见失败路径无结构化日志:!ok 分支只写 DB 列,不像 decrypt/写库失败/egress 那样 slog | admin/provider_credential_balance.go:90-105 | !ok 分支补 slog.Warn |
| 6 | P3 | 非 USD 货币符号绝对定位叠在 80px 数字输入框上;当前服务端恒回 'USD',纯前瞻性瑕疵 | web/src/views/ProvidersView.vue:1376;admin/provider_credential_balance.go:126 | 暂缓,currency 非 USD 真实出现时再改 |

## 二、核实为健康的面

- **⟳ 按钮与元数据展示复用既有套件**:btn btn-ghost btn-sm 同款;错误红字复用 .health-error 类+var(--danger) 主题 token;emoji 图标是全站既有惯例(12+ view 使用)。
- **fmtTimeAgo 复用**:新元数据行与既有用法同一函数同一格式。
- **默认排序 usage 与列头指示**:qualitySortKey 默认改 usage 复用既有 sortProvidersByQuality;本页从未有过列头排序指示符,默认值变更未造成指示不一致。
- **路由与鉴权**:refresh-balance 挂在既有 handleProviderCredentials switch 内,与 reveal/check/models 同一 providerConsole 鉴权链,POST-only;menu-config.json 窗口内 diff 仅 exported_at 时间戳;无新增孤儿页/window.confirm/手写表格。
- **manual 保护谓词两处同步**(bg SELECT/UPDATE 语义)+探测失败 fail-open+400 配置事实不入 balance_error 的取舍有注释依据+截断 rune 边界安全。
- **session_online_pagination/logs/live_stream_sse**:窗口内零提交零改动(派发词中"窗口内有改动"经查不成立)。

## 三、未覆盖项与原因

parity 测试未实跑(只读纪律,静态证据链完整,建议主代理实跑);⟳ 端到端实机验证(需真实凭据与出口);keys_referenced/hardcoded_cjk 未逐条推演(keys_referenced 以 zh-CN 为 SSOT 按构造应过)。

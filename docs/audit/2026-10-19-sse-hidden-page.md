# 2026-10-19 隐藏页SSE连接生命周期审计

基线main1abd79df3；source fe482b5e7195c8bc94b90a447a8bc5e7f1d676f5经最新并行主干cfb7e35f4 no-ff合并为cb4c07f950941de7240bc40d5089be67935232bc，已推送并回读远端。三文件154增/35删，共189行；原共享checkout保持原状。

根因：document.hidden期间旧onmessage先接收数据再丢弃，只节省渲染，后台仍产生传输。后端周期状态去重78b32c7d6仅覆盖不变queue/node，不能代替关闭后台连接。

改动文件与行为：

- web/src/composables/liveStreamStore.ts：visibility监听跟随consumer refCount，隐藏立即close/隔代/取消fatal timer，首次在后台acquire也不建连；可见时新连接获取完整权威重放，多consumer共享一条连接，最后释放卸监听。manual/identity/fatal重连均受同一隐藏守卫约束，旧generation回调隔离。
- liveStreamStore.visibility.test.ts：5条行为回归覆盖首次hidden、恢复空状态、双consumer/最后释放、fatal/手动/身份路径及真实listener存活；旧liveStreamStore.test.ts将主动断流后的重放期望同步，未降低断言。

测试命令与结果：

- node node_modules/vitest/vitest.mjs run src/composables/liveStreamStore.visibility.test.ts src/composables/liveStreamStore.test.ts src/composables/liveStreamStore.identity.test.ts src/composables/liveStreamStore.actions.test.ts src/composables/useLiveStreamUrl.test.ts src/composables/liveStreamDisplay.test.ts src/components/LiveRequestStreamV2.queueStreamNotice.test.ts：111 passed；旧store只读Vite overlay下5个新用例全部RED。独立TypeScript审查另跑四文件71 passed。
- pnpm run typecheck：独立执行通过；pnpm build完整通过menu export/element审计/UI strict/vue-tsc/Vite，构建17.13s。仅生成menu-config exported_at变动，完整其它字段等价后防护恢复；未纳入时间戳噪声。corepack在该shell不存在，使用既有pnpm10.29.2，无依赖安装/升级。
- browser-use专用headed session sse-hidden-1019，真实native tab hidden/visible、真实EventSource/HTTP及实际store，共9条断言PASS。首次consumer在真正hidden取得ref时opened0；visible收到当前requests1/queue7/nodes1；双consumer共1连接，hidden关闭；6.502s后台frames243→243且不重连；visible新initial_data权威清空；final release后不恢复。最终opened3/closed3/active0。未伪造document.hidden或合成visibilitychange。
- Go/后端未变；本片未重复已通过的全仓Go检查。generic/TypeScript正式审查均绑定source fe482b5e7及merge cb4c07f95，三web blob逐字一致，无剩余P1/P2。

验证边界：browser fixture验证实际store/协议，不等于生产整页、鉴权或业务DB验收。源码SHA256438662c1f0e2cd1adf693906f63f722a566f1a2f2822b35c4a54c5d5d65af959在浏览器前后相同，独占session及PID6657服务已关、57061拒绝连接。原有匹配manifest/locks的依赖以隔离ignored目录内package symlink复用，未改原依赖目录。

生产未部署。252实际转211:8000→8781，Mac Mini备用；版本文件旧9c43da60且根/前端序号2679/2715不一致，未证明二进制提交身份。当前源码AdminMiddleware只接受有效用户JWT/cookie，先前用ops管理key采样401不能据此断言服务故障；需既有合法用户session完成同周期取样。回滚本片：git revert fe482b5e7并走原验证/发布流程。

用户已确认异常供应商为efan（原月100G足够、现一周耗尽）。六机保存同订阅源，但34大LLM采样DIRECT；不得将公网SSE量或夹具99%记作efan扣费。本片未发布生产、未改数据库/鉴权/依赖或全局代理。

持久证据：~/.codex/audit-backups/network-switch-20261019/llm-sse，目录0700/文件0600；browser-hidden下含report/9断言/真实HTTP事件及清理，RED/GREEN/build日志同存。

下一轮提示词：先读network-switch主报告/handoff及本记录，核对远端最新main、SSE后端78b32c7d6和前端fe482b5e7是否在用。沿252→211→Mac Mini备用核实二进制身份与发布锁，保持一行回滚，走既有发布/健康/新鲜度门后再量化同期流量。使用合法用户JWT采样，不把ops key的401当生产故障。对齐efan重置/追加与耗尽日期及共享客户端，补同步计量/账单；尊重211未迁移依赖和252守卫/xray边界，不声称周归因完成。每片fresh验证、TaskStop与精确SHA审查后no-ff合并推送。

# 隐藏页 SSE 连接生命周期修复计划

基线：已推送main1abd79df3；原共享checkout保持不动。此前onmessage在隐藏后丢帧仍产生传输，服务端状态去重不能消除请求/心跳等其余流量。

- [x] 写行为回归并在旧逻辑确认RED：隐藏关闭、首次hidden不连接、visible重放、多消费者/最后释放、隐藏期间manual/fatal/identity不重开、旧generation帧不写状态。
- [x] 将visibility listener绑定消费者引用计数；传输close不移除listener，最终release移除；统一open隐藏守卫，hidden立即产生重放需求并关闭，visible仅在有消费者时新连。
- [x] 更新原“没收到帧不重连”判据以符合主动断流；保留identity租户清空、空状态和请求完整语义。
- [x] 定向Vitest、类型检查与构建门，Go后端未变无需重跑全仓Go；真实browser-use页面切后台/回前台验证EventSource断开、恢复权威数据和引用计数。
- [x] 独立通用与TypeScript审查绑定最终SHA，完整TaskStop、小片commit/no-ff main合并/push；文档与handoff独立小片同步，生产发布与账单节省保持未验证边界。

只改liveStreamStore和必要测试，不顺带改probe store/全局鉴权/后端/依赖或生产配置。可用本机现有匹配依赖进行隔离验证，安装及缓存文件不得进提交。源码一行回滚为git revert本片commit，生产仍走既有发布验证。

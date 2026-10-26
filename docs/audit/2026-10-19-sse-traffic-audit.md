# 2026-10-19 live-stream周期状态流量审计

本轮来源：network-switch六机流量审计。基线origin/main为39ff587b2；在独立codex/sse-traffic-audit-20261019工作区实现，不覆盖原共享main。

## 结论与根因

252七日完成请求中live-stream响应328.02GB是公网响应量，不等于机场扣费。源码两套2s ticker在内容不变时仍对每个连接发送全量queue_snapshot/node_update。浏览器隐藏页原逻辑是在收到数据后丢弃，因此仍消耗传输；该前端行为本片不修改；后续已由fe482b5e7修正，见[隐藏页续轮](2026-10-19-sse-hidden-page.md)。

核心修复commit为78b32c7d606150a29565021593ba6519d03289de：在已有客户端writeMu下维护最多两类SHA256摘要，仅去重相同周期状态。变化与空状态、新连接、initial_data、请求事件和25s keepalive注释保持可达。

审查另发现并关闭两个P2：

- 注册先于初始回放，周期Q1可先写、晚initial_data再覆写Q0。普通帧成功写出后失效缓存，使下一tick重新发Q1；真实handler屏障及正式回归都由RED转GREEN。代价是普通事件后每类最多补发一帧，不把它写成所有类型合计一帧。
- QueueProjection.SourceVersion每次采样都会Add(1)，不能当业务变化。仅在摘要的浅副本中归零，发送数据仍保留真实采样版本；生产同形递增版本测试先RED后GREEN。

## 测试与边界

- CGO_ENABLED=0 go test ./admin -run TestPeriodicState -count=1：通过变化/空状态、新客户端、重放交错、并发、请求/keepalive、写失败重试与字节夹具。
- CGO_ENABLED=0 go test ./admin -run 'Test(LiveStream|LiveQueue|LiveNode|PeriodicState|HandleLiveStream|FanOut)' -count=1：通过所选live-stream集合。
- CGO_ENABLED=1 SDKROOT=/Library/Developer/CommandLineTools/SDKs/MacOSX26.5.sdk GOFLAGS=-p=2 go test -race ./admin -run TestPeriodicState -count=2：通过，仅既有vendor编译warning。
- CGO_ENABLED=0 go build ./...、go vet ./...：通过。未改全局CI白名单或刻意为红的退休负向闸门，不声称全仓测试全部通过。
- 字节夹具要求首次恰好一帧且非零，100次稳定tick字节与帧数严格不增加，避免“完全不发数据”或多发一帧假绿。
- 通过Go overlay分别禁用周期去重和丢弃所有周期帧，重复帧/并发及首次非零字节断言重新失败；生产文件未因变异而改写。
- 固定500节点、无其他普通事件的100个稳定tick：原约11,139,300B，修后111,393B。此99%仅为夹具重复量，普通事件会使缓存失效，实际业务变化也会发送；不能外推线上或周账单节省。
- Go及通用审查均绑定精确source SHA78b32c7d606150a29565021593ba6519d03289de，两个P2关闭。

实际browser-use使用新隔离session与原Go Hub的HandleLiveStream/Run/Publish，共7条协议断言通过：初始数据、4个真实2s周期稳定去重、变化、清空、改变后重连、两条相同请求事件保留、未改变状态再重连。最终initial_data=3/queue_snapshot=6/node_update=6/request=2；普通请求后每类最多补发一帧。overlay只添加测试fixture，实际编译最终源文件；自己的浏览器session/56959服务均已关闭。此验证无DB/Redis，不代替生产/live全页、客户权限或auth middleware验收；keepalive由单测独立验证。

核心source78b32c7d已no-ff集成到主干126419af2，merge 36d32f52fcc0e3a1b4ca9b95be8504ca1751d423已推送并由远端main回读确认；原共享checkout保持原状。已测合并树与最终合并树的运行代码一致，期间上游仅两份文档变化；build/admin vet/live-stream回归通过，Go和通用审查均绑定最终merge SHA。

## 运行侧限制与回滚

只读核对252的Nginx配置：公网upstream主指向211的100.106.67.198:8000，后者ns_llm_gateway_upstream主指向127.0.0.1:8781容器；两层备用均为Mac Mini的100.106.169.63:8782。211容器llm-gateway-211健康运行，252本地开发服务disabled。部署目标必须按此现场链路核验，不能只更新252本地二进制。

SSOT env-injector实际入口在~/.agents/skills/env-injector/scripts/env-injector.sh，注入成功；旧文档的工具包脚本路径失效。公网12s/2MiB预算的只读SSE采样返回401，未绕鉴权或输出key，不能宣称取得生产帧统计。252 SSH别名配置UserKnownHostsFile=/dev/null会使StrictHostKeyChecking无可用pin；显式指定已有~/.ssh/known_hosts即可保持严格校验访问，无须接受新指纹或改全局配置。

本片未改DB/索引、身份/租户策略、依赖、前端或生产配置，未部署新二进制。回滚核心修复：`git revert 78b32c7d6`。后续先核对252 Nginx真实upstream和在用版本，使用正确只读管理身份取样；生产发布须走已有蓝绿/序号/健康门，另验变化新鲜度及同期公网流量。后续隐藏页断开/恢复源码已随cb4c07f95发布，实际native tab9项验收通过，生产仍未部署。

详细RED/GREEN/mutation/race/build/vet和脱敏401证据持久保存在本机私密~/.codex/audit-backups/network-switch-20261019/llm-sse（0700/0600），不记录订阅URL、key或生产帧内容。

下一轮提示词：从本记录及network-switch主审计handoff继续；先复核远端main/源码SHA与真实部署upstream。保留周期状态的逐连接摘要、晚回放失效、采样版本忽略和wire原版本；不要抑制请求事件或空状态。隐藏页生命周期源码fe482b5e7已修并经native tab实测，先核实在用版本与发布链。补生产只读采样/发布验证和同周期账单归因，不将夹具99%写成已节省机场配额。每片完整Task Stop与精确SHA审查后no-ff合并并推送。

# D14 安全子代理报告（窗口：67f78247c..294e0f65d，R39 增量面 = 全新 mDNS LAN 广播特性）

> 原文存档（主代理已逐条亲读复核，处置见轮文档）。子代理：Explore，2026-09-17。

改动面：discovery/lan_advertise.go(307行)+test(186行)、cmd/gateway/main.go mDNS 集成块(6989–7050)与 shutdown 路径、config/config.go LANAdvertise 配置(lan_advertise / LLM_GATEWAY_LAN_ADVERTISE)、go.mod/go.sum/vendor{hashicorp/mdns v1.0.7, miekg/dns v1.1.72}。来源 commit：6062a638c（特性）+ cd76a7f0e（main 集成重构）。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | P2 | a.stopCh 字段数据竞争：monitor goroutine 在 select 求值时**无锁读** a.stopCh，而 Start 重启路径在 mu 内**写**同字段；Stop#1 关闭旧 stopCh 后旧 monitor 可能尚未执行 select 求值，与 Start#2 的字段写入无 happens-before | discovery/lan_advertise.go:97（写，mu 内）vs :115（无锁读）；触发路径：Stop() 后立即 Start()（TestLANAdvertiser_Restart 即此序列，-race 下大概率可复现） | Start 内将 stopCh 存局部变量、捕获值传入 goroutine（不读字段）；加 -race 钉桩 |
| 2 | P2 | YAML `lan_advertise` 是死配置：mergeFrom 逐字段白名单不含 LANAdvertise，yaml tag 让运维以为可文件配置，实际解析进 fileCfg 后永不合并回 cfg，静默 no-op（安全上 fail-closed，但是空壳配置面） | config/config.go:289（yaml tag）、:827-1037（mergeFrom 全函数无 LANAdvertise 分支）；触发路径：config.yaml 写 lan_advertise: true → 功能不开且无任何报错/日志 | mergeFrom 补 `configValueConfigured(other,"lan_advertise")` 分支，或删 yaml tag 只留 env 并在注释钉死 |
| 3 | P2 | 蓝绿双实例同主机实例名冲突：两实例均以 os.Hostname() 作 mDNS 实例名 → 同一 `hostname._llm-gateway._tcp.local.` 对应 8781/8782 两个 SRV；hashicorp/mdns 无 probe/defend 冲突消解（库内 TODO 自认），SO_REUSEADDR 使两进程均成功绑 5353 互不报错 | cmd/gateway/main.go:7005-7010（hostname 作实例名）；deploy/llm-gateway-go-canary@.service:46（端口 8781/8782 交替）；vendor/github.com/hashicorp/mdns/zone.go:61-65；触发路径：蓝绿并存期均置 LLM_GATEWAY_LAN_ADVERTISE=true → LAN 客户端对同一实例名非确定地解析到蓝或绿端口 | 实例名拼端口/角色后缀（hostname-8782），并在文档写明蓝绿不可双开 |
| 4 | P3 | 广播信息面：TXT 泄露精确 version 指纹 + api 列表 + proto=http 明文标记；SRV/A 泄露主机名 + 全部非环回 IPv4 + 数据面端口（管理端点同端口监听）。任意 L2 邻居零成本探测，等于给版本定向攻击发定位信标。无凭据泄露（已核 TXT 仅三键） | discovery/lan_advertise.go:64-68（TXT 构造）、:100-108（启动 Info 日志含 ips）；触发路径：任何同网段主机发 `_llm-gateway._tcp.local` PTR 查询即得全部信息 | 默认关闭已缓解；设计文档记 accepted-risk；version 可考虑只广播主次版本 |
| 5 | P3 | DiscoverGateways/GatewayInfo 为死代码且 ctx 参数无效：全仓无生产调用方；`mdns.Query` 内部用 context.Background()，入参 ctx 被忽略；返回的 Address 完全可由任意 LAN 应答方伪造——未来若接入自动配置即成未认证信任边界（恶意邻居可把网关回调指向任意 host:port，SSRF 式重定向） | discovery/lan_advertise.go:216（带 ctx 签名）、:244（调 mdns.Query 而非 QueryContext）、vendor/github.com/hashicorp/mdns/client.go:73（Query→Background）、:302（Address 无校验拼接）；触发路径：未来任何调用方把结果写进配置/回调目标 | 接入前必须改 QueryContext + 对发现结果做应用层认证/token 校验；短期在函数头注释钉死 trust boundary 警告 |
| 6 | P3 | 依赖记账：mdns 是 discovery 的直接依赖却标 `// indirect`；x/tools+x/mod 经 miekg/dns 的 build-tag tools.go 被拖入 vendor（约 20 个死包的供应链审计面扩大） | go.mod:111（mdns // indirect）、:164/:166；vendor/github.com/miekg/dns/tools.go:1-10（`//go:build tools` 引 x/tools/go/packages）、vendor/modules.txt:845-865；触发路径：go mod tidy/vendor 即产生 | 重跑 go mod tidy、mdns 移入 direct require 块；或记录接受 |
| 7 | P3 | TestDiscoverGateways 每次非 -short 运行做真实多播查询+绑定 8785 真端口（Start 系列测试还依次占 8781-8784），CI 无 -short 时走真网络，多播不可用环境必 flaky | discovery/lan_advertise_test.go:144-186；触发路径：CI `go test ./discovery/ -count=1` | 对齐仓内 `-tags integration` 惯例改为默认跳过 |

## 二、核实为健康的面

- **默认关闭全链路**：config.go:746-749 仅 env 驱动、Load 零值即 false；全仓 grep（含 deploy/*.service、compose、config.example.yaml、scripts）`LLM_GATEWAY_LAN_ADVERTISE`/`lan_advertise` 仅出现在 main.go、config.go、discovery 两文件——无任何部署面默认置 true。fail-closed 成立。
- **绑定失败降级不 crash 主进程**：lan_advertise.go:91-94 将 NewServer 错误上抛 → main.go:7019-7021 仅 Error 日志 + lanAdvertiser=nil，HTTP 服务照常启动。端口占用/无多播接口走此路径（vendor server.go:65-73 双栈全败才报错；单栈失败静默 IPv4-only 降级，不崩）。
- **Stop 幂等 + double-close 安全**：running 守卫（lan_advertise.go:128-130）；stopCh 的 select-guard close 全程持 mu 无 TOCTOU（:139-145）；server.Shutdown CAS 防重入（vendor server.go:99-102）；ctx 取消触发的 monitor Stop 与 shutdown 路径显式 Stop（main.go:7047-7049）并发时被 mu 串行化，无 double-shutdown。
- **TXT 无敏感凭据**：仅 version/api/proto 三键（lan_advertise.go:64-68），无 token/secret/租户/内部拓扑信息。
- **DiscoverGateways 通道生命周期**：vendor 库从不 close params.Entries（仅 select-default 非阻塞发送，client.go:381-384），本侧 close 前已 wg.Wait（lan_advertise.go:244-253），含 Query 出错分支在内无 send-on-closed panic 路径。
- **锁纪律**：文件内单 mutex、无嵌套锁对；mu 持有期间仅 socket bind 短系统调用（NewServer），无长阻塞 IO，符合 D14 §3.1。
- **生产路径无 goroutine 泄漏**：main 仅 Start 一次/Stop 一次；monitor 在 ctx.Done 与显式 Stop 两条路径均有界退出（lan_advertise.go:111-118）；ListenAndServe 失败走 os.Exit(1)（main.go:7036），进程退出回收 mDNS socket，孤儿广播窗口可忽略。
- **vendored 依赖概览**：mdns/miekg 仅收发 224.0.0.251 / ff02::fb :5353 多播，无额外出网/exec 面；Server.recv 正常退出无忙转（shutdown flag 先置后 close，vendor server.go:116-131）。

## 三、未覆盖项与原因

- 蓝绿同主机双实例同开 mDNS 的实测广播结果（发现#3 为 vendor 源码推断：SO_REUSEADDR 复用 + 无冲突消解）——需真机多播环境，只读审计不可实机启停。
- `go test -race ./discovery/` 未执行（只读纪律不运行测试）；发现#1 系内存模型层论证，现有 Restart 测试在 -race 下预期可复现，留主代理验证门。
- miekg/dns v1.1.72 全量逐行审计未做（派发明确概览级即可），仅抽查 server.go recv/Shutdown 与 tools.go。
- golang.org/x/mod v0.36.0 的完整引入链未追溯到底（vendor 内 importer 为 docker/docker/internal/lazyregexp，疑似 go mod tidy 连带产物而非 mdns 直接所需），不影响安全结论。

# D17 代码卫生子代理报告（窗口：67f78247c..294e0f65d）

> 原文存档（主代理已逐条亲读复核，处置见轮文档）。子代理：Explore，2026-09-17。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | **P0** | 窗口 HEAD（=当前 HEAD，工作树干净）`cmd/gateway` **编译失败**：mDNS 块调用 `net.SplitHostPort` 但 import 块无 `"net"`（仅 line 20 `"net/http"`）。实跑 `go build ./cmd/gateway/` → `main.go:6998:22: undefined: net`，exit=1。引入者为 cd76a7f0e（"改进主程序 mDNS 集成"，经 merge 294e0f65d 入 main；前置提交 6062a638c 无 `net.` 调用，blame 6998 行归属 cd76a7f0e）。CI 必挂、网关二进制无法构建，属部署阻断 | cmd/gateway/main.go:6998（调用）vs cmd/gateway/main.go:16-30（import 块缺 `"net"`） | 补 `"net"` import，按三门（build/vet/test）验证后单独 fix commit |
| 2 | P3 | `github.com/hashicorp/mdns` 被标记 `// indirect`，但 discovery 包**直接 import** 它；`go mod tidy` 应归位到 direct require 块（miekg/dns 标 indirect 是正确的——全仓无直接 import，仅经 mdns 传递） | go.mod:111（`github.com/hashicorp/mdns v1.0.7 // indirect`）vs discovery/lan_advertise.go:14 | 下次触碰 go.mod 时顺手 tidy，不单独立项 |
| 3 | P3 | 窗口**新增** 5 个文件的 gofmt 违规（BASE 时这 5 个文件 gofmt 全部 clean，归因窗口内引入）：discovery/lan_advertise.go:42、146 尾随空格；cmd/gateway/main.go:7004、7009、7017 尾随空格（同属 mDNS 新块）；bg/node_probe.go:241 `decryptFailures atomic.Int64` 字段对齐错（新增字段后未重对齐，blame 归属 20fb4c7a4）；discovery/lan_advertise_test.go:106-108 struct 字段对齐；domains/session/v2/session_aggregate_outbox_reaper_integration_test.go:499-500 行尾注释对齐 | 各 file:line（`gofmt -d` 实证） | 随发现#1 的 fix commit 顺带 gofmt，避免单独跑一遍 |
| 4 | P3（标注即可） | discovery 四个零生产调用方导出符号：`DiscoverGateways`(:216)、`GatewayInfo`(:259)、`LANAdvertiser.IsRunning`(:153)、`LANAdvertiser.Status`(:160)——全仓（排除测试/vendor/非 Go 文件）零调用，仅 lan_advertise_test.go 引用。广播侧（NewLANAdvertiser/Start/Stop）已接线 cmd/gateway/main.go:7011、7044，**发现侧客户端未接线**，属分阶段特性预留面，按要求标注、不算问题。同理 `IsSanitizeSystemPromptEnabled`（system_prompt.go:21）现零调用方，但注释已诚实声明"暂无调用方，保留门控读取器供接线时复用" | discovery/lan_advertise.go:153,160,216,259；security/sanitize/system_prompt.go:21 | 按 D17 §3.1 在四处补 `// RESERVED(<特性>): LAN 发现客户端未接线` 类标注，防后续轮误判死代码重复怀疑 |

## 二、核实为健康的面

- **vendor 新增面精确收敛**：窗口 vendor 净增 75,562 行，仅为 hashicorp/mdns v1.0.7 + miekg/dns v1.1.72 及其传递依赖——x/mod/semver、x/net{bpf,internal/iana,internal/socket,ipv4,ipv6}、x/sync/errgroup、x/tools（vendor/modules.txt diff 逐项核对；x/tools 仅经 miekg/dns 传递，x/mod/semver 仅被 x/tools internal 引用，均 grep 实证）。无越界第四方依赖。
- **modules.txt ↔ go.mod 一致**：vendor 模式下 `go build ./discovery/ ...` 等 5 包通过（Go 在加载期做 vendoring 一致性校验，发现#1 的报错是纯代码错误、无 vendoring 错误）。
- **许可证齐备**：vendor/github.com/hashicorp/mdns/LICENSE、vendor/github.com/miekg/dns/LICENSE、vendor/golang.org/x/mod/LICENSE、vendor/golang.org/x/tools/LICENSE+PATENTS 均在。
- **记录不立案**：x/tools v0.45.0（约 4 万行，含 18K 行 stdlib manifest）仅因 miekg/dns 的开发工具桩 vendor/github.com/miekg/dns/tools.go:1-10（`//go:build tools`）被 vendored，运行时零编译使用——上游生态通病，本地不可消除，仅作为 vendor 体积来源知悉。
- **sanitize 删除面无误删**：窗口内 smart_sani_guard.go -46 行、system_prompt.go -65 行与提交 6f53b7bf9 清单逐条吻合（删 `injectPlaceholderProtection`/`InjectPlaceholderProtection`/`PlaceholderProtectionPrompt` 三符号，保留门控读取器）；全仓 grep（排除 vendor）对三个被删符号**零残留引用**，仅 system_prompt.go:6、8 历史注记注释按纪律提及符号名；新增历史注记与保留函数的注释均符合"注释诚实"要求。
- **debug/TODO 快扫清洁**：窗口增量 diff（排除 vendor/docs/web/public）新增行中 `fmt.Print*`/`log.Print*`/`println` 零命中、`TODO/FIXME/XXX/HACK` 零命中。
- **版本面为生成物噪音**：VERSION、version.json、web/public/version.json 版本戳 2.5.4-8125a06a-20260917-2121、web/public/menu-config.json 仅 exported_at 时间戳刷新——惯例上不算问题。
- **非重复实现**：discovery/lan_advertise.go:176 `getLocalIPs`（IPv4 非环回地址枚举）与 licensing/fingerprint.go:56 `getPrimaryMAC`（MAC 枚举）目的不同，非复制粘贴重复；config.go LANAdvertise 字段的 yaml+env 双 tag + Load() 显式 os.Getenv 与本文件既有惯例一致（config.go:16 等同款）。
- **除发现#1 外**，discovery/security/sanitize/internal/loopback/internal/probemode/autoroute 五包 `go build` 全部通过。

## 三、未覆盖项与原因

- `go vet` / `go test` 未实跑——cmd/gateway 编译失败使 vet 无从执行；其余包的测试执行属主代理修复发现#1 后的三门验证环节，非 D17 只读职责。
- scripts/*.sh、tests/lib/*.sh、mock-system-test/*.py 仅参与 debug 打印/TODO 增量扫描，未做 shellcheck/pylint 级审查——属 deploy/E2E 域。
- db/db.go（673 行）、db/probe_views_unified.go（348 行）、domains/routeincident/store.go（131 行）等大改动的深层正确性属 SQL/域代理范围，D17 仅做卫生面快扫（gofmt/debug/TODO 已覆盖其新增行），未复核逻辑正确性。

**给主代理的一句话摘要**：本窗口唯一的硬伤是 cd76a7f0e 漏 import `"net"` 导致 cmd/gateway 编译必挂（P0，main.go:6998），修复时顺带清 5 文件 gofmt 新债即可；vendor 面干净、sanitize 删除面经 grep 亲证零误删、discovery 零调用符号属分阶段预留面只标注不清理。

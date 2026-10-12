# 客户端供应商自动配置二次审计（2026-10-12）

## 范围与交付

审计 ZCode、MiniMax Code、Cursor、CC Switch 的 itestu/itestuu 接入，以及网关“接入指南 → 自动配置”的模型来源、脚本合并、权限、密钥处理与审计记录。本轮在独立工作树和 `codex/client-setup-audit-20261012` 分支进行，未改动原 checkout 的未提交状态。

`.29` 与 `.40` 是同一台主机，后续使用 `huangxt@192.168.31.40`。网关已按标准脚本部署 **2.5.8.2551**，活动端口 **8782**，`VERIFY_PASS=1`。前次 2545 的记录属于历史证据，不作为本轮验收。

## 发现与修复

| 问题 | 修复与判据 |
|---|---|
| ZCode 每次生成新 ID，按名称替换可能破坏默认模型引用；同名异址可能覆盖其他供应商 | 同名同址复用 ID，递归合并 options/models；同名异址、同 ID 异名和重复同名项拒绝写入。验证默认引用、旧模型、自定义 header 均保留。 |
| MiniMax YAML 文本拼接不能稳妥处理转义、尾部内容、重复键和现有 provider | 用严格 PyYAML 解析及节点位置合并，只替换目标 provider；解析后再校验完整文档。其他 provider 字节与根设置保留；非法/重复/flow 格式明确停止。 |
| 脚本内嵌内容可能进入 shell、Python 或 PowerShell 语法；备份权限和并发写入保护不足 | 配置以 hex 数据传入独立 Python 安装器；创建即 0600、fsync、原子替换、专属锁、符号链接拒绝、写前内容比较。hex 不提供加密。 |
| OpenCode 复用了 ZCode 特有字段；Roo 配置写入错误入口；CC Switch 导入能力被过度描述 | OpenCode v1 使用 `@ai-sdk/openai-compatible`，完整 limit 才输出；v2/JSONC/自定义路径提示手工处理。Roo/Cursor/Cherry 走应用设置。CC Switch 有独立数据库，写 YAML 不等于导入。 |
| 特色模型只读单条策略，未按选定 Key 的租户确定来源；别名元数据会遗漏 | 新增受管理权限保护的特色目录 API，校验 Key 租户与活动状态，聚合该租户全部策略、去空白去重，通过活动别名取已知窗口，错误时不沿用旧结果。 |
| 失效 Key 可被选择；异步加载与生成结果可能过期；全部模型重试调用错接口 | 过滤 disabled/revoked/expired Key，使用请求序号丢弃旧结果；切换输入和关闭面板清除生成内容；分别管理特色/全部目录错误与重试。 |
| 文件预览显示密钥；JSON 转义后的特殊字符密钥可能避开直接字符串遮蔽 | 遮蔽原始值和 JSON 转义值，真实下载保持可用；手工配置客户端不额外 reveal Key。 |
| 审计缺 `minimax_code`、请求 camelCase 与后端 snake_case 不匹配，JSON 被重复编码为字符串 | 使用统一认证请求通道和 snake_case；校验工具/系统/Key/数量/范围及 Key 租户；直接将 map 写入审计系统，数据库确认 JSONB 为 object。 |
| 构建生成菜单未同步自动配置项；依赖审计有现存漏洞 | 导出菜单加入 `/examples#auto-config`。Vue 升至 3.5.43，source-map-js 固定 1.2.2，同步 npm/pnpm 锁文件；官方 npm 全依赖审计为 0。 |

依赖通告：[Vue 官方安全通告](https://github.com/vuejs/core/security/advisories/GHSA-g2v6-rqmx-r4w6)、[source-map-js 通告](https://github.com/advisories/GHSA-68fv-2mgg-jv7q)。审计数量以实际 npm 返回的 metadata 为证据；网络错误不当作零漏洞。

## 数据复核

通过 `ssh 252` 进入 PostgreSQL 容器 `pg-252-pg17` 只读查询。生产 `routing_policy` 有 4 条策略、一个配置特色模型的租户（default），去重后 28 个 ID；第一条策略目前恰好也包含全部 28 个。聚合修复防止后续策略拆分漏模型，不能声称本次已经因首条策略读取丢失 28 个模型。

`doubao-seed-2-0-code` 的两个活动别名目标上下文窗口均为 NULL；其废弃 preview-260215 目标有 131072。生成器保留特色别名 ID，但不拿废弃目标的窗口补值。只读 SQL fixture 另验证活动别名有窗口时正确返回，并排除废弃别名。`gpt-image-2` 在现有 canonical 数据中标记为 text，本轮保留手工特色配置，未根据名称猜测并删除模型。

选定 Key 184（default）目录返回 28 个模型，与 `.40` ZCode 的配置集合相等；另一租户活动 Key 75 返回空集合，不混入 default；失效 Key 182 返回 404，ID 0 返回 400。上述 ID 仅用于本轮只读目录校验，未修改密钥记录。

## 回归与变异验证

本机与 `.40` 均通过 3 个 Vitest 文件，共 **47 项**：安装器 18、配置面板 7、导航 22。配置文件测试在临时 HOME 中执行真实生成脚本；远端 PyYAML 安装在用户目录，测试显式保留真实模块搜索路径，避免依赖缺失造成错误拒绝的假绿。

```sh
npm --prefix web run test -- src/composables/useClientConfig.test.ts src/components/ClientConfigDialog.test.ts src/config/appNav.test.ts
npm --prefix web run typecheck
CGO_ENABLED=0 go test ./admin -run '^TestClientConfig' -count=1
CGO_ENABLED=0 go vet ./admin
CGO_ENABLED=0 go build ./cmd/gateway
python3 tests/client_config_featured_sql_test.py ssh 252 podman exec -i pg-252-pg17 psql -X -U llm_gateway -d llm_gateway -At -v ON_ERROR_STOP=1
npm audit --prefix web --registry=https://registry.npmjs.org --json
```

Go 专项两个测试通过；实际 Go SQL 文本在 `BEGIN READ ONLY` + CTE fixture 中验证，无真实表写入。桌面与移动前端均完成构建，发布包 index.html 都含 2551 标记。`gofmt -l`（仅改动 Go 文件）和 `git diff --check` 通过。

8 个故意破坏的变异全部触发正确断言：稳定 ID 改为新 ID、取消异址冲突拒绝、取消 JSON 重复键检测、移除 SQL 租户过滤、只取一条策略、活动别名改为废弃别名、取消 JSON 转义密钥遮蔽、全部目录重试改为特色目录重试。初次重复键 fixture 没有真正制造可成功合并的重复输入，曾存活；修正 fixture 后才确认该变异被测试捕获，未以最初结果宣称通过。

## 部署与源码对应

- 部署根：`/Users/huangxt/kaixuan/llm-gateway-go-v2`。
- 部署源码树：`/Users/huangxt/workspace/llm-gateway-go-client-setup-audit-20261012`。
- 构建完整标识：`2.5.8-3c162923-20261011-2551`。日期来自构建脚本的 UTC 口径。
- `run/active-version`、`bin/current` 与唯一网关进程均为 `2.5.8.2551`，端口 8782，无旧版本孤儿网关。
- 原活动 2547、审计中间版本 2550 的发布包保留，可用标准脚本回滚。
- 标准验证：数据库和 Redis 为 external，ready；17 个凭据解密检查失败数 0。
- 部署日志：目标机 `/tmp/client-setup-audit-deploy-final.log`，回归日志 `/tmp/client-setup-audit-final-tests.log`。

用户要求先部署验证再提交，因此二进制的 git_sha 是合并基线 `3c162923`，**不是最终修复提交**。本轮 14 个改动源码/测试/依赖文件与目标机逐项 SHA-256 相等；清单保存于发布包 `client-setup-source-sha256.json`，清单 SHA-256 为 `3ec206c3472dc612f3c7439d99bdad1b59a033c541d84d8cec59d46f071dcae4`。菜单生成时间与文档不纳入源码清单；菜单内容另作发布包检查。最终修复提交应与该清单比对，不把基线 git_sha 错标为含修复的 Git 提交。

构建曾因原运行环境与源码 `.env.local` 不一致、Node 不在 SSH PATH、依赖下载直连超时而中止；均发生在切换前。最终使用原活动版本的既有 runtime env、显式 Node/Go/Python 工具链与独立前端依赖，经标准脚本完整构建发布，没有使用空前端或更改数据库 Key hash 作为绕过措施。部署采用受控重启，不是无中断切换。

## 客户端与请求证据

- 2551 发布后：管理登录及特色目录 API 为 200，`/readyz` 确認 PG/Redis 连通；无鉴权及伪造 `sk-` 请求 `/v1/models` 均为 401。自动配置所需 JS chunk 返回 200，包含新目录端点；公网 `https://llm.itestu.cn` 的 ready/version 返回 200、build_seq=2551。
- 审计请求为 200，`routing_audit_log` 实查 `after_json` 为 object，tool=minimax_code、key_id=184、model_count=28、model_scope=featured，证明不是再次编码的字符串。
- 使用 `.40` ZCode 配置的 Key 直连该机网关：`minimax-m2.7` 原生函数调用 HTTP 200，函数名与 JSON 参数正确，未执行该测试函数；`doubao-seed-2-0-code` 别名流式请求 HTTP 200，35 帧、finish_reason=stop、完整 `[DONE]`，最终文本 OK。首次 256-token 校验未通过后提高测试输出预算重测成功，未据第一次失败修改网关行为。
- 本机 ZCode、MiniMax Code、CC Switch：各自 `itestu` 有 28 模型，URL 为 `https://llm.itestu.cn/v1`。分别读取配置内的 Key，顺序执行模型目录与 `minimax-m2.7` 聊天，均 HTTP 200，去除模型思考段后的最终文本为 OK。旧 `itestuu` 保留；CC Switch 仅一条 `itestuu`，无重复项。
- `.40` ZCode/MiniMax：`itestuu` 各 28 模型、URL 正确。分别强制经已有 HTTPS 代理 `192.168.31.33:7897` 请求模型目录与聊天，HTTP 200、finish_reason=stop、最终 OK。Python 在 macOS 会依据系统 bypass 规则绕过代理；测试显式关闭自动 bypass 来验证指定代理路径，没有关闭 TLS 校验。这不等同于桌面客户端一定采用相同代理规则。
- Cursor：持久状态的 Base URL、Use OpenAI Key 和保存状态正确；前次 Ask/Agent 已有成功记录。本次 UI 控制工具超时，未重做交互验收，不能以其他客户端 API 请求替代 Cursor UI 测试。
- 模型目录曾返回 616、615 项，属于动态目录；验收使用特色集合、状态码和实际响应，不固定目录总数。

## 剩余验收边界

1. `.30` 当前 SSH 主机密钥与 known_hosts 不同。服务器返回 ED25519 指纹 `SHA256:yKYs7AAK9lD5Coatu8rk6FpOa7/IQxu83LYFMc72EXM`；用户提供的 `niuer@niuerdeMacBook-Pro-20261008` 用户公钥指纹为 `SHA256:/ELxtoXXnzabqoBEEMNP3LUkNj9Vh21r+qn6Dz3nIFI`，不是相同主机密钥。等待可信终端核实后继续，没有关闭 SSH 主机验证。
2. Windows PowerShell 脚本构造已检查，但缺真实 Windows 运行环境；不能宣称 Windows 部署已验收。
3. API 代表性请求和原生工具调用不证明全部 28 个模型上游可用，也不证明各桌面应用 UI 均已重测。
4. KxMemory 服务未提供可调用工具，本轮采用本地记忆降级记录，不写入凭据。

操作说明与 28 模型清单见 [客户端自动配置指南](../06-deployment/03-initialization/client-provider-auto-config.md)。

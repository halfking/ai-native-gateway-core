# 客户端模型供应商自动配置

> 更新：2026-10-12。此文记录 ZCode、MiniMax Code、Cursor 与 CC Switch 的配置形态、数据库模型来源、自动配置入口和本轮实测结果。配置文件和生成脚本包含 API Key；不要把生成结果、密钥或数据库凭据提交到仓库。

## 接入方式

| 客户端 | 配置位置 / 入口 | 供应商形态 | 说明 |
|---|---|---|---|
| ZCode | `~/.zcode/v2/config.json`（Windows: `%APPDATA%/zcode/v2/config.json`） | `provider` 映射下的 `openai-compatible` provider | 用 UUID 作为 provider key；以名称 `itestuu` 增量合并，保留其它 provider。 |
| MiniMax Code | `~/.minimax/config.yaml`（Windows: `%USERPROFILE%/.minimax/config.yaml`） | `custom_provider.itestuu`，`api: openai-completions` | OpenAI Chat Completions 兼容模式。更新前检查 `config.yaml.lock`，以锁目录避免覆盖正在写入的配置。 |
| CC Switch | MiniMax Code 标签页 | 读取 MiniMax Code 的 `custom_provider` | CC Switch 会识别 YAML 中的自定义 provider；YAML 是配置真源，不直接改写 `~/.cc-switch/cc-switch.db`。 |
| Cursor | Settings → Models → API Keys | OpenAI API Key + Override OpenAI Base URL + Custom Model | Cursor 没有本流程可安全增量写入的 provider 文件；通过应用设置配置。OpenAI Key 和 Base URL 是全局覆盖项。 |

ZCode provider 的关键字段是 `name: itestuu`、`kind: openai-compatible`、`options.apiKey`、`options.baseURL`、`options.apiKeyRequired: true` 和 `models` 映射。MiniMax Code provider 的关键字段是 `kind: custom`、`enabled: true`、`api: openai-completions`、`options.baseURL`、`options.authMode: api-key`、`options.apiKey` 和 `models`。

配置文件脚本采用同目录临时文件、原子替换和带时间戳的备份；密钥文件权限限制为当前用户可读写（`0600`）。MiniMax Code 脚本会检测同名 provider 并替换该条，同时保留其它 YAML provider。应用已运行时，若模型列表未刷新，先保存工作，再重启客户端使其重新读取配置。

Cursor 的自定义 API Key 只用于标准聊天模型；Tab 补全等需要 Cursor 专用模型的功能继续走 Cursor 自带服务。Cursor 文档还指出模型名冲突时不能重复添加；本机设置页实际拒绝了 `kimi-k3` 和 `claude-sonnet-5` 的同名自定义项，并确认它们分别已作为内置模型存在。特色模型中的部分名称也与 Cursor 内置 Claude、GPT、Gemini、Grok 模型重合；这些条目在选择器可用，不代表请求一定经 OpenAI Base URL 到达 itestuu。因此 Cursor 的 OpenAI 兼容配置不能等同于一个独立、可任意扩展的多供应商配置。见 [Cursor API Keys 文档](https://docs.cursor.com/settings/api-keys)。

## 模型来源

自动配置默认读取数据库 `routing_policy.featured_models` 中 `default` 租户的特色模型集合，并去重后作为客户端模型列表。2026-10-12 查询到 4 条策略记录，内容去重后为以下 28 个 ID：

```text
claude-fable-5-1
claude-opus-4-7
claude-opus-4-8
claude-opus-5-5
claude-sonnet-4-6
claude-sonnet-5
deepseek-v4-flash
deepseek-v4-pro
doubao-1-5-pro-32k
doubao-seed-2-0-code
doubao-seed-2-0-code-preview
gemini-3-flash-preview
gemini-3.5-flash
glm-5.3
glm-5.3-flash
gpt-5.5
gpt-5.6-luna
gpt-5.6-sol
gpt-5.6-terra
gpt-6-astra
gpt-6-luna
gpt-6-sol
gpt-image-2
grok-4.7
kimi-k3
minimax-m2.7
minimax-m2.7-highspeed
minimax-m3
```

其中 27 个 ID 在 `models_canonical` 中有直接记录。`doubao-seed-2-0-code` 是特色列表中的别名，目录中没有同名 canonical 项；存在到 `doubao-seed-2.0-code` 和 `doubao-seed-2-0-code-preview` 的活动别名记录。网关目录列出的名称与特色列表可能因此相差一个；按特色 ID 发起的 Chat Completions 请求已验证可成功解析。模型窗口只从 canonical 元数据读取，数据库未提供的窗口和输出上限不写入配置，避免凭空设置。

## 网关自动配置入口

在侧栏选择 **接入指南 → 自动配置**，或打开接入示例页的“自动配置”锚点。管理员可选择客户端、操作系统、数据库特色模型或活动模型、API Key，以及 API Base URL。默认地址取当前网关站点的 origin 并追加 `/v1`；也可输入其它 HTTP(S) 地址。

生成器按客户端输出 JSON/YAML 配置、可下载的合并脚本和手工步骤。ZCode 与 MiniMax Code 的脚本会合并现有配置并创建备份；Cursor 输出手工 UI 步骤。生成前从密钥 API 读取选定 Key，生成结果包含该 Key，应按凭据保护。审计记录只保存客户端、系统、Key ID、模型数量和模型范围，不记录密钥值。

2026-10-12 验证与部署：本地 `pnpm --dir web typecheck` 通过，两个 Vitest 文件 24 项全通过，`git diff --check` 通过；部署工作树基于 `.40` 当前源码提交 `166e3dfa`，前端 `npm run build` 通过。通过标准 `scripts/deploy-local.sh deploy --no-frontend` 发布 `2.5.8-ee565bb3-20261011-2545`，`VERIFY_PASS=1`，活动版本与运行进程均为 2545、端口 8782，`/readyz` 返回 200。发布包中的 `menu-config.json` 有“自动配置”入口，首页与自动配置 JS 资源均返回 200。

## 实测记录

| 位置 | 结果 |
|---|---|
| 192.168.31.30 | ZCode 与 MiniMax Code 各有 `itestuu` provider，28 个特色模型，配置文件权限均为 `0600`，MiniMax 使用 `openai-completions`。两份配置各自访问 `https://llm.itestu.cn/v1/models` 均为 HTTP 200（网关目录 616 项）；使用 ZCode 与 MiniMax YAML 中的 Key 分别请求 `minimax-m2.7`，Chat Completions 均为 HTTP 200。 |
| 192.168.31.40（原 192.168.31.29，同一台 Mac mini） | ZCode 与 MiniMax Code 各有 `itestuu` provider，28 个特色模型，权限均为 `0600`；MiniMax 旧 `kaixuan` provider 保留，`config.yaml.lock` 不存在。NetBird peer 仍为 `nb-macmini-29` / `100.106.169.63`。活动发布为 `2.5.8.2545`，`run/active-version`、`bin/current` 与进程一致，端口 8782，`/readyz` 为 HTTP 200。ZCode Key 直连 `127.0.0.1:8782` 的 `/models` 与 `minimax-m2.7` 聊天均为 HTTP 200。该机的分流 DNS 将域名解析回自身 NetBird IP，直连 443 超时；系统 HTTPS 代理为 `192.168.31.33:7897`，通过该代理用 MiniMax YAML 中的 Key 请求 `/models` 和 `minimax-m2.7` 均为 HTTP 200，符合桌面应用的系统代理路径。 |
| 本机 MiniMax Code / CC Switch | MiniMax YAML 保留原有 provider 并新增 `itestuu`，包含 28 个特色模型、`openai-completions` 与指定 Base URL，权限为 `0600`。使用该 YAML 的 Key 请求 `/models` 与 `minimax-m2.7` 均为 HTTP 200。解锁后复查 CC Switch，provider 列表可见 `itestuu` 和原有 `kaixuan`、`local`、`minimax-2` 条目；MiniMax Code 的配置文件仍为 YAML 真源。 |
| 本机 ZCode | `~/.zcode/v2/config.json` 中 `itestuu` 有 28 个模型，文件权限为 `0600`；使用配置 Key 请求 `/v1/models` 返回 HTTP 200（616 个目录项），`minimax-m2.7` Chat Completions 返回 HTTP 200。应用内使用 `kaixuan/minimax-m2.7` 发送“Reply exactly OK.”，11 秒后返回 `OK`。 |
| 网关入口 / Cursor | 发布 2545 的首页和自动配置 JS 静态资源均为 HTTP 200，发布目录的 `menu-config.json` 含“接入指南 → 自动配置”。`.30` 经 `https://llm.itestu.cn/v1` 的 `/models` 与聊天请求均为 HTTP 200，证明当前入口可路由到 `.40`。Cursor 持久状态数据库中 `openAIBaseUrl` 为 `https://llm.itestu.cn/v1` 且 `useOpenAIKey=true`；Settings → Models → API Keys 界面确认 API Key 已保存、Use OpenAI API Key 和 Override OpenAI Base URL 均启用。解锁后在 `doubao-seed-2-0-code` 上分别以 Agent 和 Ask 模式发送“Reply exactly OK.”，均得到 `OK`。既有会话曾显示用户 Key 限流提示；新建会话的 Agent/Ask 实测成功。 |

## 回滚与验证

1. JSON/YAML 脚本生成的 `.backup.<timestamp>` 文件保存在原配置目录。回滚前先退出对应客户端或确认没有配置写入，然后将备份原子替换回原路径，并保持 `0600` 权限。
2. 先确认 provider 已出现在客户端模型选择器，再用低 token 的普通聊天请求验证。对于 OpenAI 兼容模式，验证请求必须把模型 ID 精确传给 `/v1/chat/completions`。
3. Cursor 的 BYOK 报错时，先在同一网络路径用直接 API 请求区分客户端提示、网关状态码和模型路由；还要在新建的 Ask/Agent 会话发起短请求，不能把 HTTP 200 的网关探测当作 Cursor UI 已通过。本轮既有会话曾显示限流提示，但新建会话在 Ask/Agent 下均返回 `OK`。
4. 局域网 SSH 使用 `huangxt@192.168.31.40`（`.29` 是该主机旧地址）；252 的网关 upstream 使用该主机稳定的 NetBird peer `100.106.169.63`。`.40` 上 `llm.itestu.cn` 的分流解析回自身 peer，桌面应用需走该机已配置的 HTTPS 系统代理；直连失败与 `invalid_key` 是不同问题。已在 `.30`、`.40` 和本机分别验证网关模型或聊天请求。
5. `401 invalid_key` 会把“未知 Key”和“disabled/revoked/expired”折叠成相同错误；状态字段正常仍不足以证明请求 Key 有效。网关用 `HMAC-SHA256(LLM_GATEWAY_SECRET_KEY, rawKey)` 比对 `api_keys.key_hash`。本轮通过部署切换到 2545 后，实际运行进程与 `run/active-version` 一致，`.30`、`.40` 的客户端 Key 均成功通过模型和聊天请求。多实例共享数据库时，仍应核对实际响应实例的版本和密钥来源，不要只修改单行 `key_hash`。

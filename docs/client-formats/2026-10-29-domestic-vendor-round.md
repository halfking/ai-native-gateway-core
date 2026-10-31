# 国内厂商编码智能体客户端识别（2026-10-29 domestic-vendor round）

> 范围：在 zcode / minimax-code / deepseek-code（2026-09-21 轮）之后，把
> 其余国内大模型厂商的编码智能体工具的特征词接入网关识别链。识别面共
> 六处，一处词汇表、五处消费方，全部在本轮对齐。

## 1. 覆盖的客户端

| 规范名（canonical） | 厂商 / 产品 | 接入依据 | 本地可驱动（agent-companion） |
| --- | --- | --- | --- |
| `codebuddy` | 腾讯 CodeBuddy Code（npm `@tencent-ai/codebuddy-code`） | 腾讯云官方 ACP 集成文档：`codebuddy --acp`（2026-01 CodeBuddy Code 2.0） | ✅ catalog 驱动（ACP stdio） |
| `trae` | 字节 trae-agent（`trae-cli`，github.com/bytedance/trae-agent） | 官方 README：`trae-cli run "<task>"` | ✅ catalog 驱动（CLI headless，PlainParser） |
| `comate` | 百度 Comate / Zulu CLI（npm `@comate/zulu`） | npm 包 + 官网；非交互旗标未验证 → 只识别不驱动 | 检测+安装（detect-only） |
| `qwen-code` | 阿里 Qwen Code（github.com/QwenLM/qwen-code） | 已是 ACC 既有 kind | ✅ 插件 |
| `kimi-cli` | 月之暗面 Kimi CLI（github.com/MoonshotAI/kimi-cli） | 已是 ACC 既有 kind | ✅ 插件 |
| `iflow` | 心流 iFlow CLI（github.com/iflow-ai/iflow-cli） | 已是 ACC catalog 条目（托管服务 2026-04-17 停服，装机客户端仍有流量） | ✅ catalog 驱动 |
| `raccoon` | 商汤 代码小浣熊 Raccoon | 官网确认 CLI 形态存在；二进制名未验证 → 只识别 | ❌（仅识别） |
| `iflycode` | 讯飞 星火飞码 iFlyCode | 官网；仅 IDE 插件，无独立 CLI → 只识别 | ❌（仅识别） |
| `lingma` | 阿里云 通义灵码 TONGYI Lingma（并入 Qoder CN） | 官网/更新日志；IDE 客户端为主 → 只识别 | ❌（仅识别） |

## 2. 六个识别面（本轮全部对齐）

| # | 位置 | 作用 | 本轮改动 |
| --- | --- | --- | --- |
| 1 | `telemetry/request_metadata.go` `defaultAgentPatterns()` | 系统提示词特征词 → 规范名（`DetectAgentFromSystemPrompt`） | +9 条目，全部锚定自述形态（"you are …"/连字 CLI token/CJK 产品名），置于裸 `claude` 兜底之前 |
| 2 | `internal/clienttype/clienttype.go` `Normalize()` | 规范名白名单（Prometheus 标签基数闸门） | +9 规范名；别名折叠（`zulu`→`comate`、`trae-cli`/`trae-agent`→`trae`、`kimi`→`kimi-cli`、`iflow-cli`→`iflow`、`qwen-cli`→`qwen-code`、`code-buddy`→`codebuddy`） |
| 3 | `internal/clienttype/clienttype.go` `DetectFromRequest()` | 头部/UA 识别链（`HasToken` 双边界） | +9 分支（含 `code-buddy`、`trae-cli`、`qwen-cli`、`kimi` 等线材拼写） |
| 4 | `telemetry/request_metadata.go` `ExtractAgentNameFromRequest()` | UA → agent 名（与 #3 同词汇表，两表同修） | +9 分支 |
| 5 | `domains/analysis/sessionmeta/extractor_rules.go` `agentRole()` | 会话侧 role 归类 | +9 入 `coding_agent`（同 R52 minimax/deepseek 教训：缺这里 role 落 other_agent） |
| 6 | `admin/auto_title_generator.go` `detectIDESource()` | 会话标题前缀 | +10 条 "you are …" 自述形态前缀 |

## 3. 契约与红线（沿既有轮次教训）

- **锚定自述形态**：裸产品词会在任意提及它的会话里误命中（09-03
  zcode/opencode、R52 deepseek 同款教训）。`raccoon`/`lingma` 等在系统
  提示词表里只收 "you are …"/连字形态/CJK 产品名（`代码小浣熊`、
  `通义灵码`——CJK 名本身就是最强特征词）。
- **UA 侧 `HasToken` 双边界**：`KimiK2/1.0`（`kimi`+`K`）、
  `CodeBuddyish`、`iFlyCodeX`、`qwen-coded` 等字母数字延续一律拒绝。
- **优先级**：国内条目赢过裸 `claude` 兜底（多个 fork 的提示词会提到
  Claude 推理）；字面 `claude code` 双词属于更早注册的 `claude-code`
  条目，这是既有排序契约（见 `TestAgentSignatureDomesticBeforeClaudeFallback`）。
- **一处词汇表**：规范名与 agent-companion 的 catalog kind 1:1 对齐，
  网关标签与运行时清单对同一工具叫同一个名字。UA 两表 + 别名折叠 +
  role 归类必须同批移动（本轮同批完成）。

## 4. 测试

- `telemetry/agent_signature_domestic_test.go`：9 客户端 × UA 路径 +
  系统提示词路径 + lookalike 反例 + Normalize 恒等 + 裸 claude 兜底
  优先级。
- `domains/analysis/sessionmeta/extractor_domestic_test.go`：role 归类 +
  `normalizeAgent` 端到端（含别名折叠、未知名拒绝）。

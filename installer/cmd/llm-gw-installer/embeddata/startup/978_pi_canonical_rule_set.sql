-- Migration: 978_pi_canonical_rule_set
-- Purpose: 把 PI 检测的 L1/L2 规则集纳入**可投递通道**，消除「环境相关安全姿态」
-- Phase: 1.1 — 数据补种（零建表 / 零列变更 / 零路由变更）
-- Author: System
-- Date: 2026-11-17
--
-- ★ 为什么需要这一件（2026-11-17 245 部署后验收轮实测）：
--   提示注入检测的**规则层**（L1 basic + L2 advanced）的内容来自两个种子迁移：
--   **315**（20 条，含 privilege_escalation / command_injection）与
--   **364**（21 条 enhanced）。而这两件**都不在任何一个建库通道里**：
--
--   ① **全新安装链**（installer/internal/dbinit/runner.go 的 `StartupFiles`
--      ⇒ 机器可读镜像 sql/schema/installed_startup_migrations.tsv）
--      实测：tsv 的 394 行里 prompt-injection 相关**只有 367 一条**
--      （967_prompt_injection_llm_policy_columns.sql）⇒ **315 / 364 都不在**。
--   ② **存量升级通道**（deploy-lib/db-changelog.sh 的
--      `_deploy_pending_startup_migrations`）的投递面 = 「版本 ≥ 对账地板 412
--      全部 + <412 且登记在 scripts/migration-low-version-delivery.list 的少数几条」
--      ⇒ 315 / 364 都 **< 412** 且都**不在**那张白名单里 ⇒ **也不在投递面**。
--   ③ 而且 315 **即使被投递也会炸**：它的规则 INSERT（315 第 70 行）**没有
--      ON CONFLICT**，而它要插的两条 `role_hijack_ignore_previous` /
--      `role_hijack_you_are_now` **正是**检测器 `ensureRulesTable` 预置的那两条
--      ⇒ 唯一键 `prompt_injection_rules_rule_name_key` 冲突 ⇒ 23505 ⇒ 迁移失败。
--      （364 的同一处是 `ON CONFLICT (rule_name) DO NOTHING` ⇒ 幂等，可安全重放。）
--
-- ⇒ 后果（**两条独立实测**，不是推断）：
--   · **全新安装**：`prompt_injection_rules` 的表是 01-schema 基线建出来的
--     （空表 + `id` 默认取自 `prompt_injection_rules_id_seq`），而
--     `ensureRulesTable()` **只在「表不存在」时**才触发（detector.go 的
--     `loadRules` 遇 42P01 才调它）⇒ **表在 ⇒ 永不触发 ⇒ 规则表恒空**。
--     ★ mock 全新安装栈实测（compose lgw-mock-only，库 llm_gateway_mock）：
--       `SELECT count(*) FROM prompt_injection_rules` = **0**。
--   · **存量库（245 活库实测）**：规则表有 **23** 条 = 364 的 21 条 +
--     ensureRulesTable 的 2 条（历史上 364 被投递过）⇒ **缺
--     `privilege_escalation`（sev 9）与 `command_injection`（sev 10）**
--     ⇒ **977 的 F4 对这两条是 no-op**（UPDATE 命中 0 行）⇒
--     977 主档里「L2 规则 pattern 已订正」这句话**只在 tool_abuse_exec 上成立**。
--
-- ⇒ 本件把规则集纳入**可投递通道**（迁移通道 + 安装器链），并让各环境
--   收敛到**同一份** canonical 规则集（25 条）：
--     · 364 的 21 条（**逐字取自 364**，不重新誊写 ⇒ 零转录误差）；
--     · ensureRulesTable 的 2 条 basic（逐字取自 315 / detector.go）；
--     · privilege_escalation / command_injection —— pattern 用 **977 订正后**的
--       字面量（逐字取自 977）⇒ 不把 977 刚修掉的假阳性又种回去。
--   写法 `ON CONFLICT (rule_name) DO NOTHING` ⇒ **幂等**、**不覆盖**既有行、
--   **不删**任何行 ⇒ 在 245 上是纯 no-op（23 条已在场），在全新安装上补齐 25 条。
--
-- ★ 为什么**不**把 315 的其余 18 条（dan_* / bypass_* / instruction_leak_* /
--   role_confusion_* / role_hijack_act_as|pretend）一并种上：
--   ① 977 的 F4 明确把其中若干条列为「**有意留白**」（dan_unrestricted /
--      dan_jailbreak / bypass_rot13 —— 它们本身就是绕过信号，收紧会削弱检测）；
--   ② 315 的 pattern 是**旧形态**（`dan_mode` 等在 977 注释里被逐条判为
--      「交替组本身已正确分组」⇒ 可用，但未逐条复审）；
--   ③ 一次性把 18 条规则投进**所有环境**属于**检测行为变更**，需要自己的审计轮。
--   ⇒ 本件只收敛「已被复审过的那一份」，其余登记为遗留项。
--
-- ★ 锁面：本件只有一条 `INSERT ... ON CONFLICT DO NOTHING`（目标表行数量级
--   为 10^1，无索引重建）⇒ 不触碰任何大表的 AccessExclusiveLock；
--   但仍声明 5s lock_timeout / 10min statement_timeout（与 977 同口径）⇒
--   抢不到锁就快速失败，而不是半应用。
--
-- ============================================================================
-- 0. 事务与超时预算
-- ============================================================================
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

-- ============================================================================
-- 1. 补种 canonical 规则集（幂等）
-- ============================================================================
-- ★ 表不在场 ⇒ 跳过（裸 INSERT 会 42P01 ⇒ 迁移失败 ⇒ 部署 die）：
--   表由 01-schema 基线建出（正常安装链下**一定在**），但「既没跑基线又没跑
--   364」的库可能没有它 ⇒ 判据必须落在迁移里。
DO $$
BEGIN
    IF to_regclass('public.prompt_injection_rules') IS NULL THEN
        RAISE NOTICE '978: public.prompt_injection_rules 不在场 ⇒ 跳过规则集补种（无表可种）';
        RETURN;
    END IF;

    -- ① 364 的 21 条（逐字取自 364_prompt_injection_enhanced.sql 第 309~349 行）
    INSERT INTO public.prompt_injection_rules (rule_name, rule_type, category, category_new, pattern, description, severity, is_system, tags) VALUES
    ('role_hijack_ignore_previous', 'basic', 'role_hijack', NULL, '(?i)(ignore|forget|disregard).*(previous|above|prior).*(instruction|prompt|rule)', '尝试让模型忽略之前的指令', 9, true, NULL),
    ('role_hijack_you_are_now', 'basic', 'role_hijack', NULL, '(?i)you are now (a|an) .*(admin|root|system|god mode|developer)', '尝试切换模型角色为特权用户', 10, true, NULL),
    ('privilege_escalation', 'advanced', 'role_hijack', NULL, '(?i)(grant|give).*\badmin\b|\bsudo\b\s+(-\S+\s+)*\S+|\broot\b[\s,;:_-]+(admin|administrator|access|privilege|privileges|permission|permissions|shell|account|user|users|password|passwd|login|console|rights|role)\b|\b(as|become|becomes|becoming|gain|gains|gaining|obtain|obtains|obtaining|escalate|escalates|escalating|elevate|elevates|elevating|promote|promotes|promoted|switch|switching|run|running)\s+(to\s+)?root\b', '尝试权限提升', 9, true, NULL),
    ('command_injection', 'advanced', 'bypass', NULL, '(?i)\b(exec|eval)\s*\(|\b(system|shell)\(|os\s*\.\s*system\s*\(|\bshell_exec\s*\(', '尝试命令注入', 10, true, NULL),
('instruction_override_ignore', 'basic', 'bypass', 'instruction_override', '(?i)(ignore|forget|disregard|skip|bypass|neglect|override).*(previous|above|prior|all|original).*(instruction|prompt|rule|directive|guideline)', '尝试忽略或覆盖之前的指令', 9, true, ARRAY['override', 'ignore']),
('instruction_override_new_rules', 'basic', 'bypass', 'instruction_override', '(?i)(new|different|updated).*(rules|instructions|guidelines).*(are|is).*(as follows|below)', '尝试注入新规则', 8, true, ARRAY['override', 'rules']),
('instruction_override_system_prompt', 'advanced', 'bypass', 'injection_marker', '(?i)(system|assistant)\s*(prompt|message|instruction)\s*[:=]', '尝试注入系统提示标记', 9, true, ARRAY['marker', 'system']),
('data_exfiltration_http', 'advanced', 'bypass', 'data_exfiltration', '(?i)(send|post|upload|exfiltrate).*(to|via|through).*(http|https|url|endpoint|webhook)', '尝试通过HTTP外泄数据', 10, true, ARRAY['exfiltration', 'http']),
('data_exfiltration_tool', 'advanced', 'bypass', 'data_exfiltration', '(?i)(call|invoke|use|execute).*(tool|function|api).*(send|leak|expose|transmit)', '尝试通过工具调用外泄数据', 10, true, ARRAY['exfiltration', 'tool']),
('resource_exhaustion_repeat', 'basic', 'bypass', 'resource_exhaustion', '(?i)(repeat|loop|iterate).*(1000|10000|infinite|unlimited|forever)', '尝试资源耗尽攻击', 6, true, ARRAY['dos', 'repeat']),
('resource_exhaustion_long', 'basic', 'bypass', 'resource_exhaustion', '(?i)(write|generate|create).*(10000|100000|very long|extremely long).*(words|characters|tokens)', '尝试生成超长内容', 5, true, ARRAY['dos', 'length']),
('social_engineering_urgency', 'basic', 'bypass', 'social_engineering', '(?i)(urgent|emergency|critical|immediately|right now|asap).*(override|bypass|ignore)', '利用紧急性绕过限制', 7, true, ARRAY['social', 'urgency']),
('social_engineering_authority', 'basic', 'bypass', 'social_engineering', '(?i)(as|I am).*(admin|root|developer|CEO|manager|supervisor).*(override|bypass|grant)', '伪装权威身份', 8, true, ARRAY['social', 'authority']),
('unicode_homoglyph', 'advanced', 'bypass', 'unicode_obfuscation', '[\x{0430}-\x{044F}\x{0410}-\x{041F}]', '检测西里尔字母混淆（拉丁/西里尔视觉相似）', 8, true, ARRAY['unicode', 'homoglyph']),
('unicode_rtl_override', 'advanced', 'bypass', 'unicode_obfuscation', '[\x{202E}\x{202D}\x{200F}\x{200E}]', '检测RTL/LTR覆盖字符', 7, true, ARRAY['unicode', 'rtl']),
('unicode_zero_width', 'advanced', 'bypass', 'unicode_obfuscation', '[\x{200B}\x{200C}\x{200D}\x{FEFF}\x{2060}]', '检测零宽字符', 7, true, ARRAY['unicode', 'invisible']),
-- ★ 2026-11-18 12h 审计轮根修（P1）：tool_abuse_exec 的 pattern 用 **977 订正后**
--   的字面量，而不是 364 的旧形态。
--   旧形态 `(?i)(exec|eval|system|subprocess|os\.system|child_process).*(\(|call|invoke)`
--   里 `system` + **无界** `.*` ⇒ 普通叙述句「the system will call you back」即命中
--   （severity 10）⇒ 合法流量被误判。
--   ★ 为什么这不是「抄写疏忽」而是**新装必现**的缺陷：
--     本件（978）登记序在 977（376 → 377）**之后**；全新安装时规则表是空的
--     （315/364 都不在任何建库通道里 ⇒ 977 的 UPDATE 命中 0 行）⇒
--     977 的订正**根本没落地** ⇒ 978 再把旧形态种回去 ⇒
--     **每次全新部署都会把这条假阳性规则装上**。
--     （存量库上 245 已有该行 ⇒ 978 的 `ON CONFLICT DO NOTHING` 不覆盖 ⇒ 不显现
--      ⇒ 这正是「存量不红、新装必红」的形状。）
--   ⇒ 逐字取自 977 的订正后字面量（与 977 F4③ 同一份）：
('tool_abuse_exec', 'advanced', 'bypass', 'tool_abuse', '(?i)\b(exec|eval)\s*\(|\b(system|shell)\(|os\s*\.\s*system\s*\(|\bshell_exec\s*\(|\b(subprocess|child_process)\b\s*[.(]|\b(call|invoke|execute|run|spawn)\s+(the\s+)?(subprocess|child_process)\b', '尝试执行系统命令', 10, true, ARRAY['tool', 'exec']),
('tool_abuse_import', 'advanced', 'bypass', 'tool_abuse', '(?i)(import|require|load|include).*(os|subprocess|sh|shell|sys)', '尝试导入危险模块', 9, true, ARRAY['tool', 'import']),
('tool_abuse_file', 'advanced', 'bypass', 'tool_abuse', '(?i)(read|write|delete|modify|access).*(file|filesystem|/etc/|/var/|/tmp/)', '尝试文件系统操作', 9, true, ARRAY['tool', 'file']),
('multi_turn_setup', 'advanced', 'bypass', 'multi_turn_attack', '(?i)(in the next|remember this for|when I say|next time I ask).*(message|prompt|turn|request)', '多轮攻击设置阶段', 6, true, ARRAY['multi_turn', 'setup']),
('multi_turn_reference', 'advanced', 'bypass', 'multi_turn_attack', '(?i)(as you agreed|you promised|remember when|like before|from earlier).*(ignore|bypass|override)', '多轮攻击引用阶段', 7, true, ARRAY['multi_turn', 'reference']),
('payload_smuggling_base64', 'advanced', 'bypass', 'payload_smuggling', '(?i)(decode|decrypt|extract|unpack).*(base64|b64|hex|rot13|cipher)', '尝试通过编码走私Payload', 8, true, ARRAY['smuggling', 'encoding']),
('payload_smuggling_markdown', 'advanced', 'bypass', 'payload_smuggling', '(?i)```[\s\S]*(ignore|override|system|jailbreak)[\s\S]*```', '尝试通过Markdown代码块走私', 7, true, ARRAY['smuggling', 'markdown']),
('context_manipulation_role', 'advanced', 'role_hijack', 'context_manipulation', '(?i)(from now on|starting now|new conversation|reset context|clear memory)', '尝试操纵对话上下文', 7, true, ARRAY['context', 'reset']),
('context_manipulation_persona', 'advanced', 'role_hijack', 'context_manipulation', '(?i)(your name is|you are called|call yourself|identity is now)', '尝试操纵AI身份', 8, true, ARRAY['context', 'identity'])
    ON CONFLICT (rule_name) DO NOTHING;
END;
$$;

-- ============================================================================
-- 2. 台账自登记（≥952 纪律）
-- ============================================================================
INSERT INTO public.schema_migrations (version, description)
VALUES ('978', 'pi_canonical_rule_set: 把 PI L1/L2 规则集纳入可投递通道（364 的 21 条 + 2 条 basic + 977 订正后的 privilege_escalation/command_injection，幂等补种）')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;

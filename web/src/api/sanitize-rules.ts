import { req, type RequestOptions } from "./_core";
export const RULE_TYPES = [
  "phone",
  "id_card",
  "email",
  "credit_card",
  "secret",
  "internal_ip",
  "server_ip",
  "account",
  "name",
  "custom",
] as const;
export const RULE_KINDS = [
  "pattern",
  "vendor_key_shape",
  "credential_declaration",
  "credential_assignment",
  "credential_pair",
  "account_declaration",
  "account_assignment",
  "network_destination",
  "review_combination",
] as const;
export const RULE_EXEMPTIONS = [
  "inert_tool",
  "nonroutable_destination",
  "identity",
] as const;
export type RulePhase = "input" | "output" | "egress";
export type RuleAction = "mask" | "block" | "log";
export type ReviewStatus =
  | "pending"
  | "confirmed_sensitive"
  | "false_positive"
  | "ignored";
export interface RuleSample {
  text: string;
  match: boolean;
}
export interface SensitiveRule {
  id: string;
  name: string;
  /**
   * 规则说明（判什么形状、命中意味着什么）。
   *
   * ★ 2026-10-29 输出合规误报轮：★ 说明的事实源是 YAML + 内建目录 ⇒ ★★ 服务端
   *   在读规则时补齐（★ 历史规则快照里没有说明）★★。
   */
  description?: string;
  type: (typeof RULE_TYPES)[number];
  kind: (typeof RULE_KINDS)[number];
  pattern: string;
  group: number;
  scopes: RulePhase[];
  action: RuleAction;
  enabled: boolean;
  priority: number;
  tenant_id?: string;
  tools?: string[];
  fields?: string[];
  samples?: RuleSample[];
  exemptions?: (typeof RULE_EXEMPTIONS)[number][];
}
export interface RuleConfig {
  rules: SensitiveRule[];
}
export interface RuleInstance {
  instance_id: string;
  version: number;
  config_digest: string;
  last_seen: string;
  last_refresh: string;
  error: string;
}
export interface RuleCurrent {
  version: number;
  config: RuleConfig;
  config_digest: string;
  status: RuleInstance;
}
export interface RuleVersion {
  id: number;
  parent_id: number;
  rollback_of?: number;
  config: RuleConfig;
  actor: string;
  comment: string;
  created_at: string;
  review_links?: { id: number; tenant_id: string }[];
}
export interface MatchedRuleSummary {
  id: string;
  name?: string;
  type?: string;
  kind?: string;
  pattern?: string;
  group?: number;
  scopes?: RulePhase[];
  action?: RuleAction;
  enabled?: boolean;
  priority?: number;
  tenant_id?: string;
  samples?: RuleSample[];
  missing?: boolean;
}
export interface RuleEvent {
  id: number;
  fingerprint: string;
  tenant_id: string;
  rule_id: string;
  version: number;
  phase: RulePhase;
  action: RuleAction | "exempted" | "pending";
  status: ReviewStatus;
  type?: string;
  field?: string;
  sample_masked: string;
  count: number;
  first_seen: string;
  last_seen: string;
  reviewer?: string;
  review_comment?: string;
  reviewed_at?: string;
  resolved_version?: number;
  matched_rule?: MatchedRuleSummary;
}
export interface RulePublish {
  base_version: number;
  config: RuleConfig;
  comment: string;
  review_event_ids?: number[];
}
const root = "/api/admin/sanitize-rules";
export const getSanitizeRules = (options?: RequestOptions) =>
  req<RuleCurrent>("GET", root, undefined, options);
export const validateSanitizeRules = (config: RuleConfig) =>
  req<{ valid: boolean; config_digest: string }>(
    "POST",
    `${root}/validate`,
    config,
  );
export const publishSanitizeRules = (body: RulePublish) =>
  req<RuleVersion>("POST", `${root}/publish`, body);
export const rollbackSanitizeRules = (body: {
  base_version: number;
  target_version: number;
  comment: string;
}) => req<RuleVersion>("POST", `${root}/rollback`, body);
export const listSanitizeRuleVersions = (options?: RequestOptions) =>
  req<{ versions: RuleVersion[] }>(
    "GET",
    `${root}/versions`,
    undefined,
    options,
  );
export function listSanitizeRuleEvents(
  filter: { action?: string; status?: string; limit?: number } = {},
  options?: RequestOptions,
) {
  const query = new URLSearchParams();
  if (filter.action) query.set("action", filter.action);
  if (filter.status) query.set("status", filter.status);
  query.set("limit", String(filter.limit ?? 100));
  return req<{ events: RuleEvent[] }>(
    "GET",
    `${root}/events?${query}`,
    undefined,
    options,
  );
}
export const reviewSanitizeRuleEvent = (
  id: number,
  body: { status: Exclude<ReviewStatus, "pending">; comment: string },
) => req<RuleEvent>("POST", `${root}/events/${id}/review`, body);
export const listSanitizeRuleInstances = (options?: RequestOptions) =>
  req<{ target_version: number; instances: RuleInstance[] }>(
    "GET",
    `${root}/instances`,
    undefined,
    options,
  );

/** 硬阻断清单条目（哪些规则允许终止回合）。 */
export interface HardBlockRule {
  scope: RulePhase;
  rule: string;
}
/** 放行记录（持久化轮 2026-10-29：含来源与有效期）。 */
export interface EgressReleaseRecord {
  tenant_id: string;
  scope: string;
  rule: string;
  payload_digest?: string;
  client_type?: string;
  approved_by?: string;
  source?: "manual" | "review";
  expires_at?: string;
}
/**
 * 拦截审核记录（一次 output/egress 硬阻断的待审单）。
 *
 * ★ 2026-10-29 输出合规误报轮补：session_id（原始会话内容的入口）、
 * label/label_*（人工标注：这次拦得对不对）、evidence/evidence_kind/
 * evidence_truncated（被拦内容的**有界摘录** —— 原始会话内容）。
 */
/**
 * 审核单的**原始上下文**与**归因**（2026-10-29 输出合规误报轮）。
 *
 * ★ 为什么需要它：★ 审核单自带「为什么」的字段（rule / reason / field /
 *   digest）全靠拦截点自报 ⇒ ★★ 历史记录这四个字段全空 ⇒ ★★ 审核面既没有
 *   「为什么」也没有「拦了什么」⇒ ★★ 审核员无法审批 ⇒ ★★ 服务端在**读时**
 *   联请求日志补上这些（★ 不落库：917 的契约是审核表不含载荷原文）★★。
 */
export interface BlockReviewCandidateRule {
  rule_id: string;
  action?: string;
  status?: string;
  count?: number;
  first_seen?: string;
  last_seen?: string;
}
export interface BlockReviewContext {
  /** 这次请求的原始上下文（请求体预览，有界）。 */
  request_preview?: string;
  /** 响应预览（★ 多数被拦请求为空：响应在落库前就被拦住了）。 */
  response_preview?: string;
  model?: string;
  session_id?: string;
  error_kind?: string;
  failure_stage?: string;
  failure_detail_code?: string;
  prompt_tokens?: number;
  completion_tokens?: number;
  latency_ms?: number;
  client_profile?: string;
  /**
   * 归因类别：rule（审核单自带规则）/ guard（守卫侧拦截，非内容规则命中）/
   * unknown（判不了，只给候选）。
   */
  attribution_kind?: "rule" | "guard" | "unknown";
  /** 归因结论（人话）。 */
  attribution?: string;
  /** 邻近时段命中的出站规则（★ 候选，不是事实）。 */
  candidate_rules?: BlockReviewCandidateRule[];
}
export interface BlockReview {
  id: number;
  tenant_id: string;
  client_type?: string;
  scope: string;
  rule: string;
  rule_name?: string;
  reason?: string;
  field?: string;
  digest?: string;
  request_id?: string;
  match_detail?: string;
  status: "pending" | "approved" | "rejected";
  created_at: string;
  decided_by?: string;
  decided_at?: string;
  session_id?: string;
  label?: BlockReviewLabel;
  label_comment?: string;
  labeled_by?: string;
  labeled_at?: string;
  evidence?: string;
  evidence_kind?: string;
  evidence_truncated?: boolean;
  /**
   * ★ 降级放行标记（迁移 926，2026-10-30 审计轮）：这条记录对应的是
   * 「守卫缓冲越界 ⇒ 放行并留痕」，不是硬阻断 —— 响应已成功送达客户端，
   * 没有批准/拒绝/标注可做，渲染成只读遥测行；也不计入待审数。
   */
  degraded?: boolean;
  /** ★ 原始上下文与归因（★ 读时补齐；★ 补不齐时整块缺失）。 */
  context?: BlockReviewContext;
}

/**
 * 人工标注的闭集。
 *
 * ★ 与审核状态（pending/approved/rejected）**正交**：状态回答「现在放不放行」，
 * 标注回答「这次拦得对不对」—— 后者是规则优化的唯一输入。
 */
export type BlockReviewLabel =
  | "false_positive"
  | "true_positive"
  | "rule_too_broad"
  | "needs_rule";

/** 按规则聚合的复核读数（规则优化的输入面）。 */
export interface BlockReviewRuleInsight {
  rule: string;
  rule_name?: string;
  tenant_id?: string;
  total: number;
  pending: number;
  approved: number;
  rejected: number;
  false_positive: number;
  true_positive: number;
  rule_too_broad: number;
  needs_rule: number;
  labeled: number;
  false_positive_ratio: number;
  auto_relaxed: boolean;
  auto_relax_until?: string;
}

/** 输出合规总开关的读数（2026-10-29 输出合规误报轮）。 */
export interface OutputCheckSwitch {
  key: string;
  scope: string;
  enabled: boolean;
  source: string;
  default: boolean;
  semantics: string;
  description?: string;
  description_long?: string;
  hot_reload?: boolean;
}
export const getHardBlockRules = (options?: RequestOptions) =>
  req<{ rules: HardBlockRule[]; note: string }>(
    "GET",
    `${root}/hardblock`,
    undefined,
    options,
  );
export const updateHardBlockRules = (body: {
  rules?: string[];
  preset?: "all" | "none";
  comment?: string;
  confirm_disable_all?: boolean;
}) =>
  req<{ rules: HardBlockRule[]; comment: string }>(
    "PUT",
    `${root}/hardblock`,
    body,
  );
export function listBlockedReviews(
  filter: { status?: string; limit?: number } = {},
  options?: RequestOptions,
) {
  const query = new URLSearchParams();
  if (filter.status) query.set("status", filter.status);
  query.set("limit", String(filter.limit ?? 100));
  return req<{
    reviews: BlockReview[];
    releases: EgressReleaseRecord[];
    process_only: boolean;
  }>("GET", `${root}/blocked-reviews?${query}`, undefined, options);
}
export const decideBlockedReview = (
  id: number,
  body: { decision: "approve" | "reject"; comment?: string },
) => req<BlockReview>("POST", `${root}/blocked-reviews/${id}/review`, body);

/**
 * 给一条审核单打人工标注（2026-10-29 输出合规误报轮）。
 *
 * ★ 与 approve/reject 正交：标 false_positive / rule_too_broad 时，
 *   同租户同规则的误报标注累计到阈值 ⇒ 服务端自动写入该租户该规则的 7 天放行。
 */
export const labelBlockedReview = (
  id: number,
  body: { label: BlockReviewLabel; comment?: string },
) => req<BlockReview>("POST", `${root}/blocked-reviews/${id}/label`, body);

/** 按规则聚合的复核读数（规则优化的输入面）。 */
export function listBlockReviewInsights(
  filter: { tenant_id?: string; limit?: number } = {},
  options?: RequestOptions,
) {
  const query = new URLSearchParams();
  if (filter.tenant_id) query.set("tenant_id", filter.tenant_id);
  query.set("limit", String(filter.limit ?? 50));
  return req<{
    insights: BlockReviewRuleInsight[];
    auto_relax_threshold: number;
    auto_relax_enabled: boolean;
    tenant_id?: string;
  }>("GET", `${root}/blocked-reviews/insights?${query}`, undefined, options);
}

/** 输出合规总开关的读数。 */
export const getOutputCheckSwitch = (options?: RequestOptions) =>
  req<OutputCheckSwitch>("GET", `${root}/output-check`, undefined, options);

/** 输出合规总开关的写入（写的是同一个设置键，不另立真值）。 */
export const updateOutputCheckSwitch = (body: {
  enabled: boolean;
  comment?: string;
}) => req<OutputCheckSwitch>("PUT", `${root}/output-check`, body);

/** Validate editor JSON shapes before rendering it; RE2/sample semantics stay on the server. */
export function isRuleConfig(value: unknown): value is RuleConfig {
  if (
    !value ||
    typeof value !== "object" ||
    !("rules" in value) ||
    !Array.isArray(value.rules)
  )
    return false;
  return value.rules.every((r: unknown) => {
    if (!r || typeof r !== "object") return false;
    const rule = r as Record<string, unknown>;
    return (
      ["id", "name", "pattern", "type", "kind", "action"].every(
        (key) => typeof rule[key] === "string",
      ) &&
      typeof rule.enabled === "boolean" &&
      typeof rule.group === "number" &&
      typeof rule.priority === "number" &&
      Array.isArray(rule.scopes) &&
      rule.scopes.every((x) =>
        ["input", "output", "egress"].includes(String(x)),
      ) &&
      ["tools", "fields", "exemptions"].every(
        (key) =>
          rule[key] === undefined ||
          (Array.isArray(rule[key]) &&
            rule[key].every((x) => typeof x === "string")),
      ) &&
      (rule.tenant_id === undefined || typeof rule.tenant_id === "string") &&
      (rule.samples === undefined ||
        (Array.isArray(rule.samples) &&
          rule.samples.every(
            (x) =>
              x &&
              typeof x === "object" &&
              typeof x.text === "string" &&
              typeof x.match === "boolean",
          )))
    );
  });
}

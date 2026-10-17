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

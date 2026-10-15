import { req, type RequestOptions } from './client'

// 脱敏规则只读面；h.admin 只认证，操作权限由 handler 再判断。
// 权限依据：admin/sanitize_rules_handler.go:32-38；租户由服务端从认证上下文取，禁止拼 tenant_id。
// 两种 Store 的列表均初始化为 []；Compile 拒绝 nil/empty rules，不能把坏响应伪装成空集。
// current/实例可能仍用本地 fallback 的版本 0，历史记录的 id 则必须为正值。

export interface SanitizeRule {
  id: string
  name: string
  type: string
  kind: string
  pattern: string
  group: number
  scopes: string[]
  action: string
  enabled: boolean
  priority: number
  tenant_id?: string
  tools?: string[]
  fields?: string[]
  samples?: { text: string; match: boolean }[]
  exemptions?: string[]
}

export interface SanitizeRuleConfig {
  rules: SanitizeRule[]
}

export interface SanitizeRuleInstance {
  instance_id: string
  version: number
  config_digest: string
  last_seen: string
  last_refresh: string
  error: string
}

export interface SanitizeRuleCurrent {
  version: number
  config: SanitizeRuleConfig
  config_digest: string
  status: SanitizeRuleInstance
}

export interface SanitizeRuleVersion {
  id: number
  parent_id: number
  rollback_of?: number
  config: SanitizeRuleConfig
  actor: string
  comment: string
  created_at: string
  review_links?: { id: number; tenant_id: string }[]
}

export interface SanitizeRuleEvent {
  id: number
  fingerprint: string
  tenant_id: string
  rule_id: string
  version: number
  phase: string
  action: string
  type?: string
  field?: string
  status: string
  sample_masked: string
  count: number
  first_seen: string
  last_seen: string
  reviewer?: string
  review_comment?: string
  reviewed_at?: string
  resolved_version?: number
}

export function sanitizeRulesCanManage(role: string | null): boolean {
  return role === 'super_admin' || role === 'admin_key'
}

export function sanitizeRulesCanReadEvents(role: string | null, tenantID: string | null): boolean {
  return sanitizeRulesCanManage(role) || (role === 'tenant_admin' && tenantID !== null && tenantID !== '')
}

const root = '/api/admin/sanitize-rules'
const phases = ['input', 'output', 'egress']
const ruleActions = ['mask', 'block', 'log']
const eventActions = [...ruleActions, 'exempted', 'pending']
const eventStatuses = ['pending', 'confirmed_sensitive', 'false_positive', 'ignored']
const ruleTypes = ['phone', 'id_card', 'email', 'credit_card', 'secret', 'internal_ip', 'server_ip', 'account', 'name', 'custom']
const ruleKinds = ['pattern', 'review_combination', 'network_destination', 'vendor_key_shape', 'credential_declaration', 'credential_assignment', 'credential_pair', 'account_declaration', 'account_assignment']
const exemptions = ['inert_tool', 'nonroutable_destination', 'identity']

type Row = Record<string, unknown>
type Check = (value: unknown) => boolean
const object = (value: unknown): value is Row => value !== null && typeof value === 'object' && !Array.isArray(value)
const string: Check = (value) => typeof value === 'string'
// int/int64 一律要求安全整数；拒绝 JSON 解析后已失真的大整数，不假装精确。
const integer: Check = (value) => typeof value === 'number' && Number.isSafeInteger(value)
const nonnegative: Check = (value) => integer(value) && (value as number) >= 0
const positive: Check = (value) => integer(value) && (value as number) > 0
const oneOf = (values: readonly string[]): Check => (value) => typeof value === 'string' && values.includes(value)
const arrayOf = (check: Check): Check => (value) => Array.isArray(value) && value.every(check)
const strings = (row: Row, keys: string[]) => keys.every((key) => string(row[key]))
// omitempty 字段可以缺键；存在（包括 null）时必须验证，不能静默丢弃坏字段/坏行。
const optional = (row: Row, key: string, check: Check) => !(key in row) || check(row[key])

const isRule: Check = (value) => object(value)
  && strings(value, ['id', 'name', 'pattern'])
  && oneOf(ruleTypes)(value.type) && oneOf(ruleKinds)(value.kind)
  && nonnegative(value.group) && integer(value.priority)
  && arrayOf(oneOf(phases))(value.scopes) && (value.scopes as unknown[]).length > 0
  && oneOf(ruleActions)(value.action) && typeof value.enabled === 'boolean'
  && optional(value, 'tenant_id', string)
  && ['tools', 'fields'].every((key) => optional(value, key, arrayOf(string)))
  && optional(value, 'exemptions', arrayOf(oneOf(exemptions)))
  && optional(value, 'samples', arrayOf((sample) => object(sample) && string(sample.text) && typeof sample.match === 'boolean'))

const isConfig: Check = (value) => object(value) && arrayOf(isRule)(value.rules) && (value.rules as unknown[]).length > 0
const isInstance: Check = (value) => object(value)
  && strings(value, ['instance_id', 'config_digest', 'last_seen', 'last_refresh', 'error'])
  && nonnegative(value.version)
const isCurrent: Check = (value) => object(value) && nonnegative(value.version)
  && isConfig(value.config) && string(value.config_digest) && isInstance(value.status)
const isVersion: Check = (value) => object(value) && positive(value.id) && nonnegative(value.parent_id)
  && isConfig(value.config) && strings(value, ['actor', 'comment', 'created_at'])
  && optional(value, 'rollback_of', nonnegative)
  && optional(value, 'review_links', arrayOf((link) => object(link) && positive(link.id) && string(link.tenant_id)))
const isEvent: Check = (value) => object(value)
  && positive(value.id) && nonnegative(value.version) && positive(value.count)
  && strings(value, ['fingerprint', 'tenant_id', 'rule_id', 'sample_masked', 'first_seen', 'last_seen'])
  && oneOf(phases)(value.phase) && oneOf(eventActions)(value.action) && oneOf(eventStatuses)(value.status)
  // type/field 等展示字符串只检查类型；不把自由文本/结构标签误当筛选枚举。
  && ['type', 'field', 'reviewer', 'review_comment', 'reviewed_at'].every((key) => optional(value, key, string))
  && optional(value, 'resolved_version', nonnegative)

async function read<T>(path: string, check: Check, options?: RequestOptions): Promise<T> {
  let value: unknown
  try {
    value = await req<unknown>('GET', path, undefined, options)
  } catch (error) {
    // client 的 JSON.parse 失败也属于坏响应；HTTP/网络/取消错误仍原样交给视图。
    if (error instanceof SyntaxError) throw new Error('sanitizeRules.invalidResponse')
    throw error
  }
  if (!check(value)) throw new Error('sanitizeRules.invalidResponse')
  return value as T
}

export function fetchSanitizeRulesCurrent(options?: RequestOptions): Promise<SanitizeRuleCurrent> {
  return read(root, isCurrent, options)
}

export function fetchSanitizeRuleVersions(options?: RequestOptions): Promise<{ versions: SanitizeRuleVersion[] }> {
  // 不携带 limit：handler 的缺省值就是 100。
  return read(`${root}/versions`, (value) => object(value) && arrayOf(isVersion)(value.versions), options)
}

export function fetchSanitizeRuleEvents(
  filter: { action?: string; status?: string; limit?: number } = {},
  options?: RequestOptions,
): Promise<{ events: SanitizeRuleEvent[] }> {
  const query = new URLSearchParams()
  for (const [key, allowed] of [['action', eventActions], ['status', eventStatuses]] as const) {
    const value = filter[key]
    if (value !== undefined && value !== '') {
      if (!oneOf(allowed)(value)) return Promise.reject(new Error('sanitizeRules.invalidFilter'))
      query.set(key, value)
    }
  }
  const limit = filter.limit === undefined ? 100 : filter.limit
  if (!positive(limit) || limit > 1000) return Promise.reject(new Error('sanitizeRules.invalidFilter'))
  query.set('limit', String(limit))
  return read(`${root}/events?${query}`, (value) => object(value) && arrayOf(isEvent)(value.events), options)
}

export function fetchSanitizeRuleInstances(options?: RequestOptions): Promise<{ target_version: number; instances: SanitizeRuleInstance[] }> {
  return read(`${root}/instances`, (value) => object(value) && nonnegative(value.target_version) && arrayOf(isInstance)(value.instances), options)
}

import { req, type RequestOptions } from './client'

// 脱敏规则四 GET + 草稿验证/事件复核 POST；h.admin 只认证，操作权限由 handler 再判断。
// 权限依据：admin/sanitize_rules_handler.go:32-38；验证仅管理员，tenant_admin 有租户时可读/复核事件。
// 验证依据：admin/sanitize_rules_handler.go:54-67、284-301；RE2/规则语义交给服务端 Compile。
// 复核依据：admin/sanitize_rules_handler.go:201-238、284-301；tenant/reviewer 由服务端决定，禁止透传。
// 发布/回滚依据：admin/sanitize_rules_handler.go:68-91、131-173、260-270；manager.go:164-199；仅 super_admin/admin_key。
// publish: base_version>0、comment非空≤2000字节、review_event_ids正整数且≤100去重；rollback: target>0、comment非空≤2000字节。
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
  config_digest?: string
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

// 仅草稿请求严格限定 JSON 键；GET 响应的扩展字段行为保持不变。
// domains/sanitizerules/types.go:18-41
const ruleKeys = ['id', 'name', 'type', 'kind', 'pattern', 'group', 'scopes', 'action', 'enabled', 'priority', 'tenant_id', 'tools', 'fields', 'samples', 'exemptions']
const onlyKeys = (row: Row, keys: readonly string[]) => Object.keys(row).every((key) => keys.includes(key))
const isDraft: Check = (value) => isConfig(value) && object(value) && onlyKeys(value, ['rules'])
  && (value.rules as Row[]).every((rule) => onlyKeys(rule, ruleKeys)
    && optional(rule, 'samples', arrayOf((sample) => object(sample) && onlyKeys(sample, ['text', 'match']))))

export async function validateSanitizeRules(
  config: SanitizeRuleConfig,
  options?: RequestOptions,
): Promise<{ valid: true; config_digest: string }> {
  let snapshot: unknown
  try {
    if (!isDraft(config)) throw new Error('invalid draft')
    const json = JSON.stringify(config)
    // 检查实际 JSON 的 UTF-8 字节（含转义/结构），且在传输前脱离调用方可变对象。
    if (new TextEncoder().encode(json).length > (2 << 20)) throw new Error('oversized draft')
    snapshot = JSON.parse(json)
    if (!isDraft(snapshot)) throw new Error('invalid serialized draft')
  } catch {
    throw new Error('sanitizeRules.invalidDraft')
  }
  let value: unknown
  try {
    value = await req<unknown>('POST', `${root}/validate`, snapshot, options)
  } catch (error) {
    if (error instanceof SyntaxError) throw new Error('sanitizeRules.invalidResponse')
    throw error
  }
  if (!object(value) || value.valid !== true || typeof value.config_digest !== 'string'
    || value.config_digest.length !== 64 || !/^[0-9a-f]{64}$/.test(value.config_digest)) throw new Error('sanitizeRules.invalidResponse')
  return value as { valid: true; config_digest: string }
}

// 仅 publish/rollback 请求体严格限定 JSON 键；GET 响应的扩展字段行为保持不变。
// domains/sanitizerules/types.go:18-41、manager.go:164-199
const isString: Check = (value) => typeof value === 'string'
const isComment: Check = (value) => isString(value) && (value as string).trim() !== '' && new TextEncoder().encode(value as string).length <= 2000
const isPublishBody: Check = (value) => {
  if (!object(value)) return false
  const row = value as Row
  if (!positive(row.base_version) || !isDraft(row.config) || !isComment(row.comment)) return false
  if (Object.prototype.hasOwnProperty.call(row, 'review_event_ids') && !isReviewEvents(row.review_event_ids)) return false
  if (!onlyKeys(row, ['base_version', 'config', 'comment', 'review_event_ids'])) return false
  return true
}
const isRollbackBody: Check = (value) => {
  if (!object(value)) return false
  const row = value as Row
  if (!positive(row.base_version) || !positive(row.target_version) || !isComment(row.comment)) return false
  return onlyKeys(row, ['base_version', 'target_version', 'comment'])
}
const isReviewEvents: Check = (value) => {
  if (!Array.isArray(value) || value.length > 100) return false
  const seen = new Set<number>()
  for (const id of value) {
    if (!positive(id) || seen.has(id)) return false
    seen.add(id)
  }
  return true
}

async function writeVersion(path: string, body: unknown, check: (value: unknown) => boolean, options?: RequestOptions): Promise<SanitizeRuleVersion> {
  let snapshot: unknown
  try {
    if (!check(body)) throw new Error('invalid publish body')
    snapshot = JSON.parse(JSON.stringify(body))
    if (!check(snapshot)) throw new Error('invalid serialized body')
  } catch {
    throw new Error('sanitizeRules.invalidPublish')
  }
  let value: unknown
  try {
    value = await req<unknown>('POST', path, snapshot, options)
  } catch (error) {
    if (error instanceof SyntaxError) throw new Error('sanitizeRules.invalidResponse')
    throw error
  }
  if (!isVersion(value) || !object(value) || !nonnegative((value as Row).parent_id)) throw new Error('sanitizeRules.invalidResponse')
  return value as unknown as SanitizeRuleVersion
}

export function publishSanitizeRules(
  body: { base_version: number; config: SanitizeRuleConfig; comment: string; review_event_ids?: number[] },
  options?: RequestOptions,
): Promise<SanitizeRuleVersion> {
  return writeVersion(`${root}/publish`, body, isPublishBody as Check, options)
}

export function rollbackSanitizeRules(
  body: { base_version: number; target_version: number; comment: string },
  options?: RequestOptions,
): Promise<SanitizeRuleVersion> {
  return writeVersion(`${root}/rollback`, body, isRollbackBody as Check, options)
}

export async function reviewSanitizeRuleEvent(
  id: number,
  body: { status: 'confirmed_sensitive' | 'false_positive' | 'ignored'; comment: string },
  options?: RequestOptions,
): Promise<SanitizeRuleEvent> {
  if (!positive(id) || !object(body)
    || !oneOf(['confirmed_sensitive', 'false_positive', 'ignored'])(body.status)
    || !string(body.comment)) throw new Error('sanitizeRules.invalidReview')
  // Go len(comment) 计 UTF-8 字节而非 JS UTF-16 长度；空评论合法。
  // domains/sanitizerules/events.go:32-39
  const { status, comment } = body
  if (new TextEncoder().encode(comment).length > 2000) throw new Error('sanitizeRules.invalidReview')
  let value: unknown
  try {
    // 白名单构造请求体；未知 tenant_id/reviewer 等字段不会进入服务端严格 decoder。
    value = await req<unknown>('POST', `${root}/events/${id}/review`, { status, comment }, options)
  } catch (error) {
    if (error instanceof SyntaxError) throw new Error('sanitizeRules.invalidResponse')
    throw error
  }
  // 不以另一行或未生效的旧状态替换视图中的目标事件。
  if (!isEvent(value) || !object(value) || value.id !== id || value.status !== status) {
    throw new Error('sanitizeRules.invalidResponse')
  }
  return value as unknown as SanitizeRuleEvent
}

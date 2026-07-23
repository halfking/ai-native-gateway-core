<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { RoutingCandidate } from '../../api/routing'

const { t } = useI18n()

const props = defineProps<{
  candidate: RoutingCandidate
}>()

const emit = defineEmits<{
  close: []
}>()

interface FlagRow {
  key: string
  label: string
  value: string | number | boolean | null
  severity: 'ok' | 'warn' | 'danger' | 'info'
  description: string
  source: string
}

const fmtTime = (iso: string | null | undefined): string => {
  if (!iso) return '—'
  return new Date(iso).toLocaleString()
}

const isActive = (s: string | null | undefined): boolean => !!s && s === 'active'
const isReady = (s: string | null | undefined): boolean => !!s && s === 'ready'
const isOk = (s: string | null | undefined): boolean => !!s && (s === 'ok' || s === 'closed')
const isOpen = (s: string | null | undefined): boolean => !!s && s === 'open'

const severityFor = (s: string | null | undefined, okValues: string[], warnValues: string[] = []): FlagRow['severity'] => {
  if (!s) return 'info'
  if (okValues.includes(s)) return 'ok'
  if (warnValues.includes(s)) return 'warn'
  return 'danger'
}

const availabilityFlags = computed<FlagRow[]>(() => [
  {
    key: 'available',
    label: '可用（综合）',
    value: props.candidate.available ? '可用' : '不可用',
    severity: props.candidate.available ? 'ok' : 'danger',
    description: '依据视图 v_routable_credential_models.is_routable 综合判定，规则涵盖 credential_status / lifecycle_status / availability_state / quota_state / circuit_state 五维',
    source: 'v_routable_credential_models.is_routable',
  },
  {
    key: 'runtime_routable',
    label: '运行时可路由',
    value: props.candidate.runtime_routable ? '是' : '否',
    severity: props.candidate.runtime_routable ? 'ok' : 'danger',
    description: '凭据维度的运行时可达性（包含进程内粘性 / 并发锁等运行约束）',
    source: 'cmb.runtime_routable',
  },
  {
    key: 'block_reason',
    label: '阻断原因',
    value: props.candidate.runtime_block_reason || props.candidate['block_reason' as keyof RoutingCandidate] || '—',
    severity: props.candidate.runtime_block_reason ? 'danger' : 'ok',
    description: '不可路由的根因代码（如 quota_exhausted / circuit_open / lifecycle_disabled / credential_disabled 等）',
    source: 'v_routable_credential_models.unavailable_reason',
  },
])

const credentialFlags = computed<FlagRow[]>(() => [
  {
    key: 'credential_status',
    label: '凭据状态',
    value: props.candidate.credential_status || '—',
    severity: isActive(props.candidate.credential_status) ? 'ok' : 'danger',
    description: '凭据本身的启用状态：active=启用，其它值表示已禁用、过期等',
    source: 'credentials.status',
  },
  {
    key: 'lifecycle_status',
    label: '生命周期',
    value: props.candidate.lifecycle_status || '—',
    severity: isActive(props.candidate.lifecycle_status) ? 'ok' : 'danger',
    description: '凭据生命周期阶段：active=在用；其它值表示已弃用 / 测试 / 退役',
    source: 'credentials.lifecycle_status',
  },
  {
    key: 'availability_state',
    label: '可用性状态',
    value: props.candidate.availability_state || '—',
    severity: isReady(props.candidate.availability_state) ? 'ok' : 'danger',
    description: '后台健康检查输出的状态：ready=就绪；cooling=冷却中；rate_limited=限流；unreachable=不可达；suspended=暂停',
    source: 'credentials.availability_state',
  },
  {
    key: 'availability_recover_at',
    label: '可用性恢复时间',
    value: fmtTime(props.candidate.availability_recover_at),
    severity: 'info',
    description: '预计从非 ready 状态自动恢复的时间戳',
    source: 'credentials.availability_recover_at',
  },
  {
    key: 'provider_enabled',
    label: 'Provider 启用',
    value: props.candidate.provider_enabled ? '是' : '否',
    severity: props.candidate.provider_enabled ? 'ok' : 'danger',
    description: '供应商是否处于启用状态（影响所有该 Provider 下的凭据）',
    source: 'providers.enabled',
  },
])

const quotaFlags = computed<FlagRow[]>(() => [
  {
    key: 'quota_state',
    label: '配额状态',
    value: props.candidate.quota_state || '—',
    severity: isOk(props.candidate.quota_state) ? 'ok' : (props.candidate.quota_state === 'low' ? 'warn' : 'danger'),
    description: '凭据配额使用情况：ok=正常；low=偏低；exhausted=耗尽；over=超限',
    source: 'credentials.quota_state',
  },
  {
    key: 'quota_recover_at',
    label: '配额恢复时间',
    value: fmtTime(props.candidate.quota_recover_at),
    severity: 'info',
    description: '配额恢复的预计时间（一般为下一个计费周期开始）',
    source: 'credentials.quota_recover_at',
  },
  {
    key: 'balance_usd',
    label: '余额 (USD)',
    value: props.candidate.balance_usd ?? '—',
    severity: Number(props.candidate.balance_usd) > 5 ? 'ok' : (Number(props.candidate.balance_usd) > 0 ? 'warn' : 'danger'),
    description: '凭据账户当前可用余额（来自上次账单同步）',
    source: 'credentials.balance_usd',
  },
  {
    key: 'quota_cap_usd',
    label: '配额上限 (USD)',
    value: props.candidate.quota_cap_usd ?? '—',
    severity: 'info',
    description: '本计费周期的总配额上限',
    source: 'model_offers.unit_price_in_per_1m',
  },
  {
    key: 'quota_used_usd',
    label: '已用配额 (USD)',
    value: props.candidate.quota_used_usd ?? '—',
    severity: 'info',
    description: '本计费周期已消耗配额',
    source: 'model_offers.unit_price_out_per_1m',
  },
])

const circuitFlags = computed<FlagRow[]>(() => [
  {
    key: 'circuit_state',
    label: '熔断器',
    value: props.candidate.circuit_state || 'closed',
    severity: isOk(props.candidate.circuit_state) ? 'ok' : 'danger',
    description: '熔断器状态机：closed=正常；open=已熔断（拒绝请求）；half_open=探测恢复',
    source: 'credentials.circuit_state',
  },
  {
    key: 'cooling_until',
    label: '冷却至',
    value: fmtTime(props.candidate.cooling_until),
    severity: 'info',
    description: '熔断器冷却截止时间（过此时刻后会自动切换到 half_open 探测）',
    source: 'credentials.cooling_until',
  },
])

const concurrencyFlags = computed<FlagRow[]>(() => [
  {
    key: 'concurrency_limit',
    label: '并发上限',
    value: props.candidate.concurrency_limit ?? '—',
    severity: 'info',
    description: '凭据配置的最大并发请求数（NULL 表示不限制）',
    source: 'credentials.concurrency_limit',
  },
  {
    key: 'effective_concurrency',
    label: '当前并发',
    value: props.candidate.effective_concurrency ?? '—',
    severity: (props.candidate.effective_concurrency ?? 0) > 0 ? 'warn' : 'ok',
    description: '当前正在并发执行的请求数（用于判断是否打满）',
    source: 'credentials.effective_concurrency',
  },
  {
    key: 'active_sessions',
    label: '活跃会话',
    value: props.candidate.active_sessions ?? 0,
    severity: 'info',
    description: '该凭据当前承载的活跃聊天会话数',
    source: 'cmb.active_sessions',
  },
])

const effectFlags = computed<FlagRow[]>(() => [
  {
    key: 'effective_at',
    label: '生效时间',
    value: fmtTime(props.candidate.effective_at),
    severity: 'info',
    description: '凭据生效起始时间（NULL 表示立即生效）',
    source: 'credentials.effective_at',
  },
  {
    key: 'expires_at',
    label: '过期时间',
    value: fmtTime(props.candidate.expires_at),
    severity: 'info',
    description: '凭据到期时间（NULL 表示永不过期）',
    source: 'credentials.expires_at',
  },
  {
    key: 'credential_in_effect',
    label: '当前在生效窗口',
    value: props.candidate['credential_in_effect' as keyof RoutingCandidate] ? '是' : '否',
    severity: props.candidate['credential_in_effect' as keyof RoutingCandidate] ? 'ok' : 'warn',
    description: 'effective_at ≤ NOW() < expires_at 时为 true',
    source: 'derived(now() BETWEEN effective_at AND expires_at)',
  },
])

const billingFlags = computed<FlagRow[]>(() => [
  {
    key: 'billing_mode',
    label: '计费模式',
    value: props.candidate.billing_mode || 'per_token',
    severity: 'info',
    description: '当前凭据的计费模式（per_token / code_plan / token_plan / monthly / agent_plan / free）',
    source: 'cmb.billing_mode',
  },
  {
    key: 'billing_round',
    label: '计费轮次',
    value: props.candidate.billing_round ?? '—',
    severity: 'info',
    description: 'P2C 计费轮次（1=单轮 token；2=包月轮）',
    source: 'provider.BillingRound()',
  },
  {
    key: 'unit_price_in',
    label: '输入价 (per 1M)',
    value: props.candidate.unit_price_in_per_1m ?? '—',
    severity: 'info',
    description: '输入 token 单价（每 1M tokens）',
    source: 'model_offers.unit_price_in_per_1m',
  },
  {
    key: 'unit_price_out',
    label: '输出价 (per 1M)',
    value: props.candidate.unit_price_out_per_1m ?? '—',
    severity: 'info',
    description: '输出 token 单价（每 1M tokens）',
    source: 'model_offers.unit_price_out_per_1m',
  },
  {
    key: 'currency',
    label: '货币',
    value: props.candidate.currency || 'USD',
    severity: 'info',
    description: '计价货币',
    source: 'cmb.currency',
  },
])

const scoreFlags = computed<FlagRow[]>(() => [
  {
    key: 'composite_score',
    label: '综合得分',
    value: props.candidate.composite_score?.toFixed(1) ?? '—',
    severity: (props.candidate.composite_score ?? 0) >= 70 ? 'ok' : ((props.candidate.composite_score ?? 0) >= 40 ? 'warn' : 'danger'),
    description: 'P2C 综合评分（基于价格 / 速度 / 稳定性 / 匹配度 / 压力 / 上下文匹配 6 维加权计算）',
    source: 'executors.CalculateCompositeScore',
  },
  {
    key: 'manual_priority',
    label: '人工优先级',
    value: props.candidate.manual_priority ?? 99,
    severity: 'info',
    description: '人工设定的优先级（数字越小越优先，默认 99）',
    source: 'cmb.manual_priority',
  },
  {
    key: 'tier',
    label: 'Tier',
    value: `T${props.candidate.tier}`,
    severity: 'info',
    description: '路由分级（1=首选，2=次选，数字越大越靠后；free tier 固定为 9）',
    source: 'cmb.routing_tier',
  },
  {
    key: 'weight',
    label: '权重',
    value: props.candidate.weight ?? 100,
    severity: 'info',
    description: '同 tier 内权重（数字越大越优先）',
    source: 'cmb.weight',
  },
  {
    key: 'success_rate',
    label: '成功率',
    value: `${((props.candidate.success_rate ?? 0) * 100).toFixed(1)}%`,
    severity: (props.candidate.success_rate ?? 0) >= 0.95 ? 'ok' : ((props.candidate.success_rate ?? 0) >= 0.8 ? 'warn' : 'danger'),
    description: '历史请求成功率（窗口由健康检查模块维护）',
    source: 'cmb.success_rate',
  },
  {
    key: 'p95_latency_ms',
    label: 'P95 延迟',
    value: props.candidate.p95_latency_ms ? `${props.candidate.p95_latency_ms}ms` : '—',
    severity: (props.candidate.p95_latency_ms ?? 9999) <= 2000 ? 'ok' : ((props.candidate.p95_latency_ms ?? 9999) <= 5000 ? 'warn' : 'danger'),
    description: 'P95 请求延迟（毫秒）',
    source: 'model_offers.p95_latency_ms',
  },
  {
    key: 'consecutive_failures',
    label: '连续失败',
    value: props.candidate.consecutive_failures ?? 0,
    severity: (props.candidate.consecutive_failures ?? 0) === 0 ? 'ok' : ((props.candidate.consecutive_failures ?? 0) >= 3 ? 'danger' : 'warn'),
    description: '连续失败次数（用于触发熔断）',
    source: 'cmb.consecutive_failures',
  },
])
</script>

<template>
  <Teleport to="body">
    <div class="cd-overlay" @click.self="emit('close')">
      <aside class="cd-drawer" role="dialog" :aria-label="`凭据 #${candidate.credential_id} 状态明细`">
        <header class="cd-head">
          <div class="cd-head-main">
            <h3>
              凭据 #{{ candidate.credential_id }}
              <span class="cd-head-label">{{ candidate.credential_label }}</span>
            </h3>
            <p class="cd-head-sub">
              {{ candidate.provider_name }} · {{ candidate.model_name }}
            </p>
          </div>
          <button class="cd-close" type="button" aria-label="关闭" @click="emit('close')">×</button>
        </header>
        <div class="cd-body">
          <section class="cd-group">
            <h4 class="cd-group-title">可用性</h4>
            <ul class="cd-flag-list">
              <li v-for="row in availabilityFlags" :key="row.key" class="cd-flag" :data-severity="row.severity">
                <span class="cd-flag-label">{{ row.label }}</span>
                <span class="cd-flag-value" :class="['cd-sev-' + row.severity]">{{ row.value }}</span>
                <div class="cd-flag-meta">
                  <div><strong>说明：</strong>{{ row.description }}</div>
                  <div><strong>来源：</strong><code>{{ row.source }}</code></div>
                </div>
              </li>
            </ul>
          </section>

          <section class="cd-group">
            <h4 class="cd-group-title">凭据 / Provider 状态</h4>
            <ul class="cd-flag-list">
              <li v-for="row in credentialFlags" :key="row.key" class="cd-flag" :data-severity="row.severity">
                <span class="cd-flag-label">{{ row.label }}</span>
                <span class="cd-flag-value" :class="['cd-sev-' + row.severity]">{{ row.value }}</span>
                <div class="cd-flag-meta">
                  <div><strong>说明：</strong>{{ row.description }}</div>
                  <div><strong>来源：</strong><code>{{ row.source }}</code></div>
                </div>
              </li>
            </ul>
          </section>

          <section class="cd-group">
            <h4 class="cd-group-title">配额</h4>
            <ul class="cd-flag-list">
              <li v-for="row in quotaFlags" :key="row.key" class="cd-flag" :data-severity="row.severity">
                <span class="cd-flag-label">{{ row.label }}</span>
                <span class="cd-flag-value" :class="['cd-sev-' + row.severity]">{{ row.value }}</span>
                <div class="cd-flag-meta">
                  <div><strong>说明：</strong>{{ row.description }}</div>
                  <div><strong>来源：</strong><code>{{ row.source }}</code></div>
                </div>
              </li>
            </ul>
          </section>

          <section class="cd-group">
            <h4 class="cd-group-title">熔断</h4>
            <ul class="cd-flag-list">
              <li v-for="row in circuitFlags" :key="row.key" class="cd-flag" :data-severity="row.severity">
                <span class="cd-flag-label">{{ row.label }}</span>
                <span class="cd-flag-value" :class="['cd-sev-' + row.severity]">{{ row.value }}</span>
                <div class="cd-flag-meta">
                  <div><strong>说明：</strong>{{ row.description }}</div>
                  <div><strong>来源：</strong><code>{{ row.source }}</code></div>
                </div>
              </li>
            </ul>
          </section>

          <section class="cd-group">
            <h4 class="cd-group-title">并发 / 会话</h4>
            <ul class="cd-flag-list">
              <li v-for="row in concurrencyFlags" :key="row.key" class="cd-flag" :data-severity="row.severity">
                <span class="cd-flag-label">{{ row.label }}</span>
                <span class="cd-flag-value" :class="['cd-sev-' + row.severity]">{{ row.value }}</span>
                <div class="cd-flag-meta">
                  <div><strong>说明：</strong>{{ row.description }}</div>
                  <div><strong>来源：</strong><code>{{ row.source }}</code></div>
                </div>
              </li>
            </ul>
          </section>

          <section class="cd-group">
            <h4 class="cd-group-title">时效</h4>
            <ul class="cd-flag-list">
              <li v-for="row in effectFlags" :key="row.key" class="cd-flag" :data-severity="row.severity">
                <span class="cd-flag-label">{{ row.label }}</span>
                <span class="cd-flag-value" :class="['cd-sev-' + row.severity]">{{ row.value }}</span>
                <div class="cd-flag-meta">
                  <div><strong>说明：</strong>{{ row.description }}</div>
                  <div><strong>来源：</strong><code>{{ row.source }}</code></div>
                </div>
              </li>
            </ul>
          </section>

          <section class="cd-group">
            <h4 class="cd-group-title">计费</h4>
            <ul class="cd-flag-list">
              <li v-for="row in billingFlags" :key="row.key" class="cd-flag" :data-severity="row.severity">
                <span class="cd-flag-label">{{ row.label }}</span>
                <span class="cd-flag-value" :class="['cd-sev-' + row.severity]">{{ row.value }}</span>
                <div class="cd-flag-meta">
                  <div><strong>说明：</strong>{{ row.description }}</div>
                  <div><strong>来源：</strong><code>{{ row.source }}</code></div>
                </div>
              </li>
            </ul>
          </section>

          <section class="cd-group">
            <h4 class="cd-group-title">评分 / 优先级</h4>
            <ul class="cd-flag-list">
              <li v-for="row in scoreFlags" :key="row.key" class="cd-flag" :data-severity="row.severity">
                <span class="cd-flag-label">{{ row.label }}</span>
                <span class="cd-flag-value" :class="['cd-sev-' + row.severity]">{{ row.value }}</span>
                <div class="cd-flag-meta">
                  <div><strong>说明：</strong>{{ row.description }}</div>
                  <div><strong>来源：</strong><code>{{ row.source }}</code></div>
                </div>
              </li>
            </ul>
          </section>
        </div>
      </aside>
    </div>
  </Teleport>
</template>

<style scoped>
.cd-overlay {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.45);
  z-index: 70;
  display: flex;
  justify-content: flex-end;
}
.cd-drawer {
  width: min(560px, 100vw);
  height: 100%;
  background: var(--kx-bg-container);
  color: var(--kx-text-primary);
  box-shadow: -12px 0 32px rgba(0, 0, 0, 0.16);
  display: flex;
  flex-direction: column;
}
.cd-head {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  padding: 14px 16px;
  border-bottom: 1px solid var(--kx-border-light);
}
.cd-head h3 {
  margin: 0;
  font-size: 15px;
  display: flex;
  align-items: baseline;
  gap: 8px;
}
.cd-head-label {
  font-size: 12px;
  color: var(--kx-text-secondary);
  font-weight: 400;
}
.cd-head-sub {
  margin: 4px 0 0;
  font-size: 11px;
  color: var(--kx-text-secondary);
}
.cd-close {
  margin-left: auto;
  border: none;
  background: transparent;
  font-size: 22px;
  line-height: 1;
  cursor: pointer;
  color: var(--kx-text-secondary);
}
.cd-close:hover {
  color: var(--kx-text-primary);
}
.cd-body {
  overflow-y: auto;
  padding: 14px 16px 24px;
}
.cd-group {
  margin-bottom: 18px;
}
.cd-group-title {
  margin: 0 0 8px;
  font-size: 12px;
  font-weight: 600;
  color: var(--kx-text-secondary);
  text-transform: uppercase;
  letter-spacing: 0.05em;
}
.cd-flag-list {
  list-style: none;
  margin: 0;
  padding: 0;
}
.cd-flag {
  display: grid;
  grid-template-columns: minmax(120px, 30%) 1fr;
  gap: 8px;
  padding: 8px 10px;
  margin-bottom: 6px;
  background: var(--kx-bg-elevated);
  border-radius: 6px;
  border: 1px solid var(--kx-border-light);
}
.cd-flag-label {
  font-size: 12px;
  color: var(--kx-text-secondary);
}
.cd-flag-value {
  font-size: 12px;
  font-weight: 500;
  text-align: right;
  font-variant-numeric: tabular-nums;
}
.cd-sev-ok {
  color: var(--kx-success);
}
.cd-sev-warn {
  color: var(--kx-warning);
}
.cd-sev-danger {
  color: var(--kx-danger);
}
.cd-sev-info {
  color: var(--kx-text-secondary);
}
.cd-flag-meta {
  grid-column: 1 / -1;
  font-size: 11px;
  color: var(--kx-text-secondary);
  line-height: 1.55;
  border-top: 1px dashed var(--kx-border-light);
  padding-top: 6px;
  margin-top: 4px;
}
.cd-flag-meta strong {
  color: var(--kx-text-primary);
}
.cd-flag-meta code {
  font-size: 10.5px;
  background: var(--kx-bg-base);
  padding: 1px 4px;
  border-radius: 3px;
}
</style>
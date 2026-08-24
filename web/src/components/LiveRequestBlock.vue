<script setup lang="ts">
// LiveRequestBlock — single tile in the swim lane.
//
// 2026-07-03 v7: dynamic tile width + failure detail layout.
//
//   ┌────────────┐           ┌────────────┐
//   │ 14:35      │  time     │ 14:35      │
//   │  GPT       │  model    │  5XX       │ ← failure: error_kind label
//   │  OPEN      │  provider │  GPT       │ ← failure: model demoted
//   │ 1.2s       │  latency  │ 1.2s       │
//   └════════════┘           └════════════┘
//      (success / in_progress)   (failure with error_kind)
//
//   width: dynamic (60-130px) computed by LiveRequestStream from
//   the track width. Larger widths give the operator more
//   characters per tile without horizontal scroll. The width is
//   applied via :style so the parent can stretch a single tile
//   when there is spare room in the swim lane.
//
// Failure detail: when status==='failure' AND error_kind is
// present, the model's row is REPLACED by a short, colored
// error_kind label (e.g. "5xx", "timeout", "disc", "rate"). This
// surfaces the failure mode directly on the swim lane — the
// operator no longer needs to hover for a tooltip to know
// whether a red tile was upstream 5xx or a client timeout.
import { computed } from 'vue'
import { getRequestCredentialId, requestCredentialRevision } from '../composables/liveStreamStore'
import { credentialDisplayName } from '../composables/useCredentialLabels'
import { useI18n } from 'vue-i18n'
import type { LiveRequest } from '../composables/useLiveStream'
import {
  modelShortLabel,
  providerShortLabel,
  hexToRgba,
  latencyLabel,
  timeHHMM,
  errorKindLabel,
  errorKindBg,
} from '../composables/liveStreamDisplay'
import {
  getModelCategoryColor,
  STATUS_BORDER_COLORS,
  STATUS_BORDER_WIDTHS,
} from '../composables/liveStreamColors'

const props = defineProps<{
  request: LiveRequest
  /**
   * Optional explicit tile width in px. The parent (LiveRequestStream)
   * computes a value in [60, 130] from the track width and passes it
   * down. When omitted we fall back to a 64px default so unit tests
   * and Storybook snapshots do not need to know the layout.
   */
  width?: number
}>()

const emit = defineEmits<{
  /** Fired when the user clicks a real (non-idle) tile. */
  select: [requestId: string]
}>()

const { locale } = useI18n()

const isIdle = computed(() => props.request.type === 'idle_marker')
const isFailure = computed(() => props.request.status === 'failure' && !!props.request.error_kind)
const isProbe = computed(() => props.request.is_probe === true)

// Idle markers occupy ~2 tile widths so they read as a heartbeat
// gap rather than as a real request in the swim lane.
const idleStyle = computed(() => ({
  width: props.width ? `${props.width * 2 + 4}px` : '132px',
}))

const idleLabel = computed(() => {
  if (!isIdle.value) return ''
  const start = Date.parse(props.request.ts)
  if (Number.isNaN(start)) return '—'
  const minutes = Math.max(1, Math.round((Date.now() - start) / 60_000))
  return `Idle ${minutes}m`
})

// Line 1: HH:MM start time (locale-aware).
const timeLabel = computed(() => timeHHMM(props.request.ts, locale.value))

// Line 2: full model name (success / in_progress) or the
// coarse error_kind label (failure). When the failure has no
// recognisable error_kind, we fall back to the model name so the
// tile still shows the model that was attempted.
const errorLabel = computed(() => errorKindLabel(props.request.error_kind))
const vendorLabel = computed(() => props.request.model || '???')

// Line 3: full provider name (small, muted). Always shown.
const providerLabel = computed(() => props.request.provider_code || '???')

// Line 4: latency.
const latencyText = computed(() =>
  latencyLabel(props.request.latency_ms ?? null, props.request.status === 'in_progress'),
)

/**
 * Background colour:
 *  - failure with known error_kind → errorKindBg (translucent red /
 *    amber / yellow / purple by family)
 *  - success / in_progress        → family colour at 22% alpha
 *  - failure without error_kind   → family colour (so the model
 *    context is not lost)
 */
const tileBg = computed(() => {
  if (isFailure.value) return errorKindBg(props.request.error_kind)
  const c = getModelCategoryColor(props.request.model_category)
  return hexToRgba(c, 0.22)
})

const statusBorder = computed(() =>
  STATUS_BORDER_COLORS[props.request.status ?? 'in_progress'] ||
  STATUS_BORDER_COLORS.in_progress,
)

const statusBorderWidth = computed(() =>
  STATUS_BORDER_WIDTHS[props.request.status ?? 'in_progress'] ||
  STATUS_BORDER_WIDTHS.in_progress,
)

const isPulsing = computed(() => props.request.status === 'in_progress')
// 2026-08-23 凭据显示：依赖 requestCredentialRevision 让生命周期事件
//（如 node_switch）触发后立即重算凭据名称，否则 tooltip 会停留在
//挂载时的过期 ID。
const credentialName = computed(() => {
  void requestCredentialRevision.value
  return credentialDisplayName(getRequestCredentialId(props.request.request_id || ''))
})

/**
 * Multi-line native tooltip. Native `title` is chosen over a custom
 * popover because the swim lane lives inside an overflow:hidden
 * track — a popover would clip at the right viewport edge — and
 * because screen readers already announce native title.
 *
 * Failure case: the first line is the error_kind label so an
 * operator hovering the red tile sees "upstream_5xx" before the
 * other context lines.
 */
const tooltip = computed(() => {
  const r = props.request
  const lines: string[] = []
  if (r.status === 'failure' && r.error_kind) {
    lines.push(`Error: ${r.error_kind}`)
  }
  if (isProbe.value) {
    lines.push(`Probe: ${r.probe_origin || 'scheduled'}${r.probe_attempt ? ` #${r.probe_attempt}` : ''}`)
  }
  if (r.model) lines.push(`Model: ${r.model}`)
  if (r.provider_code) lines.push(`Provider: ${r.provider_code}`)
  const credentialId = getRequestCredentialId(r.request_id || '')
  if (credentialId != null) lines.push(`Credential: ${credentialName.value}`)
  lines.push(`Status: ${r.status ?? '?'}`)
  if (r.latency_ms != null) {
    lines.push(`Latency: ${latencyText.value}`)
  }
  if (r.total_tokens != null) {
    lines.push(`Tokens: ${r.total_tokens}`)
  } else if (r.prompt_tokens != null || r.completion_tokens != null) {
    const p = r.prompt_tokens ?? 0
    const c = r.completion_tokens ?? 0
    lines.push(`Tokens: ${p}+${c}`)
  }
  if (r.cost_usd != null) lines.push(`Cost: $${r.cost_usd.toFixed(4)}`)
  if (r.request_id) lines.push(`ID: ${r.request_id.slice(0, 8)}`)
  try {
    lines.push(`Time: ${new Date(r.ts).toLocaleString(locale.value)}`)
  } catch {
    /* ignore */
  }
  return lines.join('\n')
})

function onClick() {
  if (!isIdle.value && props.request.request_id) {
    emit('select', props.request.request_id)
  }
}
</script>

<template>
  <!-- Idle marker: wide dashed pill (~2 tiles wide) so 2 minutes of
       silence still reads as a heartbeat indicating "the system is
       alive but has no traffic" rather than as a real request. The
       width is supplied by the parent (LiveRequestStream). -->
  <div
    v-if="isIdle"
    class="live-block live-block--idle"
    :style="idleStyle"
    :title="idleLabel"
    aria-hidden="true"
  >
    <span class="live-block__idle-text">{{ idleLabel }}</span>
  </div>

  <!-- Real tile: 4 lines + status border + tile bg.
       Width is supplied by the parent (LiveRequestStream) via the
       `width` prop. The body background switches to a translucent
       failure-family colour (red / amber / yellow / purple) when
       the request failed, and the line-2 row is replaced with the
       coarse error_kind label so the operator can read the failure
       mode WITHOUT hovering. -->
  <div
    v-else
    class="live-block"
    :class="{
      'live-block--clickable': !!request.request_id,
      'live-block--in-progress': isPulsing,
      'live-block--failure': isFailure,
    }"
    role="button"
    tabindex="0"
    :aria-label="tooltip"
    :title="tooltip"
    :style="{
      background: tileBg,
      borderColor: statusBorder,
      borderWidth: statusBorderWidth + 'px',
      width: (width ?? 64) + 'px',
    }"
    @click="onClick"
    @keyup.enter="onClick"
    @keyup.space.prevent="onClick"
  >
    <span
      v-if="isProbe"
      class="live-block__probe"
      :title="`探测请求 · ${request.probe_origin || 'scheduled'}${request.probe_attempt ? ` #${request.probe_attempt}` : ''}`"
      aria-label="探测请求"
    >
      <svg viewBox="0 0 16 16" aria-hidden="true">
        <circle cx="8" cy="8" r="5.5" fill="none" stroke="currentColor" stroke-width="1.4" />
        <circle cx="8" cy="8" r="1.6" fill="currentColor" />
        <path d="M8 1.5v2.2M8 12.3v2.2M1.5 8h2.2M12.3 8h2.2" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" />
      </svg>
    </span>
    <span class="live-block__time">{{ timeLabel }}</span>
    <!-- Failure mode: line 2 shows the error_kind label so the
         operator sees the failure mode WITHOUT hovering. -->
    <span
      v-if="isFailure"
      class="live-block__error-kind"
      :title="request.error_kind || ''"
    >{{ errorLabel }}</span>
    <span v-else class="live-block__vendor">{{ vendorLabel }}</span>
    <!-- Failure mode: line 3 demoted to the model short label so
         the operator still sees WHICH model failed. -->
    <span
      v-if="isFailure"
      class="live-block__vendor"
      :title="request.model || ''"
    >{{ vendorLabel }}</span>
    <span v-else class="live-block__provider">{{ credentialName !== '—' ? credentialName : providerLabel }}</span>
    <!-- Latency is always the last line. The corner badge is
         intentionally suppressed in failure mode because the line-2
         label already tells the failure story; keeping the badge
         would be visual noise on top of the red border. -->
    <span class="live-block__latency">{{ latencyText }}</span>
  </div>
</template>

<style scoped>
/* 4-row tile (width: dynamic 60-130, height: 76) — v7 dark-mode.
 *
 *   width  dynamic ← supplied by parent via :style; this stylesheet
 *                   only sets the default for unit tests / Storybook
 *   height 76px    ← 4 rows × 14px + 4px padding (top+bottom)
 *   border 2-3px   ← thicker on failure
 *   font   8-12px  ← model/error_kind is the loudest; provider muted
 */
.live-block {
  box-sizing: border-box;
  width: 64px;
  height: 76px;
  border-radius: 4px;
  border: 2px solid var(--neutral-bd);
  color: var(--text);
  flex-shrink: 0;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: space-around;
  padding: 3px 4px;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  position: relative;
  transition: transform 0.15s ease, box-shadow 0.15s ease, border-color 0.15s ease;
  overflow: hidden;
}

.live-block--clickable {
  cursor: pointer;
}
.live-block--clickable:hover,
.live-block--clickable:focus-visible {
  transform: translateY(-2px) scale(1.1);
  z-index: 2;
  box-shadow: 0 4px 14px color-mix(in srgb, var(--accent) 45%, transparent);
  outline: none;
}

/* In-flight pulses the border colour so the eye catches it even
 * when scanning a wall of tiles. */
.live-block--in-progress {
  animation: live-block-pulse 1.4s ease-in-out infinite;
}
@keyframes live-block-pulse {
  0%, 100% { box-shadow: 0 0 0 0 rgba(245, 158, 11, 0.0); }
  50%      { box-shadow: 0 0 0 4px var(--warning-strong); }
}

/* Failure: an extra slight scale-up hint + a subtle red glow. */
.live-block--failure {
  box-shadow: 0 0 0 1px var(--danger-strong) inset;
}

.live-block__time {
  font-size: 9px;
  line-height: 1.1;
  color: var(--muted);
  letter-spacing: 0.3px;
  text-align: center;
  width: 100%;
}

.live-block__probe {
  position: absolute;
  top: 2px;
  left: 3px;
  z-index: 1;
  color: var(--warning);
  font-weight: 800;
  text-shadow: 0 1px 2px rgba(0, 0, 0, 0.75);
}

.live-block__probe svg {
  display: block;
  width: 13px;
  height: 13px;
}

.live-block__vendor {
  font-size: 8px;
  line-height: 1.2;
  font-weight: 600;
  letter-spacing: 0.1px;
  text-transform: uppercase;
  width: 100%;
  text-align: center;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  padding: 0 2px;
}

/* Error-kind label takes the model line in failure mode. Display
 * the full error_kind string with ellipsis truncation so operators
 * see complete diagnostic info without hovering. */
.live-block__error-kind {
  font-size: 10px;
  font-weight: 800;
  line-height: 1.1;
  letter-spacing: 0.3px;
  text-transform: uppercase;
  color: var(--danger-bd);
  text-shadow: 0 1px 2px rgba(0, 0, 0, 0.7);
  width: 100%;
  text-align: center;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.live-block__provider {
  font-size: 7px;
  line-height: 1.1;
  font-weight: 600;
  letter-spacing: 0.3px;
  color: var(--muted);
  text-transform: uppercase;
  width: 100%;
  text-align: left;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  opacity: 0.85;
  padding-left: 2px;
}

.live-block__latency {
  font-size: 9px;
  line-height: 1.1;
  color: var(--muted);
  font-variant-numeric: tabular-nums;
  text-align: center;
  width: 100%;
}

.live-block--idle {
  width: 120px;
  height: 60px;
  border: 2px dashed var(--border);
  background: transparent;
  color: var(--muted);
  cursor: default;
  justify-content: center;
}
.live-block__idle-text {
  font-size: 11px;
  font-weight: 500;
}

/* Accessibility: respect reduced-motion preference. */
@media (prefers-reduced-motion: reduce) {
  .live-block,
  .live-block--in-progress {
    animation: none !important;
    transition: none !important;
  }
}
</style>

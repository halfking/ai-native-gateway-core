<script setup lang="ts">
// GatewayCoreBox.vue —— 网关核心「大方块」（2026-10-18 三带改版）
//
// owner 拍板（2026-10-18）：模块要按**处理流程的真实位置**排布，进/出两条
// 链路要分开，还要有「存储转发」的效果，并且在用插件要显示出来。
//
// 三条流程带（自上而下）：
//   进·请求链   缓存 → 脱敏 →（插件 chip）→ 路由 → 节点状态   →⬇ 派发
//   存储转发带  转发队列 N · 数据会话写入 ⇄ 会话文件存储
//   出·响应链   ⬆ 任务识别 · 错误记录 · 探测任务（旁路，虚线）
//
//  · 进站链 L→R（请求自顶边进入、从底边派发出去）；出站链上的模块是
//    响应侧/观测侧（虚线 = 旁路，不是每个请求都串行走一遍）；
//  · 存储带夹在两链之间：响应先落库（写入 ⇄ 文件），再继续转发 —— 带
//    间 ↕ 连接线用流动虚线表达「存储后转发」；转发队列深度来自真实
//    queue 快照（pipeline.depth），不是编的；
//  · 插件 chip 按 direction 落带：input/both → 进站链（请求进上游前
//    检查），output → 出站链（响应回客户端前检查）。外部插件按进程
//    状态着色（ready 绿 / degraded 黄 / failed 红）。
//
// 每格显示窗口内命中数，失败转红；**没有数据就是 0**，不编数字。

import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  GATEWAY_BRANCH_MODULES,
  GATEWAY_IN_MODULES,
  GATEWAY_OUT_MODULES,
  GATEWAY_STORE_MODULES,
  type GatewayModuleKey,
  type TopoPluginChip,
} from '../../types/liveTopology'
import type { GatewayCoreBox } from '../../types/liveTopology'
import { Coin } from '@element-plus/icons-vue'

const props = defineProps<{
  box: GatewayCoreBox
  stats: Record<GatewayModuleKey, { count: number; errors: number }>
  /** 聚焦中的模块（父层点选后传入，纯视觉）。 */
  selected?: GatewayModuleKey | null
  /** 在用插件清单（/api/admin/topology/plugins 投影）。 */
  plugins?: TopoPluginChip[]
  /** 转发队列深度（queue 快照 pipeline.depth；null = 管线未上报，隐藏徽标）。 */
  queueDepth?: number | null
}>()

const emit = defineEmits<{
  (e: 'select-module', key: GatewayModuleKey): void
}>()

const { t } = useI18n()

interface CellView {
  key: GatewayModuleKey
  label: string
  count: number
  branch: boolean
  tone: 'idle' | 'active' | 'error'
}

function cellOf(key: GatewayModuleKey): CellView {
  const stat = props.stats?.[key]
  const count = stat?.count ?? 0
  const errors = stat?.errors ?? 0
  return {
    key,
    label: t(`liveTopology.gateway.${key}`),
    count,
    branch: GATEWAY_BRANCH_MODULES.has(key),
    tone: errors > 0 && count > 0 ? 'error' : count > 0 ? 'active' : 'idle',
  }
}

const inCells = computed<CellView[]>(() => GATEWAY_IN_MODULES.map((m) => cellOf(m.key)))
const storeCells = computed<CellView[]>(() => GATEWAY_STORE_MODULES.map((m) => cellOf(m.key)))
const outCells = computed<CellView[]>(() => GATEWAY_OUT_MODULES.map((m) => cellOf(m.key)))

/** 插件 chip 按方向落带：input/both 在请求侧，output 在响应侧。 */
const inPlugins = computed(() => (props.plugins ?? []).filter((p) => p.direction !== 'output'))
const outPlugins = computed(() => (props.plugins ?? []).filter((p) => p.direction === 'output'))

const showQueue = computed(() => typeof props.queueDepth === 'number' && props.queueDepth >= 0)

const title = computed(() => t('liveTopology.gateway.title'))
</script>

<template>
  <section
    class="gw"
    :style="{ left: `${box.x}px`, top: `${box.y}px`, width: `${box.w}px`, height: `${box.h}px` }"
    :aria-label="title"
    data-testid="gateway-core-box"
  >
    <header class="gw__head">
      <span class="gw__title">{{ title }}</span>
      <span class="gw__flow gw__flow--in" data-testid="gw-flow-in">▼ {{ t('liveTopology.lane.in') }}</span>
      <span class="gw__flow gw__flow--out" data-testid="gw-flow-out">▲ {{ t('liveTopology.lane.out') }}</span>
    </header>

    <!-- 进·请求链：客户端自顶边进 → 缓存 → 脱敏 → 插件 → 路由 → 节点状态 → 底边派发 -->
    <div class="gw__lane gw__lane--in" data-testid="gw-lane-in">
      <span class="gw__lane-tag gw__lane-tag--in">{{ t('liveTopology.lane.in') }}</span>
      <div class="gw__lane-body">
        <template v-for="(cell, i) in inCells" :key="cell.key">
          <span v-if="i > 0" class="gw__arrow" aria-hidden="true">→</span>
          <button
            type="button"
            class="gw__cell"
            :class="[`is-${cell.tone}`, { 'is-branch': cell.branch, 'is-selected': cell.key === selected }]"
            :data-testid="`gw-cell-${cell.key}`"
            :aria-label="`${cell.label} ${cell.count}`"
            @click="emit('select-module', cell.key)"
          >
            <span class="gw__cell-label">{{ cell.label }}</span>
            <span class="gw__cell-count">{{ cell.count }}</span>
          </button>
          <!-- 插件带挂在脱敏之后（治理 stage 在 sanitize 与 route 之间） -->
          <template v-if="cell.key === 'sanitize' && inPlugins.length">
            <span class="gw__arrow" aria-hidden="true">→</span>
            <span class="gw__plugins" data-testid="gw-plugins-in">
              <span
                v-for="p in inPlugins"
                :key="p.id"
                class="gw__plugin"
                :class="`is-${p.status}`"
                :title="`${p.name} · ${p.status}`"
              >{{ p.name }}</span>
            </span>
            <span class="gw__arrow" aria-hidden="true">→</span>
          </template>
        </template>
        <span class="gw__arrow gw__arrow--exit" aria-hidden="true">⬇ {{ t('liveTopology.lane.toProvider') }}</span>
      </div>
    </div>

    <!-- 存储转发带：写入 ⇄ 文件存储；两侧 ↕ 与进出链相连（流动虚线 = 转发） -->
    <div class="gw__lane gw__lane--store" data-testid="gw-lane-store">
      <span class="gw__lane-tag gw__lane-tag--store">{{ t('liveTopology.lane.store') }}</span>
      <div class="gw__lane-body">
        <span
          v-if="showQueue"
          class="gw__queue"
          :class="{ 'is-busy': (queueDepth ?? 0) > 0 }"
          data-testid="gw-queue-depth"
        >{{ t('liveTopology.lane.queue', { n: queueDepth ?? 0 }) }}</span>
        <span class="gw__store-link gw__store-link--in" aria-hidden="true">↕</span>
        <template v-for="(cell, i) in storeCells" :key="cell.key">
          <span v-if="i > 0" class="gw__store-arrow" aria-hidden="true">⇄</span>
          <button
            type="button"
            class="gw__cell gw__cell--store"
            :class="[`is-${cell.tone}`]"
            :data-testid="`gw-cell-${cell.key}`"
            :aria-label="`${cell.label} ${cell.count}`"
            @click="emit('select-module', cell.key)"
          >
            <span class="gw__store-icon" aria-hidden="true"><Coin /></span>
            <span class="gw__cell-label">{{ cell.label }}</span>
            <span class="gw__cell-count">{{ cell.count }}</span>
          </button>
        </template>
        <span class="gw__store-link gw__store-link--out" aria-hidden="true">↕</span>
      </div>
    </div>

    <!-- 出·响应链：自底边收回响应 → 旁路观测模块 → 顶边回客户端 -->
    <div class="gw__lane gw__lane--out" data-testid="gw-lane-out">
      <span class="gw__lane-tag gw__lane-tag--out">{{ t('liveTopology.lane.out') }}</span>
      <div class="gw__lane-body">
        <span class="gw__arrow gw__arrow--entry" aria-hidden="true">{{ t('liveTopology.lane.fromProvider') }} ⬆</span>
        <template v-for="(cell, i) in outCells" :key="cell.key">
          <span v-if="i > 0" class="gw__arrow" aria-hidden="true">→</span>
          <button
            type="button"
            class="gw__cell"
            :class="[`is-${cell.tone}`, { 'is-branch': cell.branch, 'is-selected': cell.key === selected }]"
            :data-testid="`gw-cell-${cell.key}`"
            :aria-label="`${cell.label} ${cell.count}`"
            @click="emit('select-module', cell.key)"
          >
            <span class="gw__cell-label">{{ cell.label }}</span>
            <span class="gw__cell-count">{{ cell.count }}</span>
          </button>
        </template>
        <span v-if="outPlugins.length" class="gw__plugins gw__plugins--out" data-testid="gw-plugins-out">
          <span
            v-for="p in outPlugins"
            :key="p.id"
            class="gw__plugin"
            :class="`is-${p.status}`"
            :title="`${p.name} · ${p.status}`"
          >{{ p.name }}</span>
        </span>
      </div>
    </div>
  </section>
</template>

<style scoped>
.gw {
  position: absolute;
  display: flex;
  flex-direction: column;
  border: 1px solid var(--kx-primary);
  border-radius: 14px;
  background:
    linear-gradient(180deg, color-mix(in srgb, var(--kx-primary) 10%, transparent), transparent 40%),
    color-mix(in srgb, var(--kx-surface) 92%, transparent);
  pointer-events: none;
  overflow: hidden;
}

.gw__head {
  display: flex;
  align-items: baseline;
  gap: 10px;
  flex: none;
  height: 26px;
  padding: 4px 12px 0;
}

.gw__title {
  font-size: var(--kx-font-md, 14px);
  font-weight: 700;
  color: var(--kx-text);
  letter-spacing: 0.04em;
}

.gw__flow {
  font-size: 10px;
  letter-spacing: 0.04em;
}

.gw__flow--in {
  color: var(--kx-primary);
}

.gw__flow--out {
  color: var(--kx-success);
}

/* ── 三条流程带 ─────────────────────────────────────────── */
.gw__lane {
  display: flex;
  flex: 1;
  min-height: 0;
  gap: 8px;
  padding: 0 10px;
}

.gw__lane-tag {
  flex: none;
  width: 18px;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 10px;
  letter-spacing: 0.14em;
  writing-mode: vertical-rl;
  text-orientation: mixed;
  border-radius: 6px;
}

.gw__lane-tag--in {
  color: var(--kx-primary);
  background: color-mix(in srgb, var(--kx-primary) 12%, transparent);
}

.gw__lane-tag--store {
  color: var(--kx-warning);
  background: color-mix(in srgb, var(--kx-warning) 12%, transparent);
}

.gw__lane-tag--out {
  color: var(--kx-success);
  background: color-mix(in srgb, var(--kx-success) 12%, transparent);
}

.gw__lane-body {
  display: flex;
  flex: 1;
  min-width: 0;
  align-items: center;
  gap: 5px;
  overflow: hidden;
}

.gw__arrow {
  flex: none;
  font-size: 11px;
  color: var(--kx-muted);
}

.gw__arrow--exit {
  color: var(--kx-primary);
  margin-left: auto;
  white-space: nowrap;
}

.gw__arrow--entry {
  color: var(--kx-success);
  white-space: nowrap;
}

/* ── 模块格 ─────────────────────────────────────────────── */
.gw__cell {
  display: flex;
  flex-direction: column;
  justify-content: center;
  gap: 1px;
  min-width: 0;
  flex: 1 1 0;
  padding: 4px 8px;
  border: 1px solid var(--kx-border);
  border-radius: 9px;
  background: color-mix(in srgb, var(--kx-surface) 86%, transparent);
  pointer-events: auto;
  cursor: pointer;
  transition: transform 140ms ease, border-color 140ms ease;
}

.gw__cell:hover,
.gw__cell:focus-visible {
  transform: translateY(-1px);
  border-color: var(--kx-primary);
  outline: none;
}

/* 旁路模块：虚线 + 淡色，避免被读成串行流水线的一步 */
.gw__cell.is-branch {
  border-style: dashed;
  opacity: 0.82;
}

.gw__cell.is-active {
  border-color: color-mix(in srgb, var(--kx-primary) 55%, var(--kx-border));
}

.gw__cell.is-error {
  border-color: var(--kx-danger);
}

.gw__cell.is-idle {
  opacity: 0.7;
}

/* 聚焦中的模块：环形描边（不是整格变色 —— 变红会被读成故障） */
.gw__cell.is-selected {
  box-shadow: 0 0 0 2px color-mix(in srgb, var(--kx-primary) 60%, transparent);
}

.gw__cell-label {
  font-size: var(--kx-font-sm, 12px);
  color: var(--kx-text);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.gw__cell-count {
  font-size: 11px;
  font-variant-numeric: tabular-nums;
  color: var(--kx-primary);
}

.gw__cell.is-error .gw__cell-count {
  color: var(--kx-danger);
}

.gw__cell.is-idle .gw__cell-count {
  color: var(--kx-muted);
}

/* ── 存储转发带 ─────────────────────────────────────────── */
.gw__cell--store {
  flex: 0 1 auto;
  min-width: 108px;
  flex-direction: row;
  align-items: center;
  gap: 6px;
  border-color: color-mix(in srgb, var(--kx-warning) 40%, var(--kx-border));
  background:
    repeating-linear-gradient(135deg, transparent 0 6px, color-mix(in srgb, var(--kx-warning) 7%, transparent) 6px 12px),
    color-mix(in srgb, var(--kx-surface) 86%, transparent);
}

.gw__cell--store .gw__cell-label {
  flex: 1;
}

.gw__store-icon {
  color: var(--kx-warning);
  font-size: 13px;
}

.gw__store-arrow {
  flex: none;
  font-size: 13px;
  color: var(--kx-warning);
}

/* 带↕连接线：流动虚线 = 「存储之后继续转发」，不是死仓库 */
.gw__store-link {
  flex: none;
  font-size: 12px;
  color: var(--kx-warning);
  animation: gw-store-pulse 1.8s ease-in-out infinite;
}

@keyframes gw-store-pulse {
  0%, 100% { opacity: 0.45; }
  50% { opacity: 1; }
}

.gw__queue {
  flex: none;
  padding: 2px 8px;
  border-radius: 999px;
  font-size: 10px;
  font-variant-numeric: tabular-nums;
  color: var(--kx-muted);
  border: 1px dashed var(--kx-border);
  white-space: nowrap;
}

.gw__queue.is-busy {
  color: var(--kx-warning);
  border-color: var(--kx-warning);
}

/* ── 插件 chip ──────────────────────────────────────────── */
.gw__plugins {
  display: flex;
  flex: none;
  max-width: 32%;
  flex-wrap: wrap;
  gap: 3px;
  overflow: hidden;
}

.gw__plugin {
  display: inline-flex;
  align-items: center;
  padding: 1px 7px;
  border-radius: 999px;
  font-size: 10px;
  white-space: nowrap;
  border: 1px solid var(--kx-border);
  color: var(--kx-text);
  background: color-mix(in srgb, var(--kx-surface) 80%, transparent);
}

.gw__plugin.is-builtin {
  border-color: color-mix(in srgb, var(--kx-primary) 45%, var(--kx-border));
}

.gw__plugin.is-ready {
  border-color: color-mix(in srgb, var(--kx-success) 55%, var(--kx-border));
  color: var(--kx-success);
}

.gw__plugin.is-degraded {
  border-color: color-mix(in srgb, var(--kx-warning) 65%, var(--kx-border));
  color: var(--kx-warning);
}

.gw__plugin.is-failed,
.gw__plugin.is-stopped {
  border-color: color-mix(in srgb, var(--kx-danger) 65%, var(--kx-border));
  color: var(--kx-danger);
}

.gw__plugin.is-starting {
  color: var(--kx-muted);
}

@media (prefers-reduced-motion: reduce) {
  .gw__cell,
  .gw__store-link {
    transition: none;
    animation: none;
  }
}

/* ── 2026-10-27 第 17 增量（AI 痕迹门 V3）：图标尺寸 ──
   element-plus 图标是裸 <svg viewBox="0 0 1024 1024">，**不带 width/height**
   ⇒ 浏览器按包含块宽度渲染（会撑爆布局）⇒ 必须显式定尺寸。
   尺寸沿用原字形的 font-size 观感（字形墨迹 ≈ 0.7em）。 */
.gw__store-icon > svg { width: 12px; height: 12px; }
</style>

<script setup lang="ts">
// GatewayCoreBox.vue —— 网关核心「大方块」（2026-10-18 三带版；
// 2026-10-28 选路层改版）
//
// owner 口径（2026-10-28）：「内部直连模型」与「auto 按任务选最终模型」这层
// 此前没有表达；缓存→脱敏→压缩三层数据处理缺压缩；路由与节点状态属
// 基础服务，放一侧，突出核心逻辑。
//
// 版面（主线居中，基础服务侧置；顺序=真实管线，批判式审计修正 2026-10-28）：
//   进·请求链（主线）  缓存 → 脱敏 →（插件 chip）→ 模型选择 → 压缩 → ⬇ 派发
//   选路扇出带          模型选择的出口分解：auto 按任务 N（任务分布）· 内部直连 M · 指定 K
//   存储转发带          转发队列 N · 数据会话写入 ⇄ 会话文件存储
//   出·响应链          ⬆ 任务识别 · 错误记录 · 探测任务（旁路，虚线）
//   基础服务侧条（右侧竖条）  路由管理 · 节点状态管理
//
//  · 进站链 L→R；选路扇出带是「模型选择」这一步的三个**并行出口**
//    （不是串行步骤，卡之间不画箭头）；出站链上的模块是响应侧/观测侧
//    （虚线 = 旁路）；
//  · 存储带夹在两链之间：响应先落库（写入 ⇄ 文件），再继续转发；
//  · 基础服务侧条服务于主线（凭据选择、熔断/健康管理）但不是数据
//    处理步骤 —— 虚线竖条与主线分开，格子仍可点选聚焦。
//
// 每格显示窗口内命中数，失败转红；**没有数据就是 0**，不编数字。

import { computed, onBeforeUnmount, reactive, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  GATEWAY_BRANCH_MODULES,
  GATEWAY_INFRA_MODULES,
  GATEWAY_IN_MODULES,
  GATEWAY_OUT_MODULES,
  GATEWAY_STORE_MODULES,
  type GatewayModuleKey,
  type ModelSelectionStats,
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
  /** 选路扇出统计（模型选择三出口）；null/缺省 = 尚无数据，三卡画 0。 */
  selection?: ModelSelectionStats | null
  /** 内部直连分桶是否完成测量（供应商清单加载失败 = false，须标注未测量）。 */
  directMeasured?: boolean
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
const infraCells = computed<CellView[]>(() => GATEWAY_INFRA_MODULES.map((m) => cellOf(m.key)))
const storeCells = computed<CellView[]>(() => GATEWAY_STORE_MODULES.map((m) => cellOf(m.key)))
const outCells = computed<CellView[]>(() => GATEWAY_OUT_MODULES.map((m) => cellOf(m.key)))

/** 选路扇出三出口卡（并行出口，非串行步骤）。 */
const modeCards = computed(() => {
  const sel = props.selection
  return [
    {
      key: 'auto' as const,
      label: t('liveTopology.selection.auto'),
      count: sel?.auto ?? 0,
      /** auto 的任务分布（后端原始词：chat/reasoning/code/…，跨语种一致）。 */
      detail: (sel?.autoTasks ?? []).map((x) => `${x.type} ×${x.count}`).join(' · '),
    },
    {
      key: 'direct' as const,
      label: t('liveTopology.selection.direct'),
      count: sel?.direct ?? 0,
      detail: '',
    },
    {
      key: 'explicit' as const,
      label: t('liveTopology.selection.explicit'),
      count: sel?.explicit ?? 0,
      detail: '',
    },
  ]
})

/** 插件 chip 按方向落带：input/both 在请求侧，output 在响应侧。 */
const inPlugins = computed(() => (props.plugins ?? []).filter((p) => p.direction !== 'output'))
const outPlugins = computed(() => (props.plugins ?? []).filter((p) => p.direction === 'output'))

const showQueue = computed(() => typeof props.queueDepth === 'number' && props.queueDepth >= 0)

/** 模块格「刚处理过请求」脉冲（2026-10-28 live 页动效轮）：窗口内命中数
 *  **增加**的那一格闪一次（460ms 渐隐）—— 「这个模块正在干活」在图上
 *  有了直接表达，而不只是一个静默更新的数字。
 *  ★ 首见不闪：stats 从空到有值是「数据到达」不是「新活动」；
 *  ★ count 下降不闪：窗口滑动导致计数回落，不是新请求命中。 */
const flashing = reactive(new Set<GatewayModuleKey>())
const flashTimers = new Map<GatewayModuleKey, number>()
const lastCounts = new Map<GatewayModuleKey, number>()

watch(
  () => props.stats,
  (stats) => {
    for (const [key, s] of Object.entries(stats ?? {})) {
      const k = key as GatewayModuleKey
      const prev = lastCounts.get(k)
      if (prev !== undefined && s.count > prev) {
        flashing.add(k)
        const old = flashTimers.get(k)
        if (old) window.clearTimeout(old)
        flashTimers.set(
          k,
          window.setTimeout(() => flashing.delete(k), 470),
        )
      }
      lastCounts.set(k, s.count)
    }
  },
  { deep: true, immediate: true },
)
onBeforeUnmount(() => {
  for (const t of flashTimers.values()) window.clearTimeout(t)
})

const title = computed(() => t('liveTopology.gateway.title'))
const infraTitle = computed(() => t('liveTopology.gateway.infra'))
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

    <!-- 主体：左侧四条流程带（主线），右侧基础服务竖条（2026-10-28）。 -->
    <div class="gw__body">
      <div class="gw__lanes">
        <!-- 进·请求链（主线）：缓存 → 脱敏 → 插件 → 模型选择 → 压缩 → 派发。
             ★ 压缩在模型选择**之后**（真实管线：候选解析先定目标模型，
             SessionCompressor 才按其上下文窗口做滑窗压缩）。 -->
        <div class="gw__lane gw__lane--in" data-testid="gw-lane-in">
          <span class="gw__lane-tag gw__lane-tag--in">{{ t('liveTopology.lane.in') }}</span>
          <div class="gw__lane-body">
            <template v-for="(cell, i) in inCells" :key="cell.key">
              <span v-if="i > 0" class="gw__arrow" aria-hidden="true">→</span>
              <button
                type="button"
                class="gw__cell"
                :class="[`is-${cell.tone}`, { 'is-branch': cell.branch, 'is-selected': cell.key === selected, 'is-flash': flashing.has(cell.key) }]"
                :data-testid="`gw-cell-${cell.key}`"
                :aria-label="`${cell.label} ${cell.count}`"
                @click="emit('select-module', cell.key)"
              >
                <span class="gw__cell-label">{{ cell.label }}</span>
                <span class="gw__cell-count">{{ cell.count }}</span>
              </button>
              <!-- 插件带挂在脱敏之后（输入侧治理件在请求进上游前检查） -->
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

        <!-- 选路扇出带：模型选择这一步的三个出口分解（并行、非串行步骤）。
             是「模型选择」格的明细注解带：不带派发箭头（主链出口在进站链末尾）。 -->
        <div class="gw__lane gw__lane--select" data-testid="gw-lane-select">
          <span class="gw__lane-tag gw__lane-tag--select">{{ t('liveTopology.lane.select') }}</span>
          <div class="gw__lane-body">
            <span
              v-for="mode in modeCards"
              :key="mode.key"
              class="gw__mode"
              :class="{ 'is-active': mode.count > 0 }"
              :data-testid="`gw-mode-${mode.key}`"
              :aria-label="`${mode.label} ${mode.count}`"
              :title="mode.detail || mode.label"
            >
              <span class="gw__mode-label">{{ mode.label }}</span>
              <span class="gw__mode-count">{{ mode.count }}</span>
              <span v-if="mode.detail" class="gw__mode-detail">{{ mode.detail }}</span>
            </span>
            <!-- 直连分桶未测量必须说出口：0 是「没有直连」，「没测过」不是 0。 -->
            <span v-if="directMeasured === false" class="gw__mode-note" data-testid="gw-mode-unmeasured">
              {{ t('liveTopology.selection.unmeasured') }}
            </span>
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
            <!-- 带↕连接线：竖向流动虚线 = 「存储之后继续转发」，不是死仓库。
                 请求侧向下流入存储带（请求先落库），响应侧向上流回出站链。 -->
            <span class="gw__store-link" aria-hidden="true"><i class="gw__flow-line gw__flow-line--down" /></span>
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
            <span class="gw__store-link" aria-hidden="true"><i class="gw__flow-line gw__flow-line--up" /></span>
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
                :class="[`is-${cell.tone}`, { 'is-branch': cell.branch, 'is-selected': cell.key === selected, 'is-flash': flashing.has(cell.key) }]"
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
      </div>

      <!-- 基础服务侧条：路由管理 · 节点状态管理（服务于主线，不在主链上） -->
      <aside class="gw__infra" data-testid="gw-infra" :aria-label="infraTitle">
        <span class="gw__infra-tag">{{ infraTitle }}</span>
        <div class="gw__infra-cells">
          <button
            v-for="cell in infraCells"
            :key="cell.key"
            type="button"
            class="gw__cell gw__cell--infra"
            :class="[`is-${cell.tone}`, { 'is-selected': cell.key === selected, 'is-flash': flashing.has(cell.key) }]"
            :data-testid="`gw-cell-${cell.key}`"
            :aria-label="`${cell.label} ${cell.count}`"
            @click="emit('select-module', cell.key)"
          >
            <span class="gw__cell-label">{{ cell.label }}</span>
            <span class="gw__cell-count">{{ cell.count }}</span>
          </button>
        </div>
      </aside>
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
  pointer-events: auto;
  /* 2026-10-29 空白清空轮：面板表面必须自己接住点击。此前 none 让点击
     穿透到画布根 —— 画布根的空白点击=清除点选，而大方块占画面正中，
     用户不会认为点它是「点空白」⇒ 过滤被误清。TopologyCanvas 在本组件
     根上挂了 @click.stop：表面/头部/格子的点击到此为止，不再冒泡。 */
  overflow: hidden;
}

.gw__head {
  display: flex;
  align-items: baseline;
  gap: 8px;
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
  font-size: 11px;
  letter-spacing: 0.04em;
}

.gw__flow--in {
  color: var(--kx-primary);
}

.gw__flow--out {
  color: var(--kx-success);
}

/* ── 主体：左流程带列 + 右基础服务竖条 ───────────────────── */
.gw__body {
  display: flex;
  flex: 1;
  min-height: 0;
  gap: 4px;
  padding: 0 8px 8px;
}

.gw__lanes {
  display: flex;
  flex: 1;
  min-width: 0;
  flex-direction: column;
  gap: 4px;
}

/* ── 流程带 ──────────────────────────────────────────────── */
.gw__lane {
  display: flex;
  flex: 1;
  min-height: 0;
  gap: 8px;
}

.gw__lane-tag {
  flex: none;
  width: 18px;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 11px;
  letter-spacing: 0.14em;
  writing-mode: vertical-rl;
  text-orientation: mixed;
  border-radius: 6px;
}

.gw__lane-tag--in {
  color: var(--kx-primary);
  background: color-mix(in srgb, var(--kx-primary) 12%, transparent);
}

.gw__lane-tag--select {
  color: var(--kx-primary);
  background: color-mix(in srgb, var(--kx-primary) 8%, transparent);
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
  gap: 4px;
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
  transition: transform 140ms var(--kx-motion-ease-out), border-color 140ms var(--kx-motion-ease-out);
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

/* ── 选路扇出带：三个并行出口卡 ─────────────────────────── */
.gw__mode {
  display: flex;
  flex: 1 1 0;
  min-width: 0;
  flex-direction: column;
  justify-content: center;
  gap: 0;
  padding: 2px 8px;
  border: 1px solid color-mix(in srgb, var(--kx-primary) 25%, var(--kx-border));
  border-radius: 9px;
  background: color-mix(in srgb, var(--kx-surface) 86%, transparent);
}

.gw__mode.is-active {
  border-color: color-mix(in srgb, var(--kx-primary) 55%, var(--kx-border));
}

.gw__mode-label {
  font-size: var(--kx-font-sm, 12px);
  color: var(--kx-text);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.gw__mode-count {
  font-size: 11px;
  font-variant-numeric: tabular-nums;
  color: var(--kx-primary);
}

.gw__mode-detail {
  font-size: 11px;
  color: var(--kx-muted);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.gw__mode-note {
  flex: none;
  padding: 1px 8px;
  border-radius: 999px;
  font-size: 11px;
  color: var(--kx-muted);
  border: 1px dashed var(--kx-border);
  white-space: nowrap;
}

/* ── 基础服务侧条 ────────────────────────────────────────── */
.gw__infra {
  flex: none;
  width: 92px;
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 4px 4px 4px 8px;
  border-left: 1px dashed color-mix(in srgb, var(--kx-muted) 45%, var(--kx-border));
}

.gw__infra-tag {
  flex: none;
  font-size: 11px;
  letter-spacing: 0.1em;
  color: var(--kx-muted);
  text-align: center;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.gw__infra-cells {
  display: flex;
  flex: 1;
  min-height: 0;
  flex-direction: column;
  gap: 4px;
}

/* 侧条格：横排（窄列里竖排放不下两行文字），占比语义与主链格一致 */
.gw__cell--infra {
  flex: 1 1 0;
  flex-direction: row;
  align-items: center;
  gap: 4px;
  border-style: dashed;
}

.gw__cell--infra .gw__cell-label {
  flex: 1;
  font-size: 11px;
}

/* ── 存储转发带 ─────────────────────────────────────────── */
.gw__cell--store {
  flex: 0 1 auto;
  min-width: 108px;
  flex-direction: row;
  align-items: center;
  gap: 4px;
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

/* 带↕连接线：竖向流动虚线（2026-10-28 由 ↕ 字符升级 —— 字形当图标
   是 craft-floor 禁的形态，且一条真的在流动的线比一个闪烁的字更能
   表达「存储后转发」的方向）。linear + background-position 匀速位移
   ⇒ M7 的「无限循环匀速」判据成立。 */
.gw__store-link {
  flex: none;
  display: flex;
  align-items: center;
  align-self: stretch;
  padding: 3px 0;
}

.gw__flow-line {
  display: block;
  width: 2px;
  height: 100%;
  border-radius: 1px;
  background: repeating-linear-gradient(
    180deg,
    color-mix(in srgb, var(--kx-warning) 60%, transparent) 0 3px,
    transparent 3px 7px
  );
}

/* 进站链收完请求向下流入存储带；出站链自存储带向上回流。
   pattern 周期 7px ⇒ 位移 7px 无缝循环。 */
.gw__flow-line--down {
  animation: gw-flow-down 0.9s linear infinite;
}

.gw__flow-line--up {
  animation: gw-flow-up 0.9s linear infinite;
}

@keyframes gw-flow-down {
  from { background-position: 0 0; }
  to { background-position: 0 7px; }
}

@keyframes gw-flow-up {
  from { background-position: 0 0; }
  to { background-position: 0 -7px; }
}

/* 模块格「刚处理过请求」脉冲：从命中色指数减速回落到默认
   （470ms ≤ 800ms 一次性上限；起始帧不透明度不为 0 ⇒ 非入场类）。 */
.gw__cell.is-flash {
  animation: gw-cell-flash 0.47s var(--kx-motion-ease-out);
}

@keyframes gw-cell-flash {
  0% {
    border-color: color-mix(in srgb, var(--kx-primary) 80%, var(--kx-border));
    background-color: color-mix(in srgb, var(--kx-primary) 16%, var(--kx-surface));
  }
}

.gw__cell.is-error.is-flash {
  animation-name: gw-cell-flash-error;
}

@keyframes gw-cell-flash-error {
  0% {
    border-color: var(--kx-danger);
    background-color: color-mix(in srgb, var(--kx-danger) 14%, var(--kx-surface));
  }
}

.gw__queue {
  flex: none;
  padding: 2px 8px;
  border-radius: 999px;
  font-size: 11px;
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
  padding: 1px 8px;
  border-radius: 999px;
  font-size: 11px;
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
  /* 新增动效的 reduced-motion 路径：脉冲与流线停下（静态虚线仍表达
     「连接」，信息不丢），hover 反馈保留。 */
  .gw__cell.is-flash,
  .gw__flow-line--down,
  .gw__flow-line--up {
    animation: none;
  }
}

/* ── 2026-10-27 第 17 增量（AI 痕迹门 V3）：图标尺寸 ──
   element-plus 图标是裸 <svg viewBox="0 0 1024 1024">，**不带 width/height**
   ⇒ 浏览器按包含块宽度渲染（会撑爆布局）⇒ 必须显式定尺寸。
   尺寸沿用原字形的 font-size 观感（字形墨迹 ≈ 0.7em）。 */
.gw__store-icon > svg { width: 12px; height: 12px; }
</style>

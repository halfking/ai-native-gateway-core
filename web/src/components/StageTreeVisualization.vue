<script setup lang="ts">
/**
 * StageTreeVisualization — Request pipeline stage tree component.
 * 
 * Displays the complete request execution pipeline from authentication to reply,
 * with real-time status updates via SSE. Supports both full and compact display modes.
 * 
 * Design based on: docs/audit/2026-11-16-live阶段树原型-完整紧凑双模式版.html
 * 
 * @component
 * @since 2026-11-16
 */
import { ref, computed, watch } from 'vue';
import type { StageTreeFrame, StageNode, StageKey, StageStatus } from '@/types/stageTree';
import { 
  STAGE_NAMES, 
  STATUS_NAMES, 
  STATUS_GLYPHS,
  COMPACT_GLYPHS,
  STAGE_ICONS,
  getStageColor,
  formatDuration,
  formatTokens,
  findStage,
} from '@/types/stageTree';

interface Props {
  frame?: StageTreeFrame | null;
  mode?: 'full' | 'compact';
  showTimeline?: boolean;
}

const props = withDefaults(defineProps<Props>(), {
  frame: null,
  mode: 'full',
  showTimeline: true,
});

const emit = defineEmits<{
  stageClick: [stage: StageNode];
  attemptClick: [attemptIndex: number];
}>();

// Computed properties
const isCompact = computed(() => props.mode === 'compact');

const allStages = computed(() => {
  if (!props.frame) return [];
  return [...props.frame.send_stages, ...props.frame.recv_stages];
});

const overallStatusClass = computed(() => {
  if (!props.frame) return '';
  const status = props.frame.overall_status;
  return `status-${status}`;
});

// Get glyph for status based on mode
function getStatusGlyph(status: StageStatus): string {
  return isCompact.value ? COMPACT_GLYPHS[status] : STATUS_GLYPHS[status];
}

// Get status CSS class
function getStatusClass(status: StageStatus): string {
  return `s-${status}`;
}

// Handle stage click
function handleStageClick(stage: StageNode) {
  emit('stageClick', stage);
}

// Handle attempt click
function handleAttemptClick(index: number) {
  emit('attemptClick', index);
}

// Format timestamp for display
function formatTime(timestamp: string | undefined): string {
  if (!timestamp) return '—';
  try {
    const date = new Date(timestamp);
    return date.toLocaleTimeString('zh-CN', { 
      hour: '2-digit', 
      minute: '2-digit', 
      second: '2-digit',
      fractionalSecondDigits: 3,
    });
  } catch {
    return '—';
  }
}
</script>

<template>
  <div 
    class="stage-tree-visualization" 
    :class="{ 
      'mode-compact': isCompact,
      'mode-full': !isCompact,
      [overallStatusClass]: true,
    }"
  >
    <!-- Empty state（用 frame 直判，便于 vue-tsc 收窄可空 props） -->
    <div v-if="!frame" class="stage-tree-empty">
      <p>暂无请求数据</p>
    </div>

    <!-- Full mode visualization -->
    <div v-else-if="!isCompact" class="stage-tree-full">
      <!-- Header -->
      <div class="stage-tree-header">
        <div class="header-left">
          <h3 class="request-id">{{ frame.request_id }}</h3>
          <span class="phase-badge" :class="`phase-${frame.phase}`">
            {{ frame.phase }}
          </span>
        </div>
        <div class="header-right">
          <span v-if="frame.total_latency_ms" class="metric">
            ⏱ {{ formatDuration(frame.total_latency_ms) }}
          </span>
          <span v-if="frame.ttft_ms" class="metric">
            ⚡ TTFT {{ formatDuration(frame.ttft_ms) }}
          </span>
          <span v-if="frame.tokens_in" class="metric">
            ⇅ {{ formatTokens(frame.tokens_in) }}
          </span>
          <span v-if="frame.tokens_out" class="metric">
            → {{ formatTokens(frame.tokens_out) }}
          </span>
        </div>
      </div>

      <!-- Model info -->
      <div v-if="frame.client_model" class="model-info">
        <span class="label">请求模型:</span>
        <span class="value">{{ frame.client_model }}</span>
        <template v-if="frame.canonical_model && frame.canonical_model !== frame.client_model">
          <span class="arrow">→</span>
          <span class="value canonical">{{ frame.canonical_model }}</span>
        </template>
      </div>

      <!-- Pipeline visualization -->
      <div class="pipeline-container">
        <!-- Send rail -->
        <div class="pipeline-rail send-rail">
          <div class="rail-label">发送轨 →</div>
          <div class="stage-nodes">
            <div
              v-for="stage in frame.send_stages"
              :key="stage.key"
              class="stage-node"
              :class="getStatusClass(stage.status)"
              @click="handleStageClick(stage)"
            >
              <div class="node-icon" v-html="STAGE_ICONS[stage.key]"></div>
              <div class="node-content">
                <div class="node-header">
                  <span class="node-glyph">{{ getStatusGlyph(stage.status) }}</span>
                  <span class="node-name">{{ stage.name }}</span>
                </div>
                <div v-if="stage.duration_ms" class="node-duration">
                  {{ formatDuration(stage.duration_ms) }}
                </div>
                <div v-if="stage.error_kind" class="node-error">
                  {{ stage.error_kind }}
                </div>
              </div>
              <div class="node-status-bar" :style="{ background: getStageColor(stage.status) }"></div>
            </div>
          </div>
        </div>

        <!-- Receive rail -->
        <div class="pipeline-rail recv-rail">
          <div class="rail-label">← 接收轨</div>
          <div class="stage-nodes">
            <div
              v-for="stage in frame.recv_stages"
              :key="stage.key"
              class="stage-node"
              :class="getStatusClass(stage.status)"
              @click="handleStageClick(stage)"
            >
              <div class="node-icon" v-html="STAGE_ICONS[stage.key]"></div>
              <div class="node-content">
                <div class="node-header">
                  <span class="node-glyph">{{ getStatusGlyph(stage.status) }}</span>
                  <span class="node-name">{{ stage.name }}</span>
                </div>
                <div v-if="stage.duration_ms" class="node-duration">
                  {{ formatDuration(stage.duration_ms) }}
                </div>
              </div>
              <div class="node-status-bar" :style="{ background: getStageColor(stage.status) }"></div>
            </div>
          </div>
        </div>
      </div>

      <!-- Attempts section -->
      <div v-if="frame.attempts && frame.attempts.length > 0" class="attempts-section">
        <h4>路由尝试</h4>
        <div class="attempts-list">
          <div
            v-for="(attempt, index) in frame.attempts"
            :key="attempt.sequence"
            class="attempt-item"
            :class="{ 
              'attempt-success': attempt.success, 
              'attempt-failed': !attempt.success,
              'attempt-current': index + 1 === frame.current_attempt,
            }"
            @click="handleAttemptClick(index)"
          >
            <div class="attempt-header">
              <span class="attempt-number">#{{ attempt.sequence }}</span>
              <span class="attempt-provider">{{ attempt.provider }}</span>
              <span v-if="attempt.credential_label" class="attempt-credential">
                {{ attempt.credential_label }}
              </span>
              <span v-if="attempt.is_fallback" class="attempt-badge fallback">重试</span>
              <span class="attempt-status" :class="{ success: attempt.success, failed: !attempt.success }">
                {{ attempt.success ? '成功' : '失败' }}
              </span>
            </div>
            <div class="attempt-metrics">
              <span v-if="attempt.latency_ms">{{ formatDuration(attempt.latency_ms) }}</span>
              <span v-if="attempt.ttft_ms">TTFT {{ formatDuration(attempt.ttft_ms) }}</span>
              <span v-if="attempt.prompt_tokens">
                Tokens: {{ attempt.prompt_tokens }} + {{ attempt.completion_tokens }}
              </span>
            </div>
            <div v-if="attempt.error_kind" class="attempt-error">
              {{ attempt.error_kind }}: {{ attempt.error_message }}
            </div>
          </div>
        </div>
      </div>

      <!-- Session info -->
      <div v-if="frame.session_id" class="session-info">
        <span class="label">会话:</span>
        <span class="value">{{ frame.session_id }}</span>
        <span v-if="frame.is_first_turn" class="badge first-turn">首轮</span>
        <span v-else class="turn-number">第 {{ frame.session_turn }} 轮</span>
      </div>
    </div>

    <!-- Compact mode visualization -->
    <div v-else class="stage-tree-compact">
      <div class="compact-row">
        <!-- Time column -->
        <div class="time-col">
          {{ formatTime(frame.start_time) }}
        </div>

        <!-- Client badge -->
        <div class="client-badge" :class="getStatusClass(frame.overall_status)">
          <span class="client-name">{{ frame.client_name || frame.client_model }}</span>
        </div>

        <!-- Stage tiles -->
        <div class="stage-tiles">
          <div
            v-for="stage in allStages"
            :key="stage.key"
            class="stage-tile"
            :class="getStatusClass(stage.status)"
            @click="handleStageClick(stage)"
          >
            <div class="tile-icon" v-html="STAGE_ICONS[stage.key]"></div>
            <span v-if="getStatusGlyph(stage.status)" class="tile-glyph">
              {{ getStatusGlyph(stage.status) }}
            </span>
            <span v-if="stage.duration_ms" class="tile-num">
              {{ Math.round(stage.duration_ms) }}
            </span>
          </div>
        </div>

        <!-- Summary info -->
        <div class="summary-info">
          <span v-if="frame.is_first_turn" class="sess-badge first">◆ 首轮</span>
          <span v-else-if="frame.session_turn" class="sess-badge">第 {{ frame.session_turn }} 轮</span>
          <span class="model-name">{{ frame.canonical_model || frame.client_model }}</span>
          <span v-if="frame.tokens_in" class="tokens">
            ⇅ {{ formatTokens(frame.tokens_in) }}
          </span>
          <span v-if="frame.tokens_out" class="tokens">
            → {{ formatTokens(frame.tokens_out) }}
          </span>
          <span v-if="frame.total_latency_ms" class="duration">
            {{ formatDuration(frame.total_latency_ms) }}
          </span>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* Base styles */
.stage-tree-visualization {
  font-family: -apple-system, BlinkMacSystemFont, 'PingFang SC', 'Microsoft YaHei', sans-serif;
  font-size: 13px;
  color: var(--kx-text, #e8eef7);
}

.stage-tree-empty {
  padding: 40px;
  text-align: center;
  color: var(--kx-muted, #8b9bb0);
}

/* Full mode styles */
.stage-tree-full {
  padding: 16px;
  background: var(--kx-surface, #1a222d);
  border-radius: 8px;
  border: 1px solid var(--kx-border, #2a3544);
}

.stage-tree-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 12px;
  padding-bottom: 12px;
  border-bottom: 1px solid var(--kx-border, #2a3544);
}

.header-left {
  display: flex;
  align-items: center;
  gap: 12px;
}

.request-id {
  margin: 0;
  font-size: 15px;
  font-weight: 650;
  font-family: ui-monospace, 'SF Mono', Menlo, monospace;
}

.phase-badge {
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 11px;
  font-weight: 600;
  background: var(--kx-primary-soft, rgba(91, 140, 255, 0.16));
  color: var(--kx-primary, #5b8cff);
}

.header-right {
  display: flex;
  gap: 12px;
  font-size: 12px;
}

.metric {
  color: var(--kx-muted, #8b9bb0);
  font-variant-numeric: tabular-nums;
}

.model-info {
  margin-bottom: 16px;
  padding: 8px 12px;
  background: var(--kx-panel-bg, rgba(15, 20, 28, 0.4));
  border-radius: 6px;
  font-size: 12px;
}

.model-info .label {
  color: var(--kx-muted, #8b9bb0);
  margin-right: 8px;
}

.model-info .value {
  color: var(--kx-text, #e8eef7);
  font-weight: 600;
}

.model-info .canonical {
  color: var(--kx-primary, #5b8cff);
}

.model-info .arrow {
  margin: 0 6px;
  color: var(--kx-muted, #8b9bb0);
}

/* Pipeline visualization */
.pipeline-container {
  display: flex;
  flex-direction: column;
  gap: 16px;
  margin-bottom: 16px;
}

.pipeline-rail {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.rail-label {
  font-size: 11px;
  color: var(--kx-muted, #8b9bb0);
  font-weight: 600;
  letter-spacing: 0.3px;
}

.stage-nodes {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}

.stage-node {
  position: relative;
  min-width: 104px;
  padding: 6px 8px;
  background: var(--kx-surface, #1a222d);
  border: 1px solid var(--kx-border, #2a3544);
  border-radius: 6px;
  cursor: pointer;
  transition: all 0.18s;
  overflow: hidden;
}

.stage-node:hover {
  border-color: var(--kx-primary-hover, #7aa3ff);
  box-shadow: 0 4px 12px rgba(0, 0, 0, 0.2);
}

.node-icon {
  width: 14px;
  height: 14px;
  margin-bottom: 4px;
  opacity: 0.7;
}

.node-icon :deep(svg) {
  width: 100%;
  height: 100%;
  fill: none;
  stroke: currentColor;
  stroke-width: 1.7;
  stroke-linecap: round;
  stroke-linejoin: round;
}

.node-content {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.node-header {
  display: flex;
  align-items: center;
  gap: 4px;
}

.node-glyph {
  font-size: 10px;
  line-height: 1;
}

.node-name {
  font-size: 11px;
  font-weight: 600;
}

.node-duration,
.node-error {
  font-size: 10px;
  color: var(--kx-muted, #8b9bb0);
}

.node-status-bar {
  position: absolute;
  left: 0;
  bottom: 0;
  width: 100%;
  height: 2px;
  opacity: 0.9;
}

/* Status-specific node styles */
.stage-node.s-done {
  border-color: var(--kx-success-line, rgba(62, 207, 142, 0.42));
}

.stage-node.s-active {
  border-color: var(--kx-primary, #5b8cff);
  background: var(--kx-primary-soft, rgba(91, 140, 255, 0.16));
  box-shadow: 0 0 0 1px var(--kx-primary-line, rgba(91, 140, 255, 0.38));
}

.stage-node.s-failed {
  border-color: var(--kx-danger, #f07167);
  background: var(--kx-danger-soft, rgba(240, 113, 103, 0.14));
}

.stage-node.s-pending {
  opacity: 0.42;
  border-style: dashed;
}

.stage-node.s-skipped {
  opacity: 0.6;
  background: repeating-linear-gradient(
    45deg,
    var(--kx-hatch, rgba(139, 155, 176, 0.09)) 0 4px,
    transparent 4px 8px
  );
}

/* Attempts section */
.attempts-section {
  margin-top: 16px;
  padding-top: 16px;
  border-top: 1px solid var(--kx-border, #2a3544);
}

.attempts-section h4 {
  margin: 0 0 8px 0;
  font-size: 12px;
  color: var(--kx-muted, #8b9bb0);
  font-weight: 600;
}

.attempts-list {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.attempt-item {
  padding: 8px 10px;
  background: var(--kx-chip-bg, rgba(139, 155, 176, 0.12));
  border-radius: 6px;
  border: 1px solid var(--kx-border, #2a3544);
  cursor: pointer;
  transition: all 0.18s;
}

.attempt-item:hover {
  border-color: var(--kx-primary, #5b8cff);
}

.attempt-item.attempt-current {
  border-color: var(--kx-primary, #5b8cff);
  background: var(--kx-primary-soft, rgba(91, 140, 255, 0.16));
}

.attempt-header {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 4px;
  font-size: 12px;
}

.attempt-number {
  font-weight: 700;
  font-family: ui-monospace, monospace;
  color: var(--kx-muted, #8b9bb0);
}

.attempt-provider {
  font-weight: 600;
  color: var(--kx-text, #e8eef7);
}

.attempt-credential {
  color: var(--kx-muted, #8b9bb0);
  font-size: 11px;
}

.attempt-badge {
  padding: 1px 6px;
  border-radius: 999px;
  font-size: 10px;
  font-weight: 600;
  background: var(--kx-warning-soft, rgba(224, 165, 58, 0.14));
  color: var(--kx-warning, #e0a53a);
}

.attempt-status {
  margin-left: auto;
  font-weight: 600;
}

.attempt-status.success {
  color: var(--kx-success, #3ecf8e);
}

.attempt-status.failed {
  color: var(--kx-danger, #f07167);
}

.attempt-metrics {
  display: flex;
  gap: 12px;
  font-size: 11px;
  color: var(--kx-muted, #8b9bb0);
  font-variant-numeric: tabular-nums;
}

.attempt-error {
  margin-top: 4px;
  font-size: 10px;
  color: var(--kx-danger, #f07167);
  font-family: ui-monospace, monospace;
}

/* Session info */
.session-info {
  margin-top: 12px;
  padding-top: 12px;
  border-top: 1px solid var(--kx-border, #2a3544);
  font-size: 12px;
}

.session-info .label {
  color: var(--kx-muted, #8b9bb0);
  margin-right: 8px;
}

.session-info .value {
  color: var(--kx-text, #e8eef7);
  font-family: ui-monospace, monospace;
}

.session-info .badge {
  margin-left: 8px;
  padding: 2px 6px;
  border-radius: 999px;
  font-size: 10px;
  font-weight: 600;
  background: var(--kx-dormant-soft, rgba(167, 139, 250, 0.15));
  color: var(--kx-dormant, #a78bfa);
  border: 1px solid var(--kx-dormant-line, rgba(167, 139, 250, 0.42));
}

.session-info .turn-number {
  margin-left: 8px;
  color: var(--kx-muted, #8b9bb0);
}

/* Compact mode styles */
.stage-tree-compact {
  padding: 8px 12px;
}

.compact-row {
  display: flex;
  align-items: center;
  gap: 12px;
  min-height: 40px;
}

.time-col {
  width: 52px;
  font-size: 10px;
  font-family: ui-monospace, 'SF Mono', Menlo, monospace;
  color: var(--kx-muted, #8b9bb0);
  flex-shrink: 0;
}

.client-badge {
  padding: 4px 10px;
  border-radius: 6px;
  border: 1px solid var(--kx-border, #2a3544);
  background: var(--kx-surface, #1a222d);
  font-size: 11px;
  font-weight: 600;
  min-width: 120px;
  flex-shrink: 0;
}

.client-badge.s-done {
  border-color: var(--kx-success-line, rgba(62, 207, 142, 0.42));
}

.client-badge.s-failed {
  border-color: var(--kx-danger, #f07167);
}

.stage-tiles {
  display: flex;
  gap: 6px;
  flex-shrink: 0;
}

.stage-tile {
  position: relative;
  width: 28px;
  height: 28px;
  border-radius: 6px;
  background: transparent;
  cursor: pointer;
  transition: all 0.16s;
  color: var(--kx-muted, #8b9bb0);
}

.stage-tile:hover {
  transform: translateY(-1px);
  background: var(--kx-chip-bg, rgba(139, 155, 176, 0.12));
}

.tile-icon {
  position: absolute;
  left: 6px;
  top: 4px;
  width: 15px;
  height: 15px;
}

.tile-icon :deep(svg) {
  width: 100%;
  height: 100%;
  fill: none;
  stroke: currentColor;
  stroke-width: 1.7;
  stroke-linecap: round;
  stroke-linejoin: round;
}

.tile-glyph {
  position: absolute;
  top: -4px;
  right: -4px;
  font-size: 9px;
  line-height: 10px;
  font-weight: 800;
  padding: 0 1px;
  border-radius: 3px;
}

.tile-num {
  position: absolute;
  right: 0;
  bottom: 0;
  font-size: 7px;
  line-height: 8px;
  font-weight: 600;
  color: var(--kx-muted, #8b9bb0);
  font-variant-numeric: tabular-nums;
  padding: 0 1px;
  border-radius: 2px;
  max-width: 30px;
  overflow: hidden;
  white-space: nowrap;
}

/* Compact status styles */
.stage-tile.s-done {
  color: var(--kx-success, #3ecf8e);
}

.stage-tile.s-done .tile-glyph {
  background: var(--kx-success, #3ecf8e);
  color: var(--kx-surface, #1a222d);
}

.stage-tile.s-active {
  color: var(--kx-primary, #5b8cff);
}

.stage-tile.s-active .tile-glyph {
  background: var(--kx-primary, #5b8cff);
  color: var(--kx-surface, #1a222d);
}

.stage-tile.s-failed {
  color: var(--kx-danger, #f07167);
}

.stage-tile.s-failed .tile-glyph {
  background: var(--kx-danger, #f07167);
  color: var(--kx-surface, #1a222d);
}

.stage-tile.s-pending {
  color: var(--kx-dormant, #a78bfa);
  opacity: 0.78;
  background: var(--kx-dormant-soft, rgba(167, 139, 250, 0.15));
}

.stage-tile.s-skipped {
  opacity: 0.4;
  color: var(--kx-muted, #8b9bb0);
  background: repeating-linear-gradient(
    45deg,
    var(--kx-hatch, rgba(139, 155, 176, 0.09)) 0 3px,
    transparent 3px 6px
  );
}

.summary-info {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-left: auto;
  font-size: 10px;
  color: var(--kx-muted, #8b9bb0);
  flex-shrink: 0;
}

.sess-badge {
  padding: 0 6px;
  border-radius: 999px;
  border: 1px solid var(--kx-border, #2a3544);
  line-height: 15px;
}

.sess-badge.first {
  background: var(--kx-dormant-soft, rgba(167, 139, 250, 0.15));
  border-color: var(--kx-dormant-line, rgba(167, 139, 250, 0.42));
  color: var(--kx-dormant, #a78bfa);
  font-weight: 600;
}

.model-name {
  color: var(--kx-text, #e8eef7);
  font-weight: 600;
}

.tokens,
.duration {
  font-variant-numeric: tabular-nums;
}
</style>

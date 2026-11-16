/**
 * Stage Tree Demo Page — 阶段树功能演示页面
 * 
 * 展示如何集成和使用StageTreeVisualization组件。
 * 包含模拟数据生成器，用于开发和测试。
 * 
 * @page StageTreeDemo
 * @since 2026-11-16
 */
<script setup lang="ts">
import { ref, onMounted, onUnmounted } from 'vue';
import StageTreeVisualization from '@/components/StageTreeVisualization.vue';
import type { StageTreeFrame, StageNode, RouteAttempt, StageStatus, RoutePhase } from '@/types/stageTree';

// Display mode
const displayMode = ref<'full' | 'compact'>('full');

// Current frame being displayed
const currentFrame = ref<StageTreeFrame | null>(null);

// List of frames for history view
const frames = ref<StageTreeFrame[]>([]);

// Selected frame from history
const selectedFrameId = ref<string | null>(null);

// Auto-play simulation
const isPlaying = ref(false);
let playInterval: ReturnType<typeof setInterval> | null = null;

/**
 * Generate a mock StageTreeFrame for demonstration.
 * In production, this data comes from SSE stream.
 */
function generateMockFrame(phase: RoutePhase, includeError = false): StageTreeFrame {
  const requestId = `req_${Date.now().toString(36)}_${Math.random().toString(36).substr(2, 5)}`;
  const now = new Date().toISOString();
  
  const sendStages = [
    { key: 'auth' as const, name: '鉴权/限流', status: 'done' as StageStatus, duration_ms: 12 },
    { key: 'approval' as const, name: '审批', status: 'skipped' as StageStatus },
    { key: 'injection' as const, name: '注入检测', status: 'skipped' as StageStatus },
    { key: 'sanitize' as const, name: '脱敏', status: 'skipped' as StageStatus },
    { key: 'compress' as const, name: '压缩', status: 'skipped' as StageStatus },
    { key: 'route' as const, name: '路由', status: 'done' as StageStatus, duration_ms: 8 },
    { key: 'provider' as const, name: '供应商/令牌池', status: 'pending' as StageStatus },
  ];
  
  const recvStages = [
    { key: 'first_byte' as const, name: '首字节', status: 'pending' as StageStatus },
    { key: 'restore' as const, name: '还原', status: 'pending' as StageStatus },
    { key: 'billing' as const, name: '计量', status: 'pending' as StageStatus },
    { key: 'reply' as const, name: '回复', status: 'pending' as StageStatus },
  ];
  
  // Update stages based on phase
  switch (phase) {
    case 'selecting':
      sendStages[5].status = 'active';
      break;
    case 'attempting':
      sendStages[6].status = 'active';
      sendStages[6].metadata = {
        provider: 'deepseek',
        credential_id: 102,
        credential_label: 'cred-deepseek-pool',
      };
      break;
    case 'responding':
      sendStages[6].status = 'done';
      sendStages[6].duration_ms = 245;
      recvStages[0].status = 'done';
      recvStages[0].duration_ms = 180;
      recvStages[2].status = 'active';
      break;
    case 'done':
      sendStages[6].status = 'done';
      sendStages[6].duration_ms = 245;
      recvStages[0].status = 'done';
      recvStages[0].duration_ms = 180;
      recvStages[1].status = 'skipped';
      recvStages[2].status = 'done';
      recvStages[2].duration_ms = 15;
      recvStages[3].status = 'done';
      recvStages[3].duration_ms = 8;
      break;
    case 'failed':
      sendStages[6].status = 'failed';
      sendStages[6].error_kind = 'upstream_rate_limited';
      sendStages[6].error_message = 'Rate limit exceeded';
      sendStages[6].error_source = 'provider';
      recvStages.forEach(s => s.status = 'skipped');
      break;
  }
  
  const attempts: RouteAttempt[] = [];
  
  if (phase !== 'started' && phase !== 'selecting') {
    attempts.push({
      sequence: 1,
      credential_id: 102,
      credential_label: 'cred-deepseek-pool',
      provider: 'deepseek',
      model: 'deepseek-chat',
      start_time: now,
      success: phase === 'done',
      is_fallback: false,
      latency_ms: phase === 'done' ? 2450 : undefined,
      ttft_ms: phase === 'done' || phase === 'responding' ? 180 : undefined,
      prompt_tokens: phase === 'done' ? 1200 : undefined,
      completion_tokens: phase === 'done' ? 650 : undefined,
      error_kind: phase === 'failed' ? 'upstream_rate_limited' : undefined,
      error_message: phase === 'failed' ? 'Rate limit exceeded' : undefined,
    });
  }
  
  const frame: StageTreeFrame = {
    request_id: requestId,
    tenant_id: 1,
    client_name: 'Claude Code',
    client_type: 'coding_agent',
    client_model: 'deepseek-chat',
    canonical_model: 'deepseek-chat',
    is_auto_model: false,
    phase,
    overall_status: phase === 'done' ? 'done' : phase === 'failed' ? 'failed' : 'active',
    send_stages: sendStages,
    recv_stages: recvStages,
    attempts,
    current_attempt: attempts.length,
    total_latency_ms: phase === 'done' ? 2450 : undefined,
    ttft_ms: phase === 'done' || phase === 'responding' ? 180 : undefined,
    tokens_in: phase === 'done' ? 1200 : undefined,
    tokens_out: phase === 'done' ? 650 : undefined,
    cache_read: phase === 'done' ? 300 : undefined,
    session_id: 'sess_abc123',
    session_turn: Math.random() < 0.3 ? 1 : Math.floor(Math.random() * 5) + 2,
    is_first_turn: Math.random() < 0.3,
    start_time: new Date(Date.now() - 3000).toISOString(),
    updated_at: now,
    version: 1,
  };
  
  return frame;
}

/**
 * Simulate request progression through phases.
 */
async function simulateRequest() {
  const phases: RoutePhase[] = ['started', 'selecting', 'attempting', 'responding', 'done'];
  
  for (const phase of phases) {
    const frame = generateMockFrame(phase);
    currentFrame.value = frame;
    frames.value.unshift(frame);
    
    // Keep only last 20 frames
    if (frames.value.length > 20) {
      frames.value = frames.value.slice(0, 20);
    }
    
    await new Promise(resolve => setTimeout(resolve, 800));
  }
}

/**
 * Simulate a failed request.
 */
async function simulateFailedRequest() {
  const phases: RoutePhase[] = ['started', 'selecting', 'attempting', 'failed'];
  
  for (const phase of phases) {
    const frame = generateMockFrame(phase, true);
    currentFrame.value = frame;
    frames.value.unshift(frame);
    
    if (frames.value.length > 20) {
      frames.value = frames.value.slice(0, 20);
    }
    
    await new Promise(resolve => setTimeout(resolve, 800));
  }
}

/**
 * Toggle auto-play mode.
 */
function toggleAutoPlay() {
  isPlaying.value = !isPlaying.value;
  
  if (isPlaying.value) {
    playInterval = setInterval(() => {
      // 80% success, 20% failure
      if (Math.random() < 0.8) {
        simulateRequest();
      } else {
        simulateFailedRequest();
      }
    }, 4000);
  } else {
    if (playInterval) {
      clearInterval(playInterval);
      playInterval = null;
    }
  }
}

/**
 * Select a frame from history.
 */
function selectFrame(frame: StageTreeFrame) {
  selectedFrameId.value = frame.request_id;
  currentFrame.value = frame;
}

/**
 * Handle stage click event.
 */
function handleStageClick(stage: StageNode) {
  console.log('Stage clicked:', stage);
  alert(`阶段: ${stage.name}\n状态: ${stage.status}\n${stage.duration_ms ? `耗时: ${stage.duration_ms}ms` : ''}`);
}

/**
 * Handle attempt click event.
 */
function handleAttemptClick(index: number) {
  if (!currentFrame.value?.attempts) return;
  const attempt = currentFrame.value.attempts[index];
  console.log('Attempt clicked:', attempt);
  alert(`尝试 #${attempt.sequence}\n供应商: ${attempt.provider}\n${attempt.success ? '成功' : '失败'}`);
}

/**
 * Clear all frames.
 */
function clearFrames() {
  frames.value = [];
  currentFrame.value = null;
  selectedFrameId.value = null;
}

// Initialize with one frame
onMounted(() => {
  const initialFrame = generateMockFrame('started');
  currentFrame.value = initialFrame;
  frames.value.push(initialFrame);
});

// Cleanup on unmount
onUnmounted(() => {
  if (playInterval) {
    clearInterval(playInterval);
  }
});
</script>

<template>
  <div class="stage-tree-demo">
    <div class="demo-header">
      <h1>阶段树可视化演示</h1>
      <p class="demo-description">
        展示请求从鉴权到回复的完整管线执行过程。支持完整和紧凑两种显示模式。
      </p>
    </div>

    <div class="demo-controls">
      <div class="control-group">
        <label>显示模式:</label>
        <div class="mode-switch">
          <button
            :class="{ active: displayMode === 'full' }"
            @click="displayMode = 'full'"
          >
            完整
          </button>
          <button
            :class="{ active: displayMode === 'compact' }"
            @click="displayMode = 'compact'"
          >
            紧凑
          </button>
        </div>
      </div>

      <div class="control-group">
        <label>模拟操作:</label>
        <div class="action-buttons">
          <button class="btn btn-primary" @click="simulateRequest">
            ▶ 模拟成功请求
          </button>
          <button class="btn btn-danger" @click="simulateFailedRequest">
            ✕ 模拟失败请求
          </button>
          <button 
            class="btn"
            :class="{ 'btn-success': isPlaying }"
            @click="toggleAutoPlay"
          >
            {{ isPlaying ? '⏸ 停止自动播放' : '▶ 自动播放' }}
          </button>
          <button class="btn btn-secondary" @click="clearFrames">
            🗑 清空历史
          </button>
        </div>
      </div>
    </div>

    <div class="demo-content">
      <!-- Main visualization area -->
      <div class="visualization-panel">
        <h2>当前请求</h2>
        <div v-if="currentFrame" class="current-frame">
          <StageTreeVisualization
            :frame="currentFrame"
            :mode="displayMode"
            @stage-click="handleStageClick"
            @attempt-click="handleAttemptClick"
          />
        </div>
        <div v-else class="empty-state">
          <p>点击上方按钮生成模拟请求</p>
        </div>
      </div>

      <!-- History sidebar -->
      <div class="history-panel">
        <h2>请求历史 ({{ frames.length }})</h2>
        <div class="frame-list">
          <div
            v-for="frame in frames"
            :key="frame.request_id"
            class="frame-item"
            :class="{ 
              selected: selectedFrameId === frame.request_id,
              success: frame.overall_status === 'done',
              failed: frame.overall_status === 'failed',
            }"
            @click="selectFrame(frame)"
          >
            <div class="frame-header">
              <span class="frame-id">{{ frame.request_id.slice(0, 12) }}</span>
              <span class="frame-phase">{{ frame.phase }}</span>
            </div>
            <div class="frame-info">
              <span class="frame-model">{{ frame.client_model }}</span>
              <span v-if="frame.total_latency_ms" class="frame-latency">
                {{ frame.total_latency_ms }}ms
              </span>
            </div>
          </div>
        </div>
      </div>
    </div>

    <div class="demo-footer">
      <div class="footer-section">
        <h3>功能说明</h3>
        <ul>
          <li><strong>完整模式</strong>: 显示详细的阶段信息、路由尝试和会话上下文</li>
          <li><strong>紧凑模式</strong>: 单行显示所有阶段，适合一屏查看多个请求</li>
          <li><strong>实时更新</strong>: 阶段状态随请求进展实时更新（生产环境通过SSE推送）</li>
          <li><strong>错误追踪</strong>: 自动分类错误来源（网关/供应商/其它）</li>
        </ul>
      </div>
      
      <div class="footer-section">
        <h3>技术栈</h3>
        <ul>
          <li>后端: Go + SSE推送 (admin/live_stage_tree_projection.go)</li>
          <li>前端: Vue 3 + TypeScript (StageTreeVisualization.vue)</li>
          <li>数据流: RouteTrace → ProjectStageTree() → StageTreeFrame → SSE</li>
          <li>样式: CSS变量，支持明暗主题切换</li>
        </ul>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* 契约 A（全屏）：禁止根容器 max-width + margin:0 auto（e081374a / PluginSettings 同款）。 */
.stage-tree-demo {
  padding: 20px;
  max-width: none;
  width: 100%;
  color: var(--kx-text, #e8eef7);
  background: var(--kx-bg, #0f141c);
  min-height: 100vh;
  box-sizing: border-box;
}

.demo-header {
  margin-bottom: 24px;
  padding-bottom: 16px;
  border-bottom: 1px solid var(--kx-border, #2a3544);
}

.demo-header h1 {
  margin: 0 0 8px 0;
  font-size: 28px;
  font-weight: 700;
  color: var(--kx-text, #e8eef7);
}

.demo-description {
  margin: 0;
  font-size: 14px;
  color: var(--kx-muted, #8b9bb0);
}

.demo-controls {
  display: flex;
  flex-direction: column;
  gap: 16px;
  margin-bottom: 24px;
  padding: 16px;
  background: var(--kx-surface, #1a222d);
  border-radius: 8px;
  border: 1px solid var(--kx-border, #2a3544);
}

.control-group {
  display: flex;
  align-items: center;
  gap: 12px;
}

.control-group label {
  font-size: 13px;
  font-weight: 600;
  color: var(--kx-muted, #8b9bb0);
  min-width: 80px;
}

.mode-switch {
  display: flex;
  gap: 0;
  border: 1px solid var(--kx-border, #2a3544);
  border-radius: 6px;
  overflow: hidden;
}

.mode-switch button {
  padding: 6px 16px;
  font-size: 12px;
  font-weight: 600;
  color: var(--kx-text, #e8eef7);
  background: var(--kx-surface, #1a222d);
  border: none;
  cursor: pointer;
  transition: all 0.2s;
}

.mode-switch button + button {
  border-left: 1px solid var(--kx-border, #2a3544);
}

.mode-switch button:hover {
  background: var(--kx-primary-soft, rgba(91, 140, 255, 0.16));
}

.mode-switch button.active {
  background: var(--kx-primary, #5b8cff);
  color: var(--kx-text-on-primary, #fff);
}

.action-buttons {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}

.btn {
  padding: 6px 12px;
  font-size: 12px;
  font-weight: 600;
  color: var(--kx-text, #e8eef7);
  background: var(--kx-surface, #1a222d);
  border: 1px solid var(--kx-border, #2a3544);
  border-radius: 6px;
  cursor: pointer;
  transition: all 0.2s;
}

.btn:hover {
  border-color: var(--kx-primary, #5b8cff);
  background: var(--kx-primary-soft, rgba(91, 140, 255, 0.16));
}

.btn-primary {
  background: var(--kx-primary, #5b8cff);
  border-color: var(--kx-primary, #5b8cff);
  color: var(--kx-text-on-primary, #fff);
}

.btn-primary:hover {
  background: var(--kx-primary-hover, #7aa3ff);
}

.btn-danger {
  background: var(--kx-danger, #f07167);
  border-color: var(--kx-danger, #f07167);
  color: var(--kx-text-on-primary, #fff);
}

.btn-success {
  background: var(--kx-success, #3ecf8e);
  border-color: var(--kx-success, #3ecf8e);
  color: var(--kx-text-on-primary, #fff);
}

.btn-secondary {
  color: var(--kx-muted, #8b9bb0);
}

.demo-content {
  display: grid;
  grid-template-columns: 1fr 320px;
  gap: 20px;
  margin-bottom: 24px;
}

.visualization-panel,
.history-panel {
  background: var(--kx-surface, #1a222d);
  border-radius: 8px;
  border: 1px solid var(--kx-border, #2a3544);
  padding: 16px;
}

.visualization-panel h2,
.history-panel h2 {
  margin: 0 0 16px 0;
  font-size: 16px;
  font-weight: 650;
  color: var(--kx-text, #e8eef7);
}

.current-frame {
  min-height: 400px;
}

.empty-state {
  display: flex;
  align-items: center;
  justify-content: center;
  min-height: 400px;
  color: var(--kx-muted, #8b9bb0);
  font-size: 14px;
}

.frame-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
  max-height: 600px;
  overflow-y: auto;
}

.frame-item {
  padding: 10px 12px;
  background: var(--kx-chip-bg, rgba(139, 155, 176, 0.12));
  border: 1px solid var(--kx-border, #2a3544);
  border-radius: 6px;
  cursor: pointer;
  transition: all 0.2s;
}

.frame-item:hover {
  border-color: var(--kx-primary, #5b8cff);
}

.frame-item.selected {
  border-color: var(--kx-primary, #5b8cff);
  background: var(--kx-primary-soft, rgba(91, 140, 255, 0.16));
}

.frame-item.success {
  border-left: 3px solid var(--kx-success, #3ecf8e);
}

.frame-item.failed {
  border-left: 3px solid var(--kx-danger, #f07167);
}

.frame-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 4px;
}

.frame-id {
  font-size: 11px;
  font-family: ui-monospace, monospace;
  color: var(--kx-text, #e8eef7);
  font-weight: 600;
}

.frame-phase {
  font-size: 10px;
  padding: 1px 6px;
  border-radius: 3px;
  background: var(--kx-primary-soft, rgba(91, 140, 255, 0.16));
  color: var(--kx-primary, #5b8cff);
}

.frame-info {
  display: flex;
  justify-content: space-between;
  font-size: 11px;
  color: var(--kx-muted, #8b9bb0);
}

.frame-model {
  font-weight: 600;
}

.frame-latency {
  font-variant-numeric: tabular-nums;
}

.demo-footer {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 20px;
  padding: 20px;
  background: var(--kx-surface, #1a222d);
  border-radius: 8px;
  border: 1px solid var(--kx-border, #2a3544);
}

.footer-section h3 {
  margin: 0 0 12px 0;
  font-size: 14px;
  font-weight: 650;
  color: var(--kx-text, #e8eef7);
}

.footer-section ul {
  margin: 0;
  padding-left: 20px;
  list-style: disc;
}

.footer-section li {
  margin: 6px 0;
  font-size: 12px;
  color: var(--kx-muted, #8b9bb0);
  line-height: 1.6;
}

.footer-section li strong {
  color: var(--kx-text, #e8eef7);
  font-weight: 600;
}

@media (max-width: 1200px) {
  .demo-content {
    grid-template-columns: 1fr;
  }
  
  .history-panel {
    order: -1;
  }
  
  .frame-list {
    max-height: 300px;
  }
  
  .demo-footer {
    grid-template-columns: 1fr;
  }
}
</style>

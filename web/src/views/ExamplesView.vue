<script setup lang="ts">
import { ref, computed } from 'vue'
import { resolveRouting } from '../api'
import ModelPicker from '../components/ModelPicker.vue'
import { useGatewayApiKey } from '../composables/useGatewayApiKey'

const { apiKey: gatewayApiKey } = useGatewayApiKey()

const selectedModel = ref('glm-4-flash')
const realApiKey = computed(() => gatewayApiKey.value || '')
const maskedApiKey = computed(() => {
  const k = realApiKey.value
  if (!k) return '<YOUR_API_KEY>'
  if (k.length <= 16) return `${k.slice(0, 4)}****`
  return `${k.slice(0, 12)}****${k.slice(-4)}`
})
const baseUrl = computed(() => window.location.origin + '/v1')

const curlExample = computed(() => `curl ${baseUrl.value}/chat/completions \\
  -H "Content-Type: application/json" \\
  -H "Authorization: Bearer ${maskedApiKey.value}" \\
  -d '{
    "model": "${selectedModel.value}",
    "messages": [
      {"role": "system", "content": "You are a helpful assistant."},
      {"role": "user", "content": "Hello!"}
    ],
    "max_tokens": 256
  }'`)

const pythonExample = computed(() => `from openai import OpenAI

client = OpenAI(
    api_key="${maskedApiKey.value}",
    base_url="${baseUrl.value}",
)

response = client.chat.completions.create(
    model="${selectedModel.value}",
    messages=[
        {"role": "system", "content": "You are a helpful assistant."},
        {"role": "user", "content": "Hello!"},
    ],
    max_tokens=256,
)

print(response.choices[0].message.content)`)

const streamExample = computed(() => `from openai import OpenAI

client = OpenAI(
    api_key="${maskedApiKey.value}",
    base_url="${baseUrl.value}",
)

with client.chat.completions.stream(
    model="${selectedModel.value}",
    messages=[{"role": "user", "content": "Count to 5."}],
    max_tokens=64,
) as stream:
    for chunk in stream:
        delta = chunk.choices[0].delta.content or ""
        print(delta, end="", flush=True)`)

const jsExample = computed(() => `import OpenAI from "openai";

const client = new OpenAI({
  apiKey: "${maskedApiKey.value}",
  baseURL: "${baseUrl.value}",
  dangerouslyAllowBrowser: true,
});

const response = await client.chat.completions.create({
  model: "${selectedModel.value}",
  messages: [{ role: "user", content: "Hello!" }],
  max_tokens: 256,
});

console.log(response.choices[0].message.content);`)

const listModelsExample = computed(() => `curl ${baseUrl.value}/models \\
  -H "Authorization: Bearer ${maskedApiKey.value}"`)

const copied = ref<string | null>(null)
function copyCode(key: string, text: string) {
  navigator.clipboard.writeText(text)
  copied.value = key
  setTimeout(() => { copied.value = null }, 2000)
}

type ExampleId = 'curl' | 'python' | 'stream' | 'js' | 'models'
type TestKind = 'chat' | 'stream' | 'models'

interface DrawerState {
  open: boolean
  exampleId: ExampleId
  testKind: TestKind
  loading: boolean
  status: number
  latency: number
  error: string
  requestBody: string
  responseBody: string
  routing: string
}

const emptyDrawer = (exampleId: ExampleId, testKind: TestKind): DrawerState => ({
  open: false,
  exampleId,
  testKind,
  loading: false,
  status: 0,
  latency: 0,
  error: '',
  requestBody: '',
  responseBody: '',
  routing: '',
})

const drawers = ref<Record<ExampleId, DrawerState>>({
  curl: emptyDrawer('curl', 'chat'),
  python: emptyDrawer('python', 'chat'),
  stream: emptyDrawer('stream', 'stream'),
  js: emptyDrawer('js', 'chat'),
  models: emptyDrawer('models', 'models'),
})

async function runTest(exampleId: ExampleId) {
  const d = drawers.value[exampleId]
  d.open = true
  d.loading = true
  d.status = 0
  d.latency = 0
  d.error = ''
  d.responseBody = ''
  d.routing = ''
  d.requestBody = ''

  let reqBody: Record<string, unknown> | null = null
  let url = `${baseUrl.value}/chat/completions`
  let method = 'POST'
  let headers: Record<string, string> = {
    'Content-Type': 'application/json',
    'Authorization': `Bearer ${realApiKey.value}`,
  }

  if (d.testKind === 'models') {
    url = `${baseUrl.value}/models`
    method = 'GET'
    headers = { 'Authorization': `Bearer ${realApiKey.value}` }
    d.requestBody = `GET ${url}`
  } else {
    const isStream = d.testKind === 'stream'
    const msgs = isStream
      ? [{ role: 'user', content: 'Count to 5.' }]
      : [
          { role: 'system', content: 'You are a helpful assistant.' },
          { role: 'user', content: 'Hello!' },
        ]
    reqBody = {
      model: selectedModel.value,
      messages: msgs,
      max_tokens: isStream ? 64 : 256,
      ...(isStream ? { stream: true } : {}),
    }
    d.requestBody = JSON.stringify(reqBody, null, 2)
  }

  const start = performance.now()
  try {
    const resp = await fetch(url, {
      method,
      headers,
      body: reqBody ? JSON.stringify(reqBody) : undefined,
    })
    d.latency = Math.round(performance.now() - start)
    d.status = resp.status
    const text = await resp.text()

    if (d.testKind === 'stream') {
      const lines = text.split('\n').filter(l => l.startsWith('data: '))
      let full = ''
      for (const line of lines) {
        const json = line.slice(6).trim()
        if (json === '[DONE]') break
        try {
          const obj = JSON.parse(json)
          const delta = obj?.choices?.[0]?.delta?.content
          if (delta) full += delta
        } catch { /* skip */ }
      }
      d.responseBody = full || text.slice(0, 2000)
    } else {
      try {
        const parsed = JSON.parse(text)
        d.responseBody = JSON.stringify(parsed, null, 2).slice(0, 4000)
      } catch {
        d.responseBody = text.slice(0, 4000)
      }
    }

    if (realApiKey.value) {
      try {
        const route = await resolveRouting(selectedModel.value)
        d.routing = `模型: ${route.client_model}\n标准名: ${route.canonical_name || '未映射'}\n路径: ${route.resolution_path}\n候选数: ${route.candidates?.length || 0}\n原始模型: ${route.raw_models?.join(', ') || '无'}`
      } catch {
        d.routing = '路由信息获取失败'
      }
    }
  } catch (err: any) {
    d.latency = Math.round(performance.now() - start)
    d.error = err.message || String(err)
  } finally {
    d.loading = false
  }
}

function closeDrawer(exampleId: ExampleId) {
  drawers.value[exampleId].open = false
}

const exampleTitle: Record<ExampleId, string> = {
  curl: 'cURL Chat 测试',
  python: 'Python Chat 测试',
  stream: '流式输出测试',
  js: 'JavaScript Chat 测试',
  models: '列出模型测试',
}

type ClientGuideId = 'cherry' | 'cursor' | 'claude' | 'roocode'
const openGuide = ref<ClientGuideId | null>('cherry')

interface ClientGuide {
  id: ClientGuideId
  name: string
  icon: string
  steps: string[]
  mcpNote?: string
}

const clientGuides = computed((): ClientGuide[] => [
  {
    id: 'cherry',
    name: 'Cherry Studio',
    icon: '🍒',
    steps: [
      '打开 Cherry Studio → 设置 → 模型服务 → 添加 OpenAI 兼容提供商',
      `API 地址（Base URL）：${baseUrl.value}`,
      'API Key：在左侧「API 密钥」创建或复制你的 sk-* 密钥',
      '模型：填写网关支持的模型 ID（如 glm-4-flash），或使用 auto 开启自动路由',
      '保存后可在对话窗口选择该提供商下的模型',
    ],
    mcpNote: 'MCP：设置 → MCP 服务器 → 导入配置。Memora 端点 https://mcp.kxpms.cn/memora/mcp，Header 填 Authorization: Bearer <MEMORA_API_KEY>（与 LLM 密钥不同，需在 ACC 申请 memora scope）。',
  },
  {
    id: 'cursor',
    name: 'Cursor IDE',
    icon: '⌨️',
    steps: [
      'Cursor Settings（Cmd/Ctrl+Shift+J）→ Models',
      'OpenAI API Key → 填入你的 sk-* 网关密钥',
      `开启 Override OpenAI Base URL → ${baseUrl.value}`,
      '点击 Verify 验证连通性',
      'Add Custom Model：添加 glm-5.1、minimax-m3、deepseek-v4-pro 等（完整列表见下方「列出模型」示例）',
      '可选：请求头 X-Client-Profile: cursor、X-Request-Mode: agent 用于 Agent 模式优化',
    ],
    mcpNote: 'MCP：Cursor Settings → MCP，可导入 mcp.kxpms.cn 的 acc/memora/trendradar 服务（需对应 scope 的 API Key）。',
  },
  {
    id: 'claude',
    name: 'Claude Desktop',
    icon: '🤖',
    steps: [
      'Claude Desktop 本身走 Anthropic 账号；若需接入开轩 MCP 工具：',
      '编辑 claude_desktop_config.json（macOS: ~/Library/Application Support/Claude/）',
      '添加 HTTP MCP：url https://mcp.kxpms.cn/memora/mcp，headers.Authorization = Bearer <MEMORA_API_KEY>',
      'Quit & Reopen Claude Desktop 生效（需 ≥ 1.0 版本）',
      `LLM 对话走网关：使用支持 OpenAI 协议的客户端，Base URL ${baseUrl.value}`,
    ],
  },
  {
    id: 'roocode',
    name: 'Roo Code / VS Code',
    icon: '🧩',
    steps: [
      '扩展设置中选择 OpenAI Compatible 提供商',
      `Base URL：${baseUrl.value}`,
      'API Key：你的 sk-* 网关密钥',
      '模型：与 Cherry Studio 相同，支持 auto 或具体模型 ID',
      '可选 Header：X-Client-Profile: roocode',
    ],
    mcpNote: 'VS Code / Roo Code MCP：在 mcp.json 中配置 streamable-http 类型端点，参考仓库 docs/clients/roocode-mcp.json。',
  },
])

function toggleGuide(id: ClientGuideId) {
  openGuide.value = openGuide.value === id ? null : id
}
</script>

<template>
  <div>
    <div class="page-header">
      <h2>接入指南</h2>
    </div>

    <p style="color:var(--muted);margin-bottom:12px">
      网关兼容 OpenAI API 协议。将 <code>base_url</code> 指向此网关即可使用任意支持的模型；也可在侧栏「对话」直接网页聊天。
    </p>

    <h3 class="section-heading">常用客户端配置</h3>
    <div class="guide-list">
      <div v-for="g in clientGuides" :key="g.id" class="card guide-card">
        <button type="button" class="guide-header" @click="toggleGuide(g.id)">
          <span class="guide-title"><span class="guide-icon">{{ g.icon }}</span> {{ g.name }}</span>
          <span class="guide-chevron">{{ openGuide === g.id ? '▼' : '▶' }}</span>
        </button>
        <div v-show="openGuide === g.id" class="guide-body">
          <ol class="guide-steps">
            <li v-for="(step, i) in g.steps" :key="i">{{ step }}</li>
          </ol>
          <p v-if="g.mcpNote" class="guide-mcp">{{ g.mcpNote }}</p>
        </div>
      </div>
    </div>

    <h3 class="section-heading" style="margin-top:28px">API 请求示例</h3>
    <p v-if="realApiKey" class="key-hint">
      示例代码中的 API Key 已脱敏显示；点击「测试」将使用当前登录密钥。复制示例后请自行替换为真实 Key。
      <button type="button" class="btn btn-ghost btn-sm" @click="copyCode('realkey', realApiKey)">
        {{ copied === 'realkey' ? '已复制!' : '复制完整 Key' }}
      </button>
    </p>

    <div class="card" style="margin-bottom:20px">
      <div style="display:flex;align-items:center;gap:16px;flex-wrap:wrap">
        <div style="font-weight:500;white-space:nowrap">选择示例模型：</div>
        <div style="max-width:360px;flex:1;min-width:240px">
          <ModelPicker
            v-model="selectedModel"
            placeholder="选择模型…"
            title="选择示例模型"
          />
        </div>
        <div style="font-size:12px;color:var(--muted)">
          当前模型: <code style="color:var(--accent)">{{ selectedModel }}</code>
        </div>
        <div style="font-size:12px;color:var(--muted)">
          Base URL: <code>{{ baseUrl }}</code>
        </div>
      </div>
    </div>

    <div class="card" style="margin-bottom:20px">
      <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:12px">
        <h4 style="margin:0">cURL — Chat Completions</h4>
        <div style="display:flex;gap:8px">
          <button class="btn btn-ghost btn-sm" @click="copyCode('curl', curlExample)">
            {{ copied === 'curl' ? '已复制!' : '复制' }}
          </button>
          <button class="btn btn-primary btn-sm" @click="runTest('curl')" :disabled="drawers.curl.loading">
            {{ drawers.curl.loading ? '测试中...' : '测试' }}
          </button>
        </div>
      </div>
      <pre class="code-block">{{ curlExample }}</pre>
    </div>

    <div class="card" style="margin-bottom:20px">
      <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:12px">
        <h4 style="margin:0">Python (openai SDK)</h4>
        <div style="display:flex;gap:8px">
          <button class="btn btn-ghost btn-sm" @click="copyCode('python', pythonExample)">
            {{ copied === 'python' ? '已复制!' : '复制' }}
          </button>
          <button class="btn btn-primary btn-sm" @click="runTest('python')" :disabled="drawers.python.loading">
            {{ drawers.python.loading ? '测试中...' : '测试' }}
          </button>
        </div>
      </div>
      <pre class="code-block">{{ pythonExample }}</pre>
    </div>

    <div class="card" style="margin-bottom:20px">
      <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:12px">
        <h4 style="margin:0">Python — 流式输出 (Streaming)</h4>
        <div style="display:flex;gap:8px">
          <button class="btn btn-ghost btn-sm" @click="copyCode('stream', streamExample)">
            {{ copied === 'stream' ? '已复制!' : '复制' }}
          </button>
          <button class="btn btn-primary btn-sm" @click="runTest('stream')" :disabled="drawers.stream.loading">
            {{ drawers.stream.loading ? '测试中...' : '测试' }}
          </button>
        </div>
      </div>
      <pre class="code-block">{{ streamExample }}</pre>
    </div>

    <div class="card" style="margin-bottom:20px">
      <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:12px">
        <h4 style="margin:0">JavaScript / TypeScript (openai SDK)</h4>
        <div style="display:flex;gap:8px">
          <button class="btn btn-ghost btn-sm" @click="copyCode('js', jsExample)">
            {{ copied === 'js' ? '已复制!' : '复制' }}
          </button>
          <button class="btn btn-primary btn-sm" @click="runTest('js')" :disabled="drawers.js.loading">
            {{ drawers.js.loading ? '测试中...' : '测试' }}
          </button>
        </div>
      </div>
      <pre class="code-block">{{ jsExample }}</pre>
    </div>

    <div class="card" style="margin-bottom:20px">
      <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:12px">
        <h4 style="margin:0">cURL — 列出可用模型</h4>
        <div style="display:flex;gap:8px">
          <button class="btn btn-ghost btn-sm" @click="copyCode('models', listModelsExample)">
            {{ copied === 'models' ? '已复制!' : '复制' }}
          </button>
          <button class="btn btn-primary btn-sm" @click="runTest('models')" :disabled="drawers.models.loading">
            {{ drawers.models.loading ? '测试中...' : '测试' }}
          </button>
        </div>
      </div>
      <pre class="code-block">{{ listModelsExample }}</pre>
    </div>

    <template v-for="eid in (['curl','python','stream','js','models'] as ExampleId[])" :key="eid">
      <div v-if="drawers[eid].open" class="drawer-backdrop" @click="closeDrawer(eid)">
        <div class="drawer-panel card" @click.stop>
          <div class="drawer-header">
            <h4 style="margin:0">{{ exampleTitle[eid] }}</h4>
            <div style="display:flex;align-items:center;gap:12px">
              <span style="font-size:13px;color:var(--muted)">模型: <code>{{ selectedModel }}</code></span>
              <button class="btn btn-ghost btn-sm" @click="closeDrawer(eid)">关闭 ✕</button>
            </div>
          </div>

          <div v-if="drawers[eid].loading" class="drawer-loading">
            <div class="spinner"></div>
            <span>请求中...</span>
          </div>

          <template v-else>
            <div class="drawer-meta">
              <div class="meta-item">
                <span class="meta-label">状态</span>
                <span :class="drawers[eid].status >= 200 && drawers[eid].status < 300 ? 'badge badge-green' : 'badge badge-red'">{{ drawers[eid].status || '—' }}</span>
              </div>
              <div class="meta-item">
                <span class="meta-label">延迟</span>
                <span>{{ drawers[eid].latency }}ms</span>
              </div>
              <div class="meta-item">
                <span class="meta-label">类型</span>
                <code>{{ drawers[eid].testKind }}</code>
              </div>
            </div>

            <div v-if="drawers[eid].error" class="alert alert-danger" style="margin:0 0 12px">
              {{ drawers[eid].error }}
            </div>

            <div v-if="drawers[eid].requestBody" class="drawer-section">
              <div class="section-title">请求内容</div>
              <pre class="code-block compact">{{ drawers[eid].requestBody }}</pre>
            </div>

            <div v-if="drawers[eid].responseBody" class="drawer-section">
              <div class="section-title">响应内容</div>
              <pre class="code-block compact">{{ drawers[eid].responseBody }}</pre>
            </div>

            <div v-if="drawers[eid].routing" class="drawer-section">
              <div class="section-title">路由信息</div>
              <pre class="code-block compact">{{ drawers[eid].routing }}</pre>
            </div>
          </template>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.code-block {
  background: #1a1d23;
  color: #e2e8f0;
  border-radius: 8px;
  padding: 16px;
  overflow-x: auto;
  font-size: 13px;
  line-height: 1.6;
  white-space: pre;
  margin: 0;
}
.code-block.compact {
  padding: 12px;
  font-size: 12px;
  max-height: 240px;
  overflow: auto;
}

.drawer-loading {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 12px;
  padding: 40px 0;
  color: var(--muted);
}

.spinner {
  width: 20px;
  height: 20px;
  border: 2px solid var(--border);
  border-top-color: var(--accent);
  border-radius: 50%;
  animation: spin 0.6s linear infinite;
}

@keyframes spin {
  to { transform: rotate(360deg); }
}

.drawer-meta {
  display: flex;
  gap: 20px;
  margin-bottom: 16px;
  font-size: 13px;
  flex-wrap: wrap;
}

.meta-item {
  display: flex;
  align-items: center;
  gap: 6px;
}

.meta-label {
  color: var(--muted);
}

.key-hint {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
  font-size: 12px;
  color: var(--muted);
  margin-bottom: 16px;
}

.section-heading {
  font-size: 15px;
  font-weight: 600;
  margin: 0 0 12px;
}

.guide-list {
  display: flex;
  flex-direction: column;
  gap: 10px;
  margin-bottom: 8px;
}

.guide-card {
  padding: 0;
  overflow: hidden;
}

.guide-header {
  width: 100%;
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 14px 16px;
  background: none;
  border: none;
  color: var(--text);
  font-size: 14px;
  font-weight: 500;
  cursor: pointer;
  text-align: left;
}

.guide-header:hover {
  background: rgba(255, 255, 255, 0.03);
}

.guide-title {
  display: flex;
  align-items: center;
  gap: 8px;
}

.guide-icon {
  font-size: 18px;
}

.guide-chevron {
  color: var(--muted);
  font-size: 12px;
}

.guide-body {
  padding: 0 16px 16px;
  border-top: 1px solid var(--border);
}

.guide-steps {
  margin: 12px 0 0;
  padding-left: 20px;
  font-size: 13px;
  line-height: 1.7;
  color: var(--text);
}

.guide-mcp {
  margin: 12px 0 0;
  padding: 10px 12px;
  font-size: 12px;
  line-height: 1.6;
  color: var(--muted);
  background: rgba(99, 102, 241, 0.08);
  border-radius: 6px;
}
</style>

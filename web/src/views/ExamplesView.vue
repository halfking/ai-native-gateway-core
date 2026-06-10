<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { getProviders, type Provider } from '../api'
import { store } from '../store'
import ModelPicker from '../components/ModelPicker.vue'

const providers = ref<Provider[]>([])
const selectedModel = ref('gpt-4o-mini')
const apiKey = computed(() => store.apiKey || '<YOUR_API_KEY>')

const baseUrl = computed(() => {
  return window.location.origin + '/v1'
})

const curlExample = computed(() => `curl ${baseUrl.value}/chat/completions \\
  -H "Content-Type: application/json" \\
  -H "Authorization: Bearer ${apiKey.value}" \\
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
    api_key="${apiKey.value}",
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
    api_key="${apiKey.value}",
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
  apiKey: "${apiKey.value}",
  baseURL: "${baseUrl.value}",
  dangerouslyAllowBrowser: true,  // only for demo
});

const response = await client.chat.completions.create({
  model: "${selectedModel.value}",
  messages: [{ role: "user", content: "Hello!" }],
  max_tokens: 256,
});

console.log(response.choices[0].message.content);`)

const listModelsExample = computed(() => `curl ${baseUrl.value}/models \\
  -H "Authorization: Bearer ${apiKey.value}"`)

const copied = ref<string | null>(null)
function copyCode(key: string, text: string) {
  navigator.clipboard.writeText(text)
  copied.value = key
  setTimeout(() => { copied.value = null }, 2000)
}

type TestKind = 'chat' | 'stream' | 'models'
type TestResult = { status: number; body: string; latency: number; error?: string }

const testing = ref<TestKind | null>(null)
const testResult = ref<TestResult | null>(null)
const testKind = ref<TestKind | null>(null)

async function testRequest(kind: TestKind) {
  testing.value = kind
  testResult.value = null
  testKind.value = kind
  const start = performance.now()
  try {
    let resp: Response
    if (kind === 'models') {
      resp = await fetch(`${baseUrl.value}/models`, {
        method: 'GET',
        headers: { 'Authorization': `Bearer ${apiKey.value}` },
      })
    } else if (kind === 'stream') {
      resp = await fetch(`${baseUrl.value}/chat/completions`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${apiKey.value}`,
        },
        body: JSON.stringify({
          model: selectedModel.value,
          messages: [{ role: 'user', content: 'Count to 5.' }],
          max_tokens: 64,
          stream: true,
        }),
      })
    } else {
      resp = await fetch(`${baseUrl.value}/chat/completions`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${apiKey.value}`,
        },
        body: JSON.stringify({
          model: selectedModel.value,
          messages: [
            { role: 'system', content: 'You are a helpful assistant.' },
            { role: 'user', content: 'Hello!' },
          ],
          max_tokens: 256,
        }),
      })
    }
    const latency = Math.round(performance.now() - start)
    const text = await resp.text()
    testResult.value = {
      status: resp.status,
      body: text.slice(0, 4000),
      latency,
    }
  } catch (err: any) {
    testResult.value = {
      status: 0,
      body: '',
      latency: Math.round(performance.now() - start),
      error: err.message || String(err),
    }
  } finally {
    testing.value = null
  }
}

function closeTest() {
  testResult.value = null
  testKind.value = null
}

const testTitle = computed(() => {
  switch (testKind.value) {
    case 'chat': return 'Chat Completions 测试结果'
    case 'stream': return 'Streaming 测试结果'
    case 'models': return 'List Models 测试结果'
    default: return '测试结果'
  }
})

onMounted(async () => {
  try {
    providers.value = await getProviders()
  } catch { /* ignore */ }
})
</script>

<template>
  <div>
    <div class="page-header">
      <h2>请求示例</h2>
    </div>

    <p style="color:var(--muted);margin-bottom:20px">
      网关兼容 OpenAI API 协议。将 <code>base_url</code> 指向此网关即可使用任意支持的模型。
    </p>

    <!-- Model selector -->
    <div class="card" style="margin-bottom:20px">
      <div style="display:flex;align-items:center;gap:16px;flex-wrap:wrap">
        <div style="font-weight:500;white-space:nowrap">选择示例模型：</div>
        <div style="max-width:320px;flex:1;min-width:240px">
          <ModelPicker
            v-model="selectedModel"
            :allow-free-text="true"
            placeholder="选择或输入模型名"
          />
        </div>
        <div style="font-size:12px;color:var(--muted)">
          Base URL: <code>{{ baseUrl }}</code>
        </div>
      </div>
    </div>

    <!-- cURL -->
    <div class="card" style="margin-bottom:20px">
      <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:12px">
        <h4 style="margin:0">cURL — Chat Completions</h4>
        <div style="display:flex;gap:8px">
          <button class="btn btn-ghost btn-sm" @click="copyCode('curl', curlExample)">
            {{ copied === 'curl' ? '已复制!' : '复制' }}
          </button>
          <button class="btn btn-primary btn-sm" @click="testRequest('chat')" :disabled="testing !== null">
            {{ testing === 'chat' ? '测试中...' : '测试' }}
          </button>
        </div>
      </div>
      <pre class="code-block">{{ curlExample }}</pre>
    </div>

    <!-- Python -->
    <div class="card" style="margin-bottom:20px">
      <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:12px">
        <h4 style="margin:0">Python (openai SDK)</h4>
        <div style="display:flex;gap:8px">
          <button class="btn btn-ghost btn-sm" @click="copyCode('python', pythonExample)">
            {{ copied === 'python' ? '已复制!' : '复制' }}
          </button>
          <button class="btn btn-primary btn-sm" @click="testRequest('chat')" :disabled="testing !== null">
            {{ testing === 'chat' ? '测试中...' : '测试' }}
          </button>
        </div>
      </div>
      <pre class="code-block">{{ pythonExample }}</pre>
    </div>

    <!-- Python streaming -->
    <div class="card" style="margin-bottom:20px">
      <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:12px">
        <h4 style="margin:0">Python — 流式输出 (Streaming)</h4>
        <div style="display:flex;gap:8px">
          <button class="btn btn-ghost btn-sm" @click="copyCode('stream', streamExample)">
            {{ copied === 'stream' ? '已复制!' : '复制' }}
          </button>
          <button class="btn btn-primary btn-sm" @click="testRequest('stream')" :disabled="testing !== null">
            {{ testing === 'stream' ? '测试中...' : '测试' }}
          </button>
        </div>
      </div>
      <pre class="code-block">{{ streamExample }}</pre>
    </div>

    <!-- JavaScript -->
    <div class="card" style="margin-bottom:20px">
      <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:12px">
        <h4 style="margin:0">JavaScript / TypeScript (openai SDK)</h4>
        <div style="display:flex;gap:8px">
          <button class="btn btn-ghost btn-sm" @click="copyCode('js', jsExample)">
            {{ copied === 'js' ? '已复制!' : '复制' }}
          </button>
          <button class="btn btn-primary btn-sm" @click="testRequest('chat')" :disabled="testing !== null">
            {{ testing === 'chat' ? '测试中...' : '测试' }}
          </button>
        </div>
      </div>
      <pre class="code-block">{{ jsExample }}</pre>
    </div>

    <!-- List models -->
    <div class="card" style="margin-bottom:20px">
      <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:12px">
        <h4 style="margin:0">cURL — 列出可用模型</h4>
        <div style="display:flex;gap:8px">
          <button class="btn btn-ghost btn-sm" @click="copyCode('models', listModelsExample)">
            {{ copied === 'models' ? '已复制!' : '复制' }}
          </button>
          <button class="btn btn-primary btn-sm" @click="testRequest('models')" :disabled="testing !== null">
            {{ testing === 'models' ? '测试中...' : '测试' }}
          </button>
        </div>
      </div>
      <pre class="code-block">{{ listModelsExample }}</pre>
    </div>

    <!-- Test result modal -->
    <div v-if="testResult" class="modal-overlay" @click.self="closeTest">
      <div class="modal card" style="max-width:780px;width:90vw;max-height:85vh;display:flex;flex-direction:column">
        <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:12px">
          <h4 style="margin:0">{{ testTitle }}</h4>
          <button class="btn btn-ghost btn-sm" @click="closeTest">关闭</button>
        </div>
        <div style="display:flex;gap:16px;margin-bottom:8px;font-size:13px;flex-wrap:wrap">
          <div><span class="cell-muted">状态：</span><span :class="testResult.status >= 200 && testResult.status < 300 ? 'badge badge-green' : 'badge badge-red'">{{ testResult.status }}</span></div>
          <div><span class="cell-muted">延迟：</span>{{ testResult.latency }}ms</div>
          <div><span class="cell-muted">Kind：</span><code>{{ testKind }}</code></div>
        </div>
        <div v-if="testResult.error" class="alert alert-danger" style="margin:0">{{ testResult.error }}</div>
        <pre v-else class="code-block" style="max-height:60vh;overflow:auto;font-size:12px;flex:1;margin:0">{{ testResult.body }}</pre>
      </div>
    </div>
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
</style>

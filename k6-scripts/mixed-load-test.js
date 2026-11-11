// k6 混合负载测试脚本
// 测试目标：模拟真实场景的混合负载（80% API调用 + 20% 管理操作）

import http from 'k6/http';
import { check, sleep } from 'k6';
import { Rate, Trend } from 'k6/metrics';
import { randomItem, randomIntBetween } from 'https://jslib.k6.io/k6-utils/1.2.0/index.js';

const errorRate = new Rate('errors');
const apiLatency = new Trend('api_latency');
const adminLatency = new Trend('admin_latency');

export let options = {
  stages: [
    { duration: '2m', target: 30 },    // 2分钟升到30用户
    { duration: '5m', target: 30 },    // 维持30用户5分钟
    { duration: '2m', target: 60 },    // 2分钟升到60用户
    { duration: '5m', target: 60 },    // 维持60用户5分钟
    { duration: '2m', target: 100 },   // 2分钟升到100用户
    { duration: '5m', target: 100 },   // 维持100用户5分钟
    { duration: '2m', target: 0 },     // 2分钟降到0
  ],
  thresholds: {
    'http_req_duration': ['p(95)<1500', 'p(99)<3000'],
    'http_req_failed': ['rate<0.05'],
    'errors': ['rate<0.05'],
    'api_latency': ['p(95)<1000'],
    'admin_latency': ['p(95)<800'],
  },
};

const BASE_URL = __ENV.BASE_URL || 'http://127.0.0.1:8782';
const API_KEY = [REDACTED:secret] || 'test-key-placeholder';

// 模拟不同的模型选择
const MODELS = [
  'gpt-3.5-turbo',
  'gpt-4',
  'claude-3-sonnet',
  'claude-3-haiku',
  'gemini-pro',
];

// 模拟不同的用户消息
const USER_MESSAGES = [
  'Hello, how are you?',
  'Can you help me with a coding problem?',
  'Explain quantum computing in simple terms.',
  'Write a haiku about technology.',
  'What is the weather like today?',
  'Translate this to French: Hello world',
  'Summarize the key points of machine learning.',
  'Generate a creative story prompt.',
];

export function setup() {
  // 尝试登录获取管理员session（用于20%的管理操作）
  const loginRes = http.post(`${BASE_URL}/api/auth/token`, JSON.stringify({
    username: [REDACTED:account] || 'admin',
    password: [REDACTED:secret] || 'password',
  }), {
    headers: { 'Content-Type': 'application/json' },
  });
  
  const sessionCookie = loginRes.status === 200 && loginRes.cookies.llmgw_session
    ? loginRes.cookies.llmgw_session[0].value
    : null;
  
  return { sessionCookie };
}

export default function (data) {
  const random = Math.random();
  
  // 80% API调用（模拟真实用户流量）
  if (random < 0.8) {
    const model = randomItem(MODELS);
    const message = randomItem(USER_MESSAGES);
    const useStreaming = Math.random() < 0.3; // 30%使用流式
    
    const url = `${BASE_URL}/v1/chat/completions`;
    const payload = JSON.stringify({
      model: model,
      messages: [{ role: 'user', content: message }],
      stream: useStreaming,
      max_tokens: randomIntBetween(50, 200),
    });

    const params = {
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `[REDACTED:secret]`,
      },
      timeout: '30s',
    };

    const startTime = Date.now();
    const res = http.post(url, payload, params);
    const duration = Date.now() - startTime;
    
    apiLatency.add(duration);
    
    const success = check(res, {
      'api call ok': (r) => r.status === 200,
      'has response': (r) => r.body && r.body.length > 0,
    });
    
    if (!success) {
      console.log(`API call failed: ${res.status} - ${model} - streaming:${useStreaming}`);
    }
    
    errorRate.add(!success);
  } 
  // 20% 管理操作（模拟管理员操作）
  else {
    if (!data.sessionCookie) {
      // 没有session，尝试访问公开端点
      const res = http.get(`${BASE_URL}/api/models`);
      check(res, {
        'public endpoint accessible': (r) => r.status === 200 || r.status === 401,
      });
    } else {
      const headers = {
        'Cookie': `llmgw_session=${data.sessionCookie}`,
      };
      
      // 随机选择一个管理操作
      const adminOps = [
        () => http.get(`${BASE_URL}/api/dashboard/stats`, { headers }),
        () => http.get(`${BASE_URL}/api/request-logs?limit=20`, { headers }),
        () => http.get(`${BASE_URL}/api/providers`, { headers }),
        () => http.get(`${BASE_URL}/api/models`, { headers }),
        () => http.get(`${BASE_URL}/api/keys?limit=10`, { headers }),
      ];
      
      const op = randomItem(adminOps);
      const startTime = Date.now();
      const res = op();
      const duration = Date.now() - startTime;
      
      adminLatency.add(duration);
      
      const success = check(res, {
        'admin call ok': (r) => r.status === 200,
      });
      
      errorRate.add(!success);
    }
  }

  // 模拟真实用户的思考时间
  sleep(randomIntBetween(1, 3));
}

export function handleSummary(data) {
  let summary = '\n=== Mixed Load Test Summary ===\n';
  summary += `Total Requests: ${data.metrics.http_reqs.values.count}\n`;
  summary += `Failed Requests: ${data.metrics.http_req_failed.values.passes}\n`;
  summary += `Request Rate: ${data.metrics.http_reqs.values.rate.toFixed(2)} req/s\n`;
  summary += `Overall Response Time (p50): ${data.metrics.http_req_duration.values['p(50)'].toFixed(2)}ms\n`;
  summary += `Overall Response Time (p95): ${data.metrics.http_req_duration.values['p(95)'].toFixed(2)}ms\n`;
  summary += `Overall Response Time (p99): ${data.metrics.http_req_duration.values['p(99)'].toFixed(2)}ms\n`;
  summary += `Error Rate: ${(data.metrics.errors.values.rate * 100).toFixed(2)}%\n`;
  
  summary += '\n--- API Latency ---\n';
  summary += `API p50: ${data.metrics.api_latency.values['p(50)'].toFixed(2)}ms\n`;
  summary += `API p95: ${data.metrics.api_latency.values['p(95)'].toFixed(2)}ms\n`;
  summary += `API p99: ${data.metrics.api_latency.values['p(99)'].toFixed(2)}ms\n`;
  
  summary += '\n--- Admin Latency ---\n';
  summary += `Admin p50: ${data.metrics.admin_latency.values['p(50)'].toFixed(2)}ms\n`;
  summary += `Admin p95: ${data.metrics.admin_latency.values['p(95)'].toFixed(2)}ms\n`;
  summary += `Admin p99: ${data.metrics.admin_latency.values['p(99)'].toFixed(2)}ms\n`;
  
  return {
    'stdout': summary,
    'mixed-summary.json': JSON.stringify(data),
  };
}

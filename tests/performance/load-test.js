import http from 'k6/http';
import { check, sleep } from 'k6';
import { Rate } from 'k6/metrics';

// 自定义指标
const errorRate = new Rate('errors');

// 测试配置
export const options = {
  stages: [
    { duration: '2m', target: 50 },   // 预热阶段：2分钟到50并发
    { duration: '5m', target: 100 },  // 增压阶段：5分钟到100并发
    { duration: '10m', target: 200 }, // 压力阶段：10分钟到200并发
    { duration: '5m', target: 300 },  // 峰值阶段：5分钟到300并发
    { duration: '5m', target: 100 },  // 降压阶段：5分钟降到100并发
    { duration: '2m', target: 0 },    // 冷却阶段：2分钟降到0
  ],
  thresholds: {
    http_req_duration: ['p(95)<2000', 'p(99)<5000'], // 95%请求<2s, 99%<5s
    http_req_failed: ['rate<0.01'],                   // 错误率<1%
    errors: ['rate<0.05'],                            // 自定义错误率<5%
  },
};

// 环境变量
const BASE_URL = __ENV.BASE_URL || 'http://localhost:8780';
const API_KEY = __ENV.API_KEY || 'test-key';

// 测试场景1：API负载测试
export function apiLoadTest() {
  const payload = JSON.stringify({
    model: 'gpt-3.5-turbo',
    messages: [
      { role: 'user', content: 'Hello, how are you?' }
    ],
    max_tokens: 100
  });

  const params = {
    headers: {
      'Content-Type': 'application/json',
      'Authorization': `Bearer ${API_KEY}`,
    },
  };

  const response = http.post(`${BASE_URL}/v1/chat/completions`, payload, params);
  
  const success = check(response, {
    'status is 200': (r) => r.status === 200,
    'response has choices': (r) => {
      try {
        const body = JSON.parse(r.body);
        return body.choices && body.choices.length > 0;
      } catch {
        return false;
      }
    },
    'response time < 2s': (r) => r.timings.duration < 2000,
  });

  errorRate.add(!success);
  sleep(1);
}

// 测试场景2：流式响应压力测试
export function streamingStressTest() {
  const payload = JSON.stringify({
    model: 'gpt-3.5-turbo',
    messages: [
      { role: 'user', content: 'Tell me a short story.' }
    ],
    stream: true,
    max_tokens: 200
  });

  const params = {
    headers: {
      'Content-Type': 'application/json',
      'Authorization': `Bearer ${API_KEY}`,
    },
  };

  const response = http.post(`${BASE_URL}/v1/chat/completions`, payload, params);
  
  const success = check(response, {
    'status is 200': (r) => r.status === 200,
    'is streaming response': (r) => r.body.includes('data: '),
    'response time < 3s': (r) => r.timings.duration < 3000,
  });

  errorRate.add(!success);
  sleep(1);
}

// 测试场景3：管理端点压力测试
export function adminEndpointTest() {
  const endpoints = [
    '/api/models',
    '/api/providers',
    '/api/stats/summary',
  ];

  const endpoint = endpoints[Math.floor(Math.random() * endpoints.length)];
  
  const params = {
    headers: {
      'Authorization': `Bearer ${API_KEY}`,
    },
  };

  const response = http.get(`${BASE_URL}${endpoint}`, params);
  
  const success = check(response, {
    'status is 200 or 401': (r) => r.status === 200 || r.status === 401,
    'response time < 1s': (r) => r.timings.duration < 1000,
  });

  errorRate.add(!success);
  sleep(0.5);
}

// 默认导出：混合场景
export default function() {
  const scenario = Math.random();
  
  if (scenario < 0.6) {
    // 60%的流量：普通API请求
    apiLoadTest();
  } else if (scenario < 0.9) {
    // 30%的流量：流式请求
    streamingStressTest();
  } else {
    // 10%的流量：管理端点
    adminEndpointTest();
  }
}

// 设置阶段
export function setup() {
  console.log('Starting performance test...');
  console.log(`Base URL: ${BASE_URL}`);
  return { startTime: new Date() };
}

// 拆卸阶段
export function teardown(data) {
  console.log('Performance test completed');
  const duration = (new Date() - data.startTime) / 1000;
  console.log(`Total duration: ${duration}s`);
}

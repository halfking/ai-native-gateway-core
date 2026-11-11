import http from 'k6/http';
import { check, sleep } from 'k6';
import { Rate } from 'k6/metrics';

// 自定义指标
const errorRate = new Rate('errors');

// 测试配置
export const options = {
  stages: [
    { duration: '2m', target: 50 },   // 2分钟内逐步增加到50并发
    { duration: '5m', target: 100 },  // 5分钟内增加到100并发
    { duration: '5m', target: 100 },  // 保持100并发5分钟
    { duration: '2m', target: 200 },  // 2分钟内增加到200并发
    { duration: '5m', target: 200 },  // 保持200并发5分钟
    { duration: '2m', target: 0 },    // 2分钟内降到0
  ],
  thresholds: {
    http_req_duration: ['p(95)<2000', 'p(99)<5000'], // 95%请求<2s, 99%<5s
    http_req_failed: ['rate<0.01'],                   // 错误率<1%
    errors: ['rate<0.01'],
  },
};

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8780';
const ADMIN_PASSWORD = __ENV.ADMIN_PASSWORD || 'admin123';

// 登录获取token
function login() {
  const payload = JSON.stringify({
    username: 'admin',
    password: ADMIN_PASSWORD,
  });

  const params = {
    headers: {
      'Content-Type': 'application/json',
    },
  };

  const res = http.post(`${BASE_URL}/api/auth/login`, payload, params);
  
  check(res, {
    'login successful': (r) => r.status === 200,
    'got token': (r) => r.json('token') !== undefined,
  }) || errorRate.add(1);

  return res.json('token');
}

export function setup() {
  // 在测试开始前登录一次
  return { token: login() };
}

export default function(data) {
  const token = data.token;
  const params = {
    headers: {
      'Authorization': `Bearer ${token}`,
      'Content-Type': 'application/json',
    },
  };

  // 测试1: 获取概览信息
  let res = http.get(`${BASE_URL}/api/overview`, params);
  check(res, {
    'overview status 200': (r) => r.status === 200,
    'overview has data': (r) => r.json('data') !== undefined,
  }) || errorRate.add(1);

  sleep(1);

  // 测试2: 获取供应商列表
  res = http.get(`${BASE_URL}/api/providers`, params);
  check(res, {
    'providers status 200': (r) => r.status === 200,
    'providers is array': (r) => Array.isArray(r.json('data')),
  }) || errorRate.add(1);

  sleep(1);

  // 测试3: 获取租户列表
  res = http.get(`${BASE_URL}/api/tenants?page=1&limit=20`, params);
  check(res, {
    'tenants status 200': (r) => r.status === 200,
    'tenants has pagination': (r) => r.json('pagination') !== undefined,
  }) || errorRate.add(1);

  sleep(1);

  // 测试4: 获取模型列表
  res = http.get(`${BASE_URL}/api/models`, params);
  check(res, {
    'models status 200': (r) => r.status === 200,
    'models has items': (r) => r.json('data.length') > 0,
  }) || errorRate.add(1);

  sleep(1);

  // 测试5: API调用测试（chat completion）
  const chatPayload = JSON.stringify({
    model: 'gpt-3.5-turbo',
    messages: [
      { role: 'user', content: 'Hello, this is a test message' }
    ],
    max_tokens: 100,
  });

  res = http.post(`${BASE_URL}/v1/chat/completions`, chatPayload, params);
  check(res, {
    'chat completion status 200': (r) => r.status === 200,
    'chat has choices': (r) => r.json('choices.length') > 0,
  }) || errorRate.add(1);

  sleep(2);
}

export function teardown(data) {
  // 清理工作
  console.log('Performance test completed');
}

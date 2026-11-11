import http from 'k6/http';
import { check, sleep } from 'k6';
import { Rate, Trend } from 'k6/metrics';

// 自定义指标
const errorRate = new Rate('errors');
const responseTime = new Trend('response_time');

export const options = {
  stages: [
    { duration: '1m', target: 10 },   // 1分钟内逐步增加到10并发
    { duration: '3m', target: 20 },   // 3分钟内增加到20并发
    { duration: '4m', target: 20 },   // 保持20并发4分钟
    { duration: '2m', target: 50 },   // 2分钟内增加到50并发
    { duration: '3m', target: 50 },   // 保持50并发3分钟
    { duration: '1m', target: 0 },    // 1分钟内降到0
  ],
  thresholds: {
    errors: ['rate<0.005'],                 // 错误率<0.5%
    response_time: ['p(95)<1000'],          // 95%响应<1秒
    http_req_duration: ['p(99)<2000'],      // 99%请求<2秒
    http_req_failed: ['rate<0.01'],         // 失败率<1%
  },
};

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8780';
const API_KEY = __ENV.API_KEY || 'test-api-key';
const ADMIN_TOKEN = __ENV.ADMIN_TOKEN || 'admin-token';

const headers = {
  'Authorization': `Bearer ${ADMIN_TOKEN}`,
  'Content-Type': 'application/json',
};

// 管理端点列表
const endpoints = [
  { method: 'GET', path: '/api/providers', weight: 3 },
  { method: 'GET', path: '/api/models', weight: 3 },
  { method: 'GET', path: '/api/tenants', weight: 2 },
  { method: 'GET', path: '/api/keys', weight: 2 },
  { method: 'GET', path: '/api/routing-policies', weight: 2 },
  { method: 'GET', path: '/api/overview/stats', weight: 4 },
  { method: 'GET', path: '/api/audit-logs?limit=50', weight: 1 },
];

// 根据权重选择端点
function selectEndpoint() {
  const totalWeight = endpoints.reduce((sum, ep) => sum + ep.weight, 0);
  let random = Math.random() * totalWeight;
  
  for (const endpoint of endpoints) {
    if (random < endpoint.weight) {
      return endpoint;
    }
    random -= endpoint.weight;
  }
  
  return endpoints[0];
}

export default function() {
  const endpoint = selectEndpoint();
  const url = `${BASE_URL}${endpoint.path}`;
  
  const startTime = Date.now();
  const res = http.get(url, { headers });
  const duration = Date.now() - startTime;
  
  responseTime.add(duration);
  
  const checkResult = check(res, {
    'status is 200': (r) => r.status === 200,
    'response time < 1s': (r) => duration < 1000,
    'has valid JSON': (r) => {
      try {
        JSON.parse(r.body);
        return true;
      } catch (e) {
        return false;
      }
    },
    'no error field': (r) => {
      try {
        const body = JSON.parse(r.body);
        return !body.error;
      } catch (e) {
        return false;
      }
    },
  });
  
  if (!checkResult) {
    errorRate.add(1);
    console.error(`Failed request to ${endpoint.path}: status=${res.status}, duration=${duration}ms`);
  } else {
    errorRate.add(0);
  }
  
  sleep(1); // 1秒间隔
}

// 场景测试：批量操作
export function batchOperations() {
  const batch = [
    ['GET', `${BASE_URL}/api/providers`, null, headers],
    ['GET', `${BASE_URL}/api/models`, null, headers],
    ['GET', `${BASE_URL}/api/tenants`, null, headers],
    ['GET', `${BASE_URL}/api/keys`, null, headers],
  ];
  
  const responses = http.batch(batch);
  
  responses.forEach((res, index) => {
    check(res, {
      [`batch[${index}] status 200`]: (r) => r.status === 200,
    }) || errorRate.add(1);
  });
  
  sleep(2);
}

// 场景测试：数据创建
export function createOperations() {
  // 创建新的API密钥
  const createKeyPayload = JSON.stringify({
    name: `test-key-${Date.now()}`,
    tenant_id: 'default',
    rate_limit: 100,
  });
  
  const createRes = http.post(
    `${BASE_URL}/api/keys`,
    createKeyPayload,
    { headers }
  );
  
  check(createRes, {
    'create key status 201': (r) => r.status === 201,
    'has key in response': (r) => {
      try {
        const body = JSON.parse(r.body);
        return body.key !== undefined;
      } catch (e) {
        return false;
      }
    },
  }) || errorRate.add(1);
  
  sleep(1);
}

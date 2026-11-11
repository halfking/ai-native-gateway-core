// k6 管理后台负载测试脚本
// 测试目标：验证管理后台API的并发访问性能

import http from 'k6/http';
import { check, group, sleep } from 'k6';
import { Rate } from 'k6/metrics';

const errorRate = new Rate('errors');

export let options = {
  stages: [
    { duration: '1m', target: 10 },    // 1分钟升到10并发管理员
    { duration: '3m', target: 10 },    // 维持10并发3分钟
    { duration: '1m', target: 20 },    // 1分钟升到20并发
    { duration: '3m', target: 20 },    // 维持20并发3分钟
    { duration: '1m', target: 0 },     // 1分钟降到0
  ],
  thresholds: {
    'http_req_duration': ['p(95)<1000'],
    'http_req_failed': ['rate<0.05'],
    'errors': ['rate<0.05'],
  },
};

const BASE_URL = __ENV.BASE_URL || 'http://127.0.0.1:8782';
const ADMIN_USER = __ENV.ADMIN_USER || 'admin';
const ADMIN_PASSWORD = __ENV.ADMIN_PASSWORD || 'password';

export function setup() {
  // 登录获取session cookie
  const loginRes = http.post(`${BASE_URL}/api/auth/token`, JSON.stringify({
    username: ADMIN_USER,
    password: ADMIN_PASSWORD,
  }), {
    headers: { 'Content-Type': 'application/json' },
  });
  
  if (loginRes.status !== 200) {
    console.error('Login failed:', loginRes.status, loginRes.body);
    return { sessionCookie: null };
  }
  
  const cookies = loginRes.cookies;
  const sessionCookie = cookies.llmgw_session ? cookies.llmgw_session[0].value : null;
  
  return { sessionCookie };
}

export default function (data) {
  if (!data.sessionCookie) {
    console.error('No session cookie available');
    errorRate.add(1);
    return;
  }

  const headers = {
    'Cookie': `llmgw_session=${data.sessionCookie}`,
  };

  let groupSuccess = true;

  // 测试仪表盘API
  group('Dashboard APIs', function () {
    const res = http.get(`${BASE_URL}/api/dashboard/stats`, { headers });
    const success = check(res, {
      'dashboard stats ok': (r) => r.status === 200,
      'has json response': (r) => {
        try {
          JSON.parse(r.body);
          return true;
        } catch (e) {
          return false;
        }
      },
    });
    if (!success) groupSuccess = false;
  });

  // 测试请求日志API
  group('Request Logs', function () {
    const res = http.get(`${BASE_URL}/api/request-logs?limit=50&offset=0`, { headers });
    const success = check(res, {
      'request logs ok': (r) => r.status === 200,
      'has data': (r) => r.body && r.body.length > 0,
    });
    if (!success) groupSuccess = false;
  });

  // 测试供应商API
  group('Providers', function () {
    const res = http.get(`${BASE_URL}/api/providers`, { headers });
    const success = check(res, {
      'providers ok': (r) => r.status === 200,
    });
    if (!success) groupSuccess = false;
  });

  // 测试模型API
  group('Models', function () {
    const res = http.get(`${BASE_URL}/api/models`, { headers });
    const success = check(res, {
      'models ok': (r) => r.status === 200,
    });
    if (!success) groupSuccess = false;
  });

  // 测试租户API
  group('Tenants', function () {
    const res = http.get(`${BASE_URL}/api/tenants`, { headers });
    const success = check(res, {
      'tenants ok': (r) => r.status === 200 || r.status === 403, // 403可能是权限限制
    });
    if (!success) groupSuccess = false;
  });

  // 测试密钥API
  group('API Keys', function () {
    const res = http.get(`${BASE_URL}/api/keys?limit=20`, { headers });
    const success = check(res, {
      'keys ok': (r) => r.status === 200,
    });
    if (!success) groupSuccess = false;
  });

  errorRate.add(!groupSuccess);

  sleep(1);
}

export function handleSummary(data) {
  let summary = '\n=== Admin Load Test Summary ===\n';
  summary += `Total Requests: ${data.metrics.http_reqs.values.count}\n`;
  summary += `Failed Requests: ${data.metrics.http_req_failed.values.passes}\n`;
  summary += `Request Rate: ${data.metrics.http_reqs.values.rate.toFixed(2)} req/s\n`;
  summary += `Response Time (p50): ${data.metrics.http_req_duration.values['p(50)'].toFixed(2)}ms\n`;
  summary += `Response Time (p95): ${data.metrics.http_req_duration.values['p(95)'].toFixed(2)}ms\n`;
  summary += `Error Rate: ${(data.metrics.errors.values.rate * 100).toFixed(2)}%\n`;
  
  // 按group统计
  summary += '\n--- Per-Group Statistics ---\n';
  for (const [name, metric] of Object.entries(data.metrics)) {
    if (name.startsWith('group_duration{group:::')) {
      const groupName = name.match(/group:::(.+?)}$/)[1];
      summary += `${groupName}: ${metric.values.avg.toFixed(2)}ms (avg)\n`;
    }
  }
  
  return {
    'stdout': summary,
    'admin-summary.json': JSON.stringify(data),
  };
}

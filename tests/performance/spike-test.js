import http from 'k6/http';
import { check, sleep } from 'k6';
import { Rate, Trend, Counter } from 'k6/metrics';

// 自定义指标
const errorRate = new Rate('errors');
const spikeResponseTime = new Trend('spike_response_time');
const totalRequests = new Counter('total_requests');
const failedRequests = new Counter('failed_requests');

export const options = {
  stages: [
    { duration: '30s', target: 10 },    // 30秒预热到10并发
    { duration: '1m', target: 10 },     // 保持10并发1分钟（基线）
    { duration: '10s', target: 200 },   // 10秒内突然增加到200并发（第一次尖峰）
    { duration: '1m', target: 200 },    // 保持200并发1分钟
    { duration: '10s', target: 10 },    // 10秒内降回10并发
    { duration: '1m', target: 10 },     // 恢复期：保持10并发1分钟
    { duration: '10s', target: 300 },   // 10秒内突然增加到300并发（第二次尖峰）
    { duration: '1m', target: 300 },    // 保持300并发1分钟
    { duration: '10s', target: 10 },    // 10秒内降回10并发
    { duration: '1m', target: 10 },     // 恢复期：保持10并发1分钟
    { duration: '30s', target: 0 },     // 30秒内降到0
  ],
  thresholds: {
    errors: ['rate<0.01'],                      // 错误率<1%
    spike_response_time: ['p(95)<3000'],        // 95%响应<3秒
    http_req_duration: ['p(99)<5000'],          // 99%请求<5秒
    http_req_failed: ['rate<0.02'],             // 失败率<2%
  },
};

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8780';
const API_KEY = __ENV.API_KEY || 'test-api-key';

const headers = {
  'Authorization': `Bearer ${API_KEY}`,
  'Content-Type': 'application/json',
};

// 不同复杂度的请求负载
const payloads = {
  simple: {
    model: 'gpt-3.5-turbo',
    messages: [{ role: 'user', content: 'Hello!' }],
    max_tokens: 50,
  },
  medium: {
    model: 'gpt-3.5-turbo',
    messages: [
      { role: 'user', content: 'Explain quantum computing in simple terms.' }
    ],
    max_tokens: 200,
  },
  complex: {
    model: 'gpt-4',
    messages: [
      { 
        role: 'system', 
        content: 'You are a helpful assistant specialized in technology.' 
      },
      { 
        role: 'user', 
        content: 'Write a detailed technical analysis of microservices architecture, including pros and cons, best practices, and common pitfalls.' 
      }
    ],
    max_tokens: 500,
  },
};

// 根据当前VU数量选择负载类型
function selectPayload() {
  const vu = __VU;
  const totalVUs = __ENV.K6_VUS || 1;
  const ratio = vu / totalVUs;
  
  if (ratio < 0.6) {
    return 'simple';   // 60% 简单请求
  } else if (ratio < 0.9) {
    return 'medium';   // 30% 中等请求
  } else {
    return 'complex';  // 10% 复杂请求
  }
}

export default function() {
  totalRequests.add(1);
  
  const payloadType = selectPayload();
  const payload = JSON.stringify(payloads[payloadType]);
  
  const startTime = Date.now();
  const res = http.post(
    `${BASE_URL}/v1/chat/completions`,
    payload,
    { headers }
  );
  const duration = Date.now() - startTime;
  
  spikeResponseTime.add(duration);
  
  const checkResult = check(res, {
    'status is 200': (r) => r.status === 200,
    'has valid response': (r) => {
      try {
        const body = JSON.parse(r.body);
        return body.choices && body.choices.length > 0;
      } catch (e) {
        return false;
      }
    },
    'response has content': (r) => {
      try {
        const body = JSON.parse(r.body);
        return body.choices[0].message.content.length > 0;
      } catch (e) {
        return false;
      }
    },
  });
  
  if (!checkResult) {
    errorRate.add(1);
    failedRequests.add(1);
    console.error(`Failed ${payloadType} request: status=${res.status}, duration=${duration}ms`);
  } else {
    errorRate.add(0);
  }
  
  // 根据当前阶段调整休眠时间
  const iteration = __ITER;
  if (iteration % 100 < 10) {
    sleep(0.1); // 尖峰期间短暂休眠
  } else {
    sleep(0.5); // 正常期间较长休眠
  }
}

// 健康检查场景
export function healthCheck() {
  const res = http.get(`${BASE_URL}/health`);
  
  check(res, {
    'health check status 200': (r) => r.status === 200,
    'health check response time < 100ms': (r) => r.timings.duration < 100,
  });
  
  sleep(5);
}

// 系统指标监控场景
export function monitorMetrics() {
  const res = http.get(`${BASE_URL}/api/overview/stats`, { headers });
  
  check(res, {
    'metrics status 200': (r) => r.status === 200,
    'has system metrics': (r) => {
      try {
        const body = JSON.parse(r.body);
        return body.requests !== undefined && body.latency !== undefined;
      } catch (e) {
        return false;
      }
    },
  });
  
  sleep(10);
}

// 设置和拆卸
export function setup() {
  console.log('Starting spike test...');
  console.log(`Base URL: ${BASE_URL}`);
  return { startTime: Date.now() };
}

export function teardown(data) {
  const duration = (Date.now() - data.startTime) / 1000;
  console.log(`Spike test completed in ${duration}s`);
}

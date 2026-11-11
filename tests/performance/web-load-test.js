import http from 'k6/http';
import { check, sleep } from 'k6';
import { Rate, Trend } from 'k6/metrics';

// Web 端点并发压测：首页 + healthz + 大静态资源（无需认证）
// 用法: BASE_URL=http://localhost:8080 VUS=50 DURATION=60s k6 run tests/performance/web-load-test.js

const errorRate = new Rate('page_errors');
const pageLoadTime = new Trend('page_load_time');
const assetLoadTime = new Trend('asset_load_time');

export const options = {
  vus: Number(__ENV.VUS || 50),
  duration: __ENV.DURATION || '60s',
  thresholds: {
    http_req_duration: ['p(95)<2000'],   // 慢页面判定阈值：P95 < 2s
    http_req_failed: ['rate<0.01'],      // 失败率 < 1%
    page_errors: ['rate<0.01'],
  },
};

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';

// 先探测一次首页拿真实资源路径
function getAssetUrls() {
  const home = http.get(`${BASE_URL}/`);
  const html = home.body || '';
  const urls = [...html.matchAll(/(?:src|href)="(\/assets\/[^"]+\.(?:js|css))"/g)].map(m => m[1]);
  return [...new Set(urls)].slice(0, 4);
}

export default function () {
  const assets = getAssetUrls();

  // 1. healthz（高频探针路径）
  const h = http.get(`${BASE_URL}/healthz`);
  check(h, { 'healthz 200': (r) => r.status === 200 });

  // 2. 首页（Web 前端主路径；API-only 构建返回 JSON 也算 200）
  const t0 = Date.now();
  const home = http.get(`${BASE_URL}/`);
  pageLoadTime.add(Date.now() - t0);
  const ok = check(home, { '首页 200': (r) => r.status === 200 });
  errorRate.add(!ok);

  // 3. 大静态资源（JS bundle，前端首屏主要耗时来源）
  for (const a of assets) {
    const t1 = Date.now();
    const r = http.get(`${BASE_URL}${a}`);
    assetLoadTime.add(Date.now() - t1);
    check(r, { [`asset ${a.slice(0, 30)} 200`]: (res) => res.status === 200 });
  }

  sleep(1);
}

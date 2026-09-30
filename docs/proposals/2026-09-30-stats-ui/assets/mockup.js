/* 效果图共享脚本 — 原生 SVG 图表 + 主题切换（2026-09-30 方案轮）
 * 仅供静态效果图演示；实施时对应 chart.js + composables/useChart.ts。 */
(function () {
  'use strict';

  // ── 主题切换 ──────────────────────────────
  window.mockToggleTheme = function () {
    const html = document.documentElement;
    const next = html.getAttribute('data-theme') === 'dark' ? 'light' : 'dark';
    html.setAttribute('data-theme', next);
    try { localStorage.setItem('mockup_theme', next); } catch (e) { /* noop */ }
    window.dispatchEvent(new CustomEvent('mocktheme', { detail: next }));
  };
  (function initTheme() {
    let saved = null;
    try { saved = localStorage.getItem('mockup_theme'); } catch (e) { /* noop */ }
    document.documentElement.setAttribute('data-theme', saved || 'dark');
  })();

  function cssVar(name) {
    return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  }
  const NS = 'http://www.w3.org/2000/svg';
  function el(tag, attrs) {
    const node = document.createElementNS(NS, tag);
    for (const k in attrs) node.setAttribute(k, attrs[k]);
    return node;
  }
  function fmtK(n) {
    if (n >= 1e9) return (n / 1e9).toFixed(2) + 'B';
    if (n >= 1e6) return (n / 1e6).toFixed(2) + 'M';
    if (n >= 1e3) return (n / 1e3).toFixed(1) + 'K';
    return String(Math.round(n));
  }
  window.mockFmtK = fmtK;

  // 通用坐标区绘制（含 y 轴刻度与 x 轴标签）
  function frame(box, labels, opts) {
    const W = box.clientWidth || 800;
    const H = box.clientHeight || 240;
    const padL = 44, padR = opts.rightAxis ? 44 : 14, padT = 10, padB = 24;
    const svg = el('svg', { class: 'chart', viewBox: `0 0 ${W} ${H}`, preserveAspectRatio: 'none' });
    svg.setAttribute('style', 'width:100%;height:100%');
    const plotW = W - padL - padR, plotH = H - padT - padB;
    const n = labels.length;
    const xAt = (i) => padL + (n <= 1 ? plotW / 2 : (plotW * i) / (n - 1));
    const gridColor = cssVar('--grid-line') || 'rgba(128,128,128,.15)';
    const muted = cssVar('--kx-muted') || '#888';
    const ticks = opts.ticks || 4;
    for (let t = 0; t <= ticks; t++) {
      const y = padT + (plotH * t) / ticks;
      svg.appendChild(el('line', { x1: padL, y1: y, x2: W - padR, y2: y, stroke: gridColor, 'stroke-width': 1 }));
      const val = opts.yMax - ((opts.yMax - (opts.yMin || 0)) * t) / ticks;
      const txt = el('text', { x: padL - 8, y: y + 4, 'text-anchor': 'end', 'font-size': 10, fill: muted });
      txt.textContent = opts.fmtY ? opts.fmtY(val) : fmtK(val);
      svg.appendChild(txt);
    }
    const step = Math.max(1, Math.ceil(n / (W / 72)));
    labels.forEach(function (lb, i) {
      if (i % step !== 0 && i !== n - 1) return;
      const txt = el('text', { x: xAt(i), y: H - 6, 'text-anchor': 'middle', 'font-size': 10, fill: muted });
      txt.textContent = lb;
      svg.appendChild(txt);
    });
    return { svg: svg, xAt: xAt, padL: padL, padR: padR, padT: padT, plotW: plotW, plotH: plotH, W: W, H: H, n: n };
  }

  // 堆叠柱 + 可选右轴折线
  // series: [{name, data:[], color, axis:'left'|'right'}]，axis=right 的画折线
  window.mockStackedBars = function (boxId, conf) {
    const box = document.getElementById(boxId);
    if (!box) return;
    box.innerHTML = '';
    const leftSeries = conf.series.filter(function (s) { return s.axis !== 'right'; });
    const rightSeries = conf.series.filter(function (s) { return s.axis === 'right'; });
    const stackMax = Math.max.apply(null, conf.labels.map(function (_, i) {
      return leftSeries.reduce(function (a, s) { return a + (s.data[i] || 0); }, 0);
    })) * 1.08 || 1;
    let rightMax = 1, rightMin = 0;
    if (rightSeries.length) {
      const all = [].concat.apply([], rightSeries.map(function (s) { return s.data; }));
      rightMax = Math.max.apply(null, all) * 1.15;
      rightMin = Math.min.apply(null, all) * 0.85;
    }
    const f = frame(box, conf.labels, { yMax: stackMax, ticks: 4, rightAxis: rightSeries.length > 0, fmtY: conf.fmtY });
    const barW = Math.min(30, (f.plotW / f.n) * 0.52);
    const leftTop = f.padT, leftH = f.plotH;
    const yLeft = function (v) { return leftTop + leftH - (v / stackMax) * leftH; };
    const yRight = function (v) { return f.padT + f.plotH - ((v - rightMin) / (rightMax - rightMin)) * f.plotH; };
    conf.labels.forEach(function (_, i) {
      const cx = f.padL + (f.plotW * i) / Math.max(1, f.n - 1);
      let acc = 0;
      leftSeries.forEach(function (s) {
        const v = s.data[i] || 0;
        const h = (v / stackMax) * leftH;
        if (h > 0.4) {
          f.svg.appendChild(el('rect', {
            x: cx - barW / 2, y: yLeft(acc + v), width: barW, height: h,
            fill: s.color, rx: 2, opacity: 0.92,
          }));
        }
        acc += v;
      });
    });
    if (rightSeries.length) {
      rightSeries.forEach(function (s) {
        const pts = s.data.map(function (v, i) { return `${f.xAt(i)},${yRight(v)}`; }).join(' ');
        f.svg.appendChild(el('polyline', { points: pts, fill: 'none', stroke: s.color, 'stroke-width': 2, 'stroke-linejoin': 'round', opacity: 0.95 }));
        s.data.forEach(function (v, i) {
          if (i % Math.max(1, Math.ceil(f.n / 7)) === 0) {
            f.svg.appendChild(el('circle', { cx: f.xAt(i), cy: yRight(v), r: 2.6, fill: s.color }));
          }
        });
      });
      // 右轴刻度
      const muted = cssVar('--kx-muted') || '#888';
      for (let t = 0; t <= 4; t++) {
        const y = f.padT + (f.plotH * t) / 4;
        const val = rightMax - ((rightMax - rightMin) * t) / 4;
        const txt = el('text', { x: f.W - f.padR + 8, y: y + 4, 'font-size': 10, fill: muted });
        txt.textContent = conf.fmtRight ? conf.fmtRight(val) : val.toFixed(0);
        f.svg.appendChild(txt);
      }
    }
    box.appendChild(f.svg);
  };

  // 堆叠面积 + 可选右轴折线（对标参考图 Token 使用趋势）
  window.mockStackedArea = function (boxId, conf) {
    const box = document.getElementById(boxId);
    if (!box) return;
    box.innerHTML = '';
    const leftSeries = conf.series.filter(function (s) { return s.axis !== 'right'; });
    const rightSeries = conf.series.filter(function (s) { return s.axis === 'right'; });
    const stackMax = Math.max.apply(null, conf.labels.map(function (_, i) {
      return leftSeries.reduce(function (a, s) { return a + (s.data[i] || 0); }, 0);
    })) * 1.1 || 1;
    let rightMax = 1, rightMin = 0;
    if (rightSeries.length) {
      const all = [].concat.apply([], rightSeries.map(function (s) { return s.data; }));
      rightMax = Math.max.apply(null, all) * 1.1;
      rightMin = Math.min.apply(null, all) * 0.9;
    }
    const f = frame(box, conf.labels, { yMax: stackMax, ticks: 4, rightAxis: rightSeries.length > 0, fmtY: conf.fmtY });
    const yL = function (v) { return f.padT + f.plotH - (v / stackMax) * f.plotH; };
    const yR = function (v) { return f.padT + f.plotH - ((v - rightMin) / (rightMax - rightMin)) * f.plotH; };
    let acc = new Array(f.n).fill(0);
    leftSeries.forEach(function (s) {
      const up = s.data.map(function (v, i) { return `${f.xAt(i)},${yL(acc[i] + v)}`; }).join(' ');
      const base = acc;
      const down = [];
      for (let i = f.n - 1; i >= 0; i--) down.push(`${f.xAt(i)},${yL(base[i])}`);
      f.svg.appendChild(el('polygon', {
        points: up + ' ' + down.join(' '),
        fill: s.color, opacity: 0.32, stroke: 'none',
      }));
      f.svg.appendChild(el('polyline', { points: up, fill: 'none', stroke: s.color, 'stroke-width': 1.6, opacity: 0.9 }));
      acc = acc.map(function (a, i) { return a + (s.data[i] || 0); });
    });
    rightSeries.forEach(function (s) {
      const pts = s.data.map(function (v, i) { return `${f.xAt(i)},${yR(v)}`; }).join(' ');
      f.svg.appendChild(el('polyline', { points: pts, fill: 'none', stroke: s.color, 'stroke-width': 2, 'stroke-dasharray': '5,4', 'stroke-linejoin': 'round' }));
      s.data.forEach(function (v, i) {
        if (i % Math.max(1, Math.ceil(f.n / 8)) === 0) f.svg.appendChild(el('circle', { cx: f.xAt(i), cy: yR(v), r: 2.4, fill: s.color }));
      });
      const muted = cssVar('--kx-muted') || '#888';
      for (let t = 0; t <= 4; t++) {
        const y = f.padT + (f.plotH * t) / 4;
        const val = rightMax - ((rightMax - rightMin) * t) / 4;
        const txt = el('text', { x: f.W - f.padR + 8, y: y + 4, 'font-size': 10, fill: muted });
        txt.textContent = conf.fmtRight ? conf.fmtRight(val) : val.toFixed(0);
        f.svg.appendChild(txt);
      }
    });
    box.appendChild(f.svg);
  };

  // 双折线（用户详情趋势等）
  window.mockLines = function (boxId, conf) {
    const box = document.getElementById(boxId);
    if (!box) return;
    box.innerHTML = '';
    const all = [].concat.apply([], conf.series.map(function (s) { return s.data; }));
    const max = Math.max.apply(null, all) * 1.1 || 1;
    const f = frame(box, conf.labels, { yMax: max, ticks: 4, fmtY: conf.fmtY });
    conf.series.forEach(function (s) {
      const pts = s.data.map(function (v, i) { return `${f.xAt(i)},${f.padT + f.plotH - (v / max) * f.plotH}`; }).join(' ');
      if (s.area) {
        const base = `${f.xAt(f.n - 1)},${f.padT + f.plotH} ${f.xAt(0)},${f.padT + f.plotH}`;
        f.svg.appendChild(el('polygon', { points: pts + ' ' + base, fill: s.color, opacity: 0.16 }));
      }
      f.svg.appendChild(el('polyline', { points: pts, fill: 'none', stroke: s.color, 'stroke-width': 2, 'stroke-linejoin': 'round' }));
    });
    box.appendChild(f.svg);
  };

  // KPI 卡迷你柱
  window.mockSpark = function (boxId, data, colorVar) {
    const box = document.getElementById(boxId);
    if (!box) return;
    box.innerHTML = '';
    const W = 88, H = 34;
    const svg = el('svg', { viewBox: `0 0 ${W} ${H}`, preserveAspectRatio: 'none' });
    svg.setAttribute('style', 'width:100%;height:100%');
    const max = Math.max.apply(null, data) || 1;
    const bw = W / data.length;
    data.forEach(function (v, i) {
      const h = Math.max(2, (v / max) * (H - 4));
      svg.appendChild(el('rect', { x: i * bw + 1, y: H - h, width: bw - 2.5, height: h, fill: `var(${colorVar})`, rx: 1.5, opacity: 0.85 }));
    });
    box.appendChild(svg);
  };

  // 渲染器：主题切换后自动重绘
  const renderers = [];
  window.mockAutoRedraw = function (fn) { renderers.push(fn); fn(); };
  window.addEventListener('mocktheme', function () {
    renderers.forEach(function (fn) { try { fn(); } catch (e) { /* noop */ } });
  });
})();

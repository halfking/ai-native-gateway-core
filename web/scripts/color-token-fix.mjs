/**
 * color-token-fix.mjs — 一键把常见硬编码颜色映射到 token。
 * 用法: node scripts/color-token-fix.mjs [--dry-run]
 *
 * 策略: 在 .vue 的 <style> 块和 .css/.scss/.ts 文件里,精确字面替换。
 */

import { readFileSync, readdirSync, statSync, writeFileSync } from 'node:fs'
import { resolve, relative, join } from 'node:path'

const ROOT = resolve(process.cwd(), 'src')

// 相对 src/ 根的路径,例如 'style.css' 或 'composables/foo.ts'
const SKIP = new Set([
  'style.css',
  'composables/liveStreamColors.ts',
  'composables/useChart.ts',
  'composables/liveStreamDisplay.ts',
  'utils/waterfallTimeline.ts',
  'types/swimlane.ts',
])

const RULES = [
  // === 反白 #fff ===
  [/#fff\b/gi, 'var(--on-primary)'],
  [/#ffffff\b/gi, 'var(--on-primary)'],

  // === Ant Design / 占位调试 ===
  [/#1a1a1a\b/gi, 'var(--kx-text)'],
  [/#e5e5e5\b/gi, 'var(--border)'],
  [/#f9f9f9\b/gi, 'var(--surface-secondary)'],
  [/#ddd\b/g, 'var(--border)'],
  [/#40a9ff\b/gi, 'var(--accent)'],
  [/#f3f3f3\b/gi, 'var(--surface-secondary)'],
  [/#fff2f0\b/gi, 'var(--danger-bg)'],
  [/#ffccc7\b/gi, 'var(--danger-bd)'],
  [/#ff4d4f\b/gi, 'var(--danger)'],
  [/#1e293b\b/gi, 'var(--kx-text)'],
  [/#64748b\b/gi, 'var(--muted)'],
  [/#d1d5db\b/gi, 'var(--border)'],
  [/#b91c1c\b/gi, 'var(--danger)'],
  [/#5558e3\b/gi, 'var(--accent)'],
  [/#14532d\b/gi, 'var(--success-strong)'],
  [/#e6e6e6\b/gi, 'var(--border)'],

  // 业务色补编
  [/#38bdf8\b/gi, 'var(--probe-cyan)'],
  [/#0ea5e9\b/gi, 'var(--probe-cyan-deep)'],
  [/#7dd3fc\b/gi, 'var(--probe-cyan-light)'],
  [/#0284c7\b/gi, 'var(--probe-cyan-darker)'],
  [/#0c1a26\b/gi, 'var(--probe-dark-bg)'],
  [/#0d1117\b/gi, 'var(--probe-dark-bg)'],
  [/#0d1b2a\b/gi, 'var(--probe-dark-bg)'],
  [/#122438\b/gi, 'var(--probe-dark-bg)'],
  [/#1f6feb\b/gi, 'var(--accent)'],
  [/#d946ef\b/gi, 'var(--magenta)'],
  [/#c084fc\b/gi, 'var(--purple)'],
  [/#a78bfa\b/gi, 'var(--purple)'],
  [/#a855f7\b/gi, 'var(--purple)'],
  [/#ec4899\b/gi, 'var(--pink)'],
  [/#fa709a\b/gi, 'var(--pink)'],
  [/#065f46\b/gi, 'var(--success-strong)'],
  [/#166534\b/gi, 'var(--success-dark)'],
  [/#15803d\b/gi, 'var(--success-dark)'],
  [/#1f6b4a\b/gi, 'var(--success-dark)'],
  [/#1e40af\b/gi, 'var(--accent-dark)'],
  [/#1e3a8a\b/gi, 'var(--accent-dark)'],
  [/#92400e\b/gi, 'var(--warning-dark)'],
  [/#b45309\b/gi, 'var(--warning-dark)'],
  [/#422006\b/gi, 'var(--warning-dark)'],
  [/#991b1b\b/gi, 'var(--danger-dark)'],
  [/#7f1d1d\b/gi, 'var(--danger-dark)'],
  [/#374151\b/gi, 'var(--muted)'],
  [/#111827\b/gi, 'var(--kx-text)'],
  [/#1f2937\b/gi, 'var(--kx-text)'],
  [/#94a3b8\b/gi, 'var(--muted)'],
  [/#cbd5e1\b/gi, 'var(--border)'],
  [/#e5e7eb\b/gi, 'var(--surface-secondary)'],
  [/#e2e8f0\b/gi, 'var(--border)'],
  [/#f3f4f6\b/gi, 'var(--surface-secondary)'],
  [/#f9fafb\b/gi, 'var(--surface-secondary)'],
  [/#fafafa\b/gi, 'var(--surface-secondary)'],
  [/#dce3ee\b/gi, 'var(--border)'],
  [/#e8eaed\b/gi, 'var(--border)'],
  [/#9aa0a6\b/gi, 'var(--muted)'],
  [/#95a5a6\b/gi, 'var(--muted)'],
  [/#a1a1aa\b/gi, 'var(--muted)'],
  [/#7a879c\b/gi, 'var(--muted)'],
  [/#8a97ab\b/gi, 'var(--muted)'],
  [/#6e7681\b/gi, 'var(--muted)'],
  [/#596164\b/gi, 'var(--muted)'],
  [/#868f96\b/gi, 'var(--muted)'],
  [/#c0c0c0\b/gi, 'var(--border)'],
  [/#d8e0ec\b/gi, 'var(--border)'],
  [/#ccc\b/g, 'var(--border)'],
  [/#333\b/g, 'var(--kx-text)'],
  [/#444\b/g, 'var(--muted)'],
  [/#222\b/g, 'var(--kx-text)'],
  [/#111\b/g, 'var(--kx-text)'],
  [/#eee\b/g, 'var(--surface-secondary)'],
  [/#dcdfe6\b/gi, 'var(--border)'],
  [/#606266\b/gi, 'var(--muted)'],
  [/#d1fae5\b/gi, 'var(--success-bg)'],
  [/#dcfce7\b/gi, 'var(--success-bg)'],
  [/#ecfdf5\b/gi, 'var(--success-bg)'],
  [/#fef3c7\b/gi, 'var(--warning-bg)'],
  [/#fde68a\b/gi, 'var(--warning-bg)'],
  [/#fef0e0\b/gi, 'var(--warning-bg)'],
  [/#fff1f0\b/gi, 'var(--danger-bg)'],
  [/#fef2f2\b/gi, 'var(--danger-bg)'],
  [/#fee2e2\b/gi, 'var(--danger-bg)'],
  [/#fecaca\b/gi, 'var(--danger-bd)'],
  [/#fca5a5\b/gi, 'var(--danger-bd)'],
  [/#ffb4ad\b/gi, 'var(--danger-bd)'],
  [/#f7d58a\b/gi, 'var(--warning-bd)'],
  [/#fdba74\b/gi, 'var(--warning-bd)'],
  [/#dbeafe\b/gi, 'var(--info-bg)'],
  [/#bfdbfe\b/gi, 'var(--info-bd)'],
  [/#e0e7ff\b/gi, 'var(--info-bg)'],
  [/#eff6ff\b/gi, 'var(--info-bg)'],
  [/#fff1f1\b/gi, 'var(--danger-bg)'],
  [/#fff7e8\b/gi, 'var(--warning-bg)'],
  [/#eaf0ff\b/gi, 'var(--info-bg)'],
  [/#fffaf0\b/gi, 'var(--warning-bg)'],
  [/#e74c3c\b/gi, 'var(--danger)'],
  [/#f44336\b/gi, 'var(--danger)'],
  [/#e91e63\b/gi, 'var(--pink)'],
  [/#4f46e5\b/gi, 'var(--purple)'],
  [/#facc15\b/gi, 'var(--warning)'],
  [/#f0b429\b/gi, 'var(--warning)'],
  [/#ea580c\b/gi, 'var(--warning)'],
  [/#f43f5e\b/gi, 'var(--danger)'],
  [/#fb7185\b/gi, 'var(--danger)'],
  [/#f97583\b/gi, 'var(--danger)'],
  [/#f56c6c\b/gi, 'var(--danger)'],
  [/#F56C6C\b/g, 'var(--danger)'],

  // === 状态语义 (success/warning/danger/accent) ===
  [/#22c55e\b/gi, 'var(--success)'],
  [/#16a34a\b/gi, 'var(--success)'],
  [/#3fb950\b/gi, 'var(--success)'],
  [/#4ade80\b/gi, 'var(--success)'],
  [/#34d399\b/gi, 'var(--success)'],
  [/#16845b\b/gi, 'var(--success)'],
  [/#10b981\b/gi, 'var(--success)'],
  [/#86efac\b/gi, 'var(--success)'],
  [/#67C23A\b/gi, 'var(--success)'],

  [/#fbbf24\b/gi, 'var(--warning)'],
  [/#f59e0b\b/gi, 'var(--warning)'],
  [/#d97706\b/gi, 'var(--warning)'],
  [/#d29922\b/gi, 'var(--warning)'],
  [/#b7791f\b/gi, 'var(--warning)'],
  [/#e6a23c\b/gi, 'var(--warning)'],
  [/#eab308\b/gi, 'var(--warning)'],
  [/#fb923c\b/gi, 'var(--warning)'],
  [/#f97316\b/gi, 'var(--warning)'],

  [/#ef4444\b/gi, 'var(--danger)'],
  [/#dc2626\b/gi, 'var(--danger)'],
  [/#f87171\b/gi, 'var(--danger)'],
  [/#e74545\b/gi, 'var(--danger)'],
  [/#f85149\b/gi, 'var(--danger)'],
  [/#c2413b\b/gi, 'var(--danger)'],

  [/#3b82f6\b/gi, 'var(--accent)'],
  [/#2563eb\b/gi, 'var(--accent)'],
  [/#1d4ed8\b/gi, 'var(--accent)'],
  [/#1e4fd6\b/gi, 'var(--accent)'],
  [/#60a5fa\b/gi, 'var(--accent)'],
  [/#93c5fd\b/gi, 'var(--accent-h)'],
  [/#409EFF\b/gi, 'var(--accent)'],
  [/#1890ff\b/gi, 'var(--accent)'],

  // === Element Plus / 占位调试色 ===
  [/#909399\b/gi, 'var(--text-secondary)'],
  [/#8b949e\b/gi, 'var(--muted)'],
  [/#94a3b8\b/gi, 'var(--muted)'],
  [/#9ca3af\b/gi, 'var(--muted)'],
  [/#6b7280\b/gi, 'var(--muted)'],
  [/#4b5563\b/gi, 'var(--muted)'],
  [/#303133\b/gi, 'var(--text)'],
  [/#888\b/g, 'var(--muted)'],
  [/#aaa\b/g, 'var(--muted)'],
  [/#999\b/g, 'var(--muted)'],
  [/#666\b/g, 'var(--muted)'],
  [/#2a2a2a\b/gi, 'var(--bg)'],
  [/#0e0e0e\b/gi, 'var(--bg)'],
  [/#1f1f1f\b/gi, 'var(--bg)'],
  [/#152033\b/gi, 'var(--kx-text)'],
  [/#5b6b82\b/gi, 'var(--kx-muted)'],
  [/#e5e7eb\b/gi, 'var(--surface-secondary)'],
  [/#e6edf3\b/gi, 'var(--surface-secondary)'],
  [/#f0f0f0\b/gi, 'var(--surface-secondary)'],
  [/#f3f4f6\b/gi, 'var(--surface-secondary)'],
  [/#000\b/gi, 'var(--kx-text)'],

  // === rgba 软背景 ===
  // success bg (≤.15)
  [/rgba\(\s*22\s*,\s*197\s*,\s*94\s*,\s*(?:\.1|0?\.10|0?\.12|0?\.15)\s*\)/g, 'var(--success-bg)'],
  [/rgba\(\s*63\s*,\s*185\s*,\s*80\s*,\s*(?:\.1|0?\.10|0?\.12|0?\.15|0?\.14)\s*\)/g, 'var(--success-bg)'],
  [/rgba\(\s*52\s*,\s*211\s*,\s*153\s*,\s*(?:\.08|0?\.1|0?\.10|0?\.12|0?\.14|0?\.15|0?\.16)\s*\)/g, 'var(--success-bg)'],
  [/rgba\(\s*62\s*,\s*207\s*,\s*142\s*,\s*(?:\.08|0?\.1|0?\.14|0?\.15|0?\.16)\s*\)/g, 'var(--success-bg)'],
  [/rgba\(\s*34\s*,\s*197\s*,\s*94\s*,\s*(?:\.08|0?\.1|0?\.10|0?\.12|0?\.14|0?\.15|0?\.16)\s*\)/g, 'var(--success-bg)'],
  [/rgba\(\s*16\s*,\s*185\s*,\s*129\s*,\s*(?:\.08|0?\.1|0?\.10|0?\.12|0?\.14|0?\.15)\s*\)/g, 'var(--success-bg)'],

  // danger bg
  [/rgba\(\s*239\s*,\s*68\s*,\s*68\s*,\s*(?:\.1|0?\.10|0?\.12|0?\.15|0?\.14)\s*\)/g, 'var(--danger-bg)'],
  [/rgba\(\s*248\s*,\s*81\s*,\s*73\s*,\s*(?:\.1|0?\.10|0?\.12|0?\.15|0?\.14)\s*\)/g, 'var(--danger-bg)'],
  [/rgba\(\s*248\s*,\s*113\s*,\s*103\s*,\s*(?:\.08|0?\.1|0?\.10|0?\.12|0?\.14|0?\.15|0?\.16)\s*\)/g, 'var(--danger-bg)'],
  [/rgba\(\s*240\s*,\s*113\s*,\s*103\s*,\s*(?:\.08|0?\.1|0?\.14|0?\.15|0?\.16)\s*\)/g, 'var(--danger-bg)'],

  // warning bg
  [/rgba\(\s*245\s*,\s*158\s*,\s*11\s*,\s*(?:\.08|0?\.1|0?\.10|0?\.12|0?\.15|0?\.14)\s*\)/g, 'var(--warning-bg)'],
  [/rgba\(\s*251\s*,\s*191\s*,\s*36\s*,\s*(?:\.08|0?\.1|0?\.10|0?\.12|0?\.15|0?\.14)\s*\)/g, 'var(--warning-bg)'],
  [/rgba\(\s*234\s*,\s*179\s*,\s*8\s*,\s*(?:\.08|0?\.1|0?\.10|0?\.12|0?\.15|0?\.14)\s*\)/g, 'var(--warning-bg)'],
  [/rgba\(\s*210\s*,\s*153\s*,\s*34\s*,\s*(?:\.08|0?\.1|0?\.10|0?\.12|0?\.14|0?\.15)\s*\)/g, 'var(--warning-bg)'],
  [/rgba\(\s*224\s*,\s*165\s*,\s*58\s*,\s*(?:\.08|0?\.1|0?\.10|0?\.12|0?\.14|0?\.15)\s*\)/g, 'var(--warning-bg)'],

  // info bg
  [/rgba\(\s*59\s*,\s*130\s*,\s*246\s*,\s*(?:\.08|0?\.1|0?\.10|0?\.12|0?\.15|0?\.14)\s*\)/g, 'var(--info-bg)'],
  [/rgba\(\s*91\s*,\s*140\s*,\s*255\s*,\s*(?:\.08|0?\.1|0?\.10|0?\.12|0?\.14|0?\.15|0?\.16)\s*\)/g, 'var(--info-bg)'],

  // neutral bg
  [/rgba\(\s*139\s*,\s*148\s*,\s*158\s*,\s*(?:\.08|0?\.1|0?\.10|0?\.12|0?\.14|0?\.15|0?\.16)\s*\)/g, 'var(--neutral-bg)'],
  [/rgba\(\s*156\s*,\s*163\s*,\s*175\s*,\s*(?:\.08|0?\.1|0?\.10|0?\.12|0?\.14|0?\.15|0?\.16)\s*\)/g, 'var(--neutral-bg)'],

  // === rgba 边框/高亮 ===
  // success bd
  [/rgba\(\s*22\s*,\s*197\s*,\s*94\s*,\s*(?:\.2|0?\.20|0?\.25|0?\.3|0?\.30|0?\.35|0?\.4|0?\.40|0?\.85|0?\.9|0?\.95)\s*\)/g, 'var(--success-strong)'],
  [/rgba\(\s*63\s*,\s*185\s*,\s*80\s*,\s*(?:\.2|0?\.20|0?\.25|0?\.3|0?\.30|0?\.35|0?\.4|0?\.40)\s*\)/g, 'var(--success-bd)'],
  [/rgba\(\s*52\s*,\s*211\s*,\s*153\s*,\s*(?:\.2|0?\.20|0?\.25|0?\.3|0?\.30|0?\.35|0?\.4|0?\.40)\s*\)/g, 'var(--success-bd)'],

  // danger bd
  [/rgba\(\s*239\s*,\s*68\s*,\s*68\s*,\s*(?:\.2|0?\.20|0?\.25|0?\.3|0?\.30|0?\.35|0?\.4|0?\.40|0?\.95)\s*\)/g, 'var(--danger-strong)'],
  [/rgba\(\s*248\s*,\s*81\s*,\s*73\s*,\s*(?:\.2|0?\.20|0?\.25|0?\.3|0?\.30|0?\.35|0?\.4|0?\.40)\s*\)/g, 'var(--danger-bd)'],
  [/rgba\(\s*248\s*,\s*113\s*,\s*103\s*,\s*(?:\.2|0?\.20|0?\.25|0?\.3|0?\.30|0?\.35|0?\.4|0?\.40)\s*\)/g, 'var(--danger-bd)'],

  // warning bd / strong
  [/rgba\(\s*245\s*,\s*158\s*,\s*11\s*,\s*(?:\.2|0?\.20|0?\.25|0?\.3|0?\.30|0?\.35|0?\.4|0?\.40|0?\.95)\s*\)/g, 'var(--warning-strong)'],
  [/rgba\(\s*210\s*,\s*153\s*,\s*34\s*,\s*(?:\.2|0?\.20|0?\.25|0?\.3|0?\.30|0?\.35|0?\.4|0?\.40|0?\.45|0?\.5)\s*\)/g, 'var(--warning-bd)'],
  [/rgba\(\s*251\s*,\s*191\s*,\s*36\s*,\s*(?:\.2|0?\.20|0?\.25|0?\.3|0?\.30|0?\.35|0?\.4|0?\.40)\s*\)/g, 'var(--warning-bd)'],

  // neutral bd
  [/rgba\(\s*139\s*,\s*148\s*,\s*158\s*,\s*(?:\.2|0?\.20|0?\.25|0?\.3|0?\.30|0?\.35|0?\.4|0?\.40)\s*\)/g, 'var(--neutral-bd)'],

  // === overlay/shadow ===
  [/rgba\(\s*0\s*,\s*0\s*,\s*0\s*,\s*(?:\.4|0?\.40|0?\.45|0?\.5|0?\.50|0?\.55|0?\.6|0?\.60)\s*\)/g, 'var(--overlay-strong)'],
  [/rgba\(\s*0\s*,\s*0\s*,\s*0\s*,\s*(?:\.25|0?\.28|0?\.30|0?\.32|0?\.35)\s*\)/g, 'var(--overlay-medium)'],
  [/rgba\(\s*0\s*,\s*0\s*,\s*0\s*,\s*(?:\.15|0?\.18|0?\.20|0?\.22)\s*\)/g, 'var(--overlay-light)'],
  [/rgba\(\s*0\s*,\s*0\s*,\s*0\s*,\s*(?:\.08|0?\.10|0?\.12)\s*\)/g, 'var(--overlay-faint)'],

  // 浮起/分隔
  [/rgba\(\s*255\s*,\s*255\s*,\s*255\s*,\s*(?:\.02|0?\.03|0?\.04|0?\.05|0?\.06|0?\.08)\s*\)/g, 'color-mix(in srgb, var(--kx-text) 4%, transparent)'],
  [/rgba\(\s*15\s*,\s*23\s*,\s*42\s*,\s*(?:\.02|0?\.04|0?\.06|0?\.08|0?\.10)\s*\)/g, 'var(--shadow-color-light)'],

  // ===== GitHub / 探测 / Material 业务色(rgba 版) =====
  [/rgba\(\s*56\s*,\s*189\s*,\s*248\s*,\s*(?:0?\.0?6|0?\.0?8|0?\.1|0?\.1?0|0?\.1?5|0?\.1?8|0?\.2|0?\.2?0|0?\.2?5|0?\.3|0?\.4|0?\.5|0?\.6|0?\.7|0?\.8|0?\.9)\s*\)/g, 'color-mix(in srgb, var(--probe-cyan) 30%, transparent)'],
  [/rgba\(\s*96\s*,\s*165\s*,\s*250\s*,\s*(?:0?\.0?8|0?\.1|0?\.1?0|0?\.1?2|0?\.1?5|0?\.1?8|0?\.2)\s*\)/g, 'color-mix(in srgb, var(--accent) 15%, transparent)'],
  [/rgba\(\s*64\s*,\s*158\s*,\s*255\s*,\s*(?:0?\.1|0?\.1?2|0?\.1?5|0?\.2|0?\.2?0|0?\.2?5|0?\.3|0?\.4)\s*\)/g, 'color-mix(in srgb, var(--accent) 20%, transparent)'],
  [/rgba\(\s*30\s*,\s*79\s*,\s*214\s*,\s*(?:0?\.0?7|0?\.1|0?\.1?8)\s*\)/g, 'var(--info-bg)'],

  // 紫红/品红/紫
  [/rgba\(\s*167\s*,\s*139\s*,\s*250\s*,\s*(?:0?\.1|0?\.2|0?\.2?2|0?\.2?5|0?\.3)\s*\)/g, 'color-mix(in srgb, var(--purple) 22%, transparent)'],
  [/rgba\(\s*192\s*,\s*132\s*,\s*252\s*,\s*(?:0?\.1|0?\.2|0?\.2?2|0?\.2?5|0?\.3)\s*\)/g, 'color-mix(in srgb, var(--purple) 22%, transparent)'],
  [/rgba\(\s*217\s*,\s*70\s*,\s*239\s*,\s*(?:0?\.1|0?\.2|0?\.2?2|0?\.2?5|0?\.3)\s*\)/g, 'color-mix(in srgb, var(--magenta) 22%, transparent)'],

  // danger 边框/strong (高 alpha)
  [/rgba\(\s*239\s*,\s*68\s*,\s*68\s*,\s*(?:0?\.5|0?\.5?0|0?\.6|0?\.6?0|0?\.7|0?\.7?0|0?\.75|0?\.8|0?\.8?5|0?\.9|0?\.9?0|0?\.95)\s*\)/g, 'var(--danger-strong)'],
  [/rgba\(\s*248\s*,\s*81\s*,\s*73\s*,\s*(?:0?\.2?6|0?\.3|0?\.3?2|0?\.4|0?\.5)\s*\)/g, 'var(--danger-bd)'],
  [/rgba\(\s*248\s*,\s*113\s*,\s*103\s*,\s*(?:0?\.3|0?\.3?2|0?\.4|0?\.5)\s*\)/g, 'var(--danger-bd)'],
  [/rgba\(\s*63\s*,\s*185\s*,\s*80\s*,\s*(?:0?\.1?8|0?\.2|0?\.2?2|0?\.2?5|0?\.3|0?\.3?5|0?\.4)\s*\)/g, 'var(--success-bd)'],
  [/rgba\(\s*34\s*,\s*197\s*,\s*94\s*,\s*(?:0?\.1?8|0?\.2|0?\.2?2|0?\.2?5|0?\.3|0?\.4)\s*\)/g, 'var(--success-bd)'],

  // warning 边框/strong
  [/rgba\(\s*245\s*,\s*158\s*,\s*11\s*,\s*(?:0?\.1?8|0?\.2|0?\.2?2|0?\.2?4|0?\.2?5|0?\.3|0?\.4|0?\.5)\s*\)/g, 'var(--warning-bd)'],
  [/rgba\(\s*251\s*,\s*146\s*,\s*60\s*,\s*(?:0?\.1?5|0?\.1?8|0?\.2|0?\.2?2|0?\.2?5|0?\.3)\s*\)/g, 'var(--warning-bd)'],
  [/rgba\(\s*251\s*,\s*191\s*,\s*36\s*,\s*(?:0?\.2|0?\.2?2|0?\.2?5|0?\.3|0?\.4)\s*\)/g, 'var(--warning-bd)'],
  [/rgba\(\s*249\s*,\s*115\s*,\s*22\s*,\s*(?:0?\.1?4|0?\.1?6|0?\.1?8|0?\.2|0?\.2?2|0?\.2?4|0?\.2?5|0?\.3)\s*\)/g, 'var(--warning-bd)'],
  [/rgba\(\s*210\s*,\s*153\s*,\s*34\s*,\s*(?:0?\.2?4|0?\.3|0?\.4|0?\.5)\s*\)/g, 'var(--warning-bd)'],

  // 浮起/分隔 rgba 白
  [/rgba\(\s*255\s*,\s*255\s*,\s*255\s*,\s*(?:0?\.88|0?\.9?0|0?\.9?2)\s*\)/g, 'var(--surface-elevated)'],

  // ===== 进一步收敛剩余 rgba =====

  // danger 系 (248,113,113 — Tailwind red-400)
  [/rgba\(\s*248\s*,\s*113\s*,\s*113\s*,\s*(?:0?\.0?5|0?\.0?8|0?\.0?9|0?\.1|0?\.1?0|0?\.1?2|0?\.1?5|0?\.18|0?\.2|0?\.2?5|0?\.3|0?\.3?2|0?\.35|0?\.4|0?\.45|0?\.5|0?\.6|0?\.7|0?\.8|0?\.9|0?\.95)\s*\)/g, 'color-mix(in srgb, var(--danger) 12%, transparent)'],

  // danger 系 (248,81,73 — GitHub)
  [/rgba\(\s*248\s*,\s*81\s*,\s*73\s*,\s*(?:0?\.0?5|0?\.0?8|0?\.0?9|0?\.1|0?\.1?0|0?\.1?2|0?\.1?5|0?\.18|0?\.2|0?\.2?5|0?\.3|0?\.3?2|0?\.35|0?\.4|0?\.45|0?\.5|0?\.6|0?\.7|0?\.8|0?\.9|0?\.95)\s*\)/g, 'color-mix(in srgb, var(--danger) 12%, transparent)'],

  // danger 系 (248,113,103 — 其它变体)
  [/rgba\(\s*248\s*,\s*113\s*,\s*103\s*,\s*(?:0?\.0?8|0?\.0?9|0?\.1|0?\.1?0|0?\.1?2|0?\.1?5|0?\.2|0?\.2?5|0?\.3|0?\.3?5|0?\.4|0?\.45|0?\.5|0?\.6|0?\.7|0?\.8|0?\.9|0?\.95)\s*\)/g, 'color-mix(in srgb, var(--danger) 12%, transparent)'],

  // danger 系 (239,68,68 — Tailwind red-500)
  [/rgba\(\s*239\s*,\s*68\s*,\s*68\s*,\s*(?:0?\.0?8|0?\.0?9|0?\.1|0?\.1?0|0?\.1?2|0?\.1?5|0?\.18|0?\.2|0?\.2?2|0?\.2?5|0?\.3|0?\.35|0?\.4|0?\.45|0?\.5|0?\.6|0?\.7|0?\.8|0?\.9|0?\.95)\s*\)/g, 'color-mix(in srgb, var(--danger) 14%, transparent)'],

  // warning 系 (245,158,11)
  [/rgba\(\s*245\s*,\s*158\s*,\s*11\s*,\s*(?:0?\.0?5|0?\.0?8|0?\.0?9|0?\.1|0?\.1?0|0?\.1?2|0?\.1?5|0?\.18|0?\.2|0?\.2?2|0?\.2?5|0?\.3|0?\.35|0?\.4|0?\.45|0?\.5|0?\.6|0?\.7|0?\.8|0?\.9|0?\.95)\s*\)/g, 'color-mix(in srgb, var(--warning) 14%, transparent)'],

  // warning 系 (210,153,34)
  [/rgba\(\s*210\s*,\s*153\s*,\s*34\s*,\s*(?:0?\.0?5|0?\.0?8|0?\.0?9|0?\.1|0?\.1?0|0?\.1?2|0?\.1?5|0?\.18|0?\.2|0?\.2?2|0?\.2?5|0?\.3|0?\.35|0?\.4|0?\.45|0?\.5|0?\.6|0?\.7|0?\.8|0?\.9|0?\.95)\s*\)/g, 'color-mix(in srgb, var(--warning) 14%, transparent)'],

  // success 系 (63,185,80)
  [/rgba\(\s*63\s*,\s*185\s*,\s*80\s*,\s*(?:0?\.0?5|0?\.0?8|0?\.0?9|0?\.1|0?\.1?0|0?\.1?2|0?\.1?5|0?\.18|0?\.2|0?\.2?5|0?\.3|0?\.35|0?\.4)\s*\)/g, 'color-mix(in srgb, var(--success) 14%, transparent)'],

  // overlay 强 (0,.7 / 0,.75 / 0,.8 / 0,.85 / 0,.9)
  [/rgba\(\s*0\s*,\s*0\s*,\s*0\s*,\s*(?:0?\.7|0?\.7?0|0?\.7?5|0?\.8|0?\.8?5|0?\.9|0?\.9?0)\s*\)/g, 'var(--overlay-strong)'],
  [/rgba\(\s*0\s*,\s*0\s*,\s*0\s*,\s*(?:0?\.4|0?\.4?0|0?\.4?5|0?\.5|0?\.5?0|0?\.5?5|0?\.6|0?\.6?0|0?\.6?5)\s*\)/g, 'var(--overlay-medium)'],
  [/rgba\(\s*0\s*,\s*0\s*,\s*0\s*,\s*(?:0?\.1|0?\.1?0|0?\.1?5|0?\.2|0?\.2?0|0?\.2?5|0?\.3|0?\.3?0|0?\.3?5)\s*\)/g, 'var(--overlay-light)'],
  [/rgba\(\s*0\s*,\s*0\s*,\s*0\s*,\s*(?:0?\.0?5|0?\.0?8|0?\.0?9)\s*\)/g, 'var(--overlay-faint)'],

  // info 系 (59,130,246)
  [/rgba\(\s*59\s*,\s*130\s*,\s*246\s*,\s*(?:0?\.0?8|0?\.1|0?\.1?5|0?\.2|0?\.2?5|0?\.3|0?\.4|0?\.45|0?\.5|0?\.6|0?\.7|0?\.8)\s*\)/g, 'color-mix(in srgb, var(--accent) 16%, transparent)'],
  [/rgba\(\s*96\s*,\s*165\s*,\s*250\s*,\s*(?:0?\.0?8|0?\.1|0?\.1?5|0?\.2|0?\.2?5|0?\.3|0?\.4|0?\.5)\s*\)/g, 'color-mix(in srgb, var(--accent) 18%, transparent)'],
  [/rgba\(\s*64\s*,\s*158\s*,\s*255\s*,\s*(?:0?\.1|0?\.1?2|0?\.1?5|0?\.2|0?\.2?0|0?\.2?5|0?\.3|0?\.4)\s*\)/g, 'color-mix(in srgb, var(--accent) 20%, transparent)'],
  [/rgba\(\s*88\s*,\s*166\s*,\s*255\s*,\s*(?:0?\.0?8|0?\.1|0?\.1?5|0?\.2)\s*\)/g, 'color-mix(in srgb, var(--accent) 18%, transparent)'],
  [/rgba\(\s*30\s*,\s*79\s*,\s*214\s*,\s*(?:0?\.0?7|0?\.1|0?\.1?8|0?\.2|0?\.2?2)\s*\)/g, 'color-mix(in srgb, var(--accent) 18%, transparent)'],

  // 中性/灰系 (139,148,158 / 156,163,175)
  [/rgba\(\s*139\s*,\s*148\s*,\s*158\s*,\s*(?:0?\.1|0?\.1?5|0?\.18|0?\.2|0?\.3|0?\.4|0?\.95)\s*\)/g, 'color-mix(in srgb, var(--muted) 14%, transparent)'],
  [/rgba\(\s*156\s*,\s*163\s*,\s*175\s*,\s*(?:0?\.0?8|0?\.1|0?\.1?5|0?\.18|0?\.2|0?\.3|0?\.4|0?\.95)\s*\)/g, 'color-mix(in srgb, var(--muted) 14%, transparent)'],

  // 探针 cyan 系 (56,189,248)
  [/rgba\(\s*56\s*,\s*189\s*,\s*248\s*,\s*(?:0?\.1|0?\.1?5|0?\.2|0?\.2?5|0?\.3|0?\.4|0?\.5|0?\.6|0?\.7|0?\.8|0?\.9|0?\.95)\s*\)/g, 'color-mix(in srgb, var(--probe-cyan) 22%, transparent)'],

  // ===== 收敛残余高频 =====

  // 行 hover 极浅白底(几乎透明的白色覆盖)
  [/rgba\(\s*255\s*,\s*255\s*,\s*255\s*,\s*0?\.0?2\s*\)/g, 'var(--bg-hover)'],
  [/rgba\(\s*255\s*,\s*255\s*,\s*255\s*,\s*0?\.0?4\s*\)/g, 'var(--bg-hover)'],
  [/rgba\(\s*255\s*,\s*255\s*,\s*255\s*,\s*0?\.0?5\s*\)/g, 'var(--bg-hover)'],
  [/rgba\(\s*255\s*,\s*255\s*,\s*255\s*,\s*0?\.95\s*\)/g, 'var(--surface-elevated)'],

  // 深底遮罩/抽屉(15,23,42 — slate-900)
  [/rgba\(\s*15\s*,\s*23\s*,\s*42\s*,\s*0?\.55\s*\)/g, 'var(--overlay-medium)'],
  [/rgba\(\s*15\s*,\s*23\s*,\s*42\s*,\s*0?\.65\s*\)/g, 'var(--overlay-medium)'],
  [/rgba\(\s*15\s*,\s*23\s*,\s*42\s*,\s*0?\.75\s*\)/g, 'var(--overlay-strong)'],
  // 深底 0.78 (GitHub-style overlay)
  [/rgba\(\s*15\s*,\s*17\s*,\s*23\s*,\s*0?\.78\s*\)/g, 'var(--overlay-strong)'],
  [/rgba\(\s*15\s*,\s*17\s*,\s*23\s*,\s*0?\.55\s*\)/g, 'var(--overlay-medium)'],

  // 深蓝底色 30,45,75 / 30,55,100 (卡片 shadow)
  [/rgba\(\s*30\s*,\s*45\s*,\s*75\s*,\s*0?\.0?4\s*\)/g, 'var(--kx-shadow-sm)'],
  [/rgba\(\s*30\s*,\s*45\s*,\s*75\s*,\s*0?\.0?8\s*\)/g, 'var(--kx-shadow-md)'],
  [/rgba\(\s*30\s*,\s*55\s*,\s*100\s*,\s*0?\.0?6\s*\)/g, 'var(--kx-shadow-sm)'],

  // 深绿 success 系 (22,132,91)
  [/rgba\(\s*22\s*,\s*132\s*,\s*91\s*,\s*0?\.0?4\s*\)/g, 'var(--success-bg)'],
  [/rgba\(\s*22\s*,\s*132\s*,\s*91\s*,\s*0?\.1?4\s*\)/g, 'color-mix(in srgb, var(--success) 14%, transparent)'],

  // warning rgba(245,158,11,0.0) → 完全透明
  [/rgba\(\s*245\s*,\s*158\s*,\s*11\s*,\s*0\.0+\s*\)/g, 'transparent'],

  // accent rgba(59,130,246,.5) / (.25)
  [/rgba\(\s*59\s*,\s*130\s*,\s*246\s*,\s*0?\.25\s*\)/g, 'color-mix(in srgb, var(--accent) 25%, transparent)'],
  [/rgba\(\s*59\s*,\s*130\s*,\s*246\s*,\s*0?\.5\s*\)/g, 'color-mix(in srgb, var(--accent) 50%, transparent)'],
  // accent rgba(30,79,214,.22)
  [/rgba\(\s*30\s*,\s*79\s*,\s*214\s*,\s*0?\.2?2\s*\)/g, 'color-mix(in srgb, var(--accent) 22%, transparent)'],
  // rgba(88,166,255,.1)
  [/rgba\(\s*88\s*,\s*166\s*,\s*255\s*,\s*0?\.1\s*\)/g, 'color-mix(in srgb, var(--accent) 18%, transparent)'],
  // rgba(248,81,73,.18 / .1)
  [/rgba\(\s*248\s*,\s*81\s*,\s*73\s*,\s*0?\.1\s*\)/g, 'color-mix(in srgb, var(--danger) 12%, transparent)'],
  [/rgba\(\s*248\s*,\s*81\s*,\s*73\s*,\s*0?\.18\s*\)/g, 'color-mix(in srgb, var(--danger) 16%, transparent)'],
  // rgba(248,113,113,.15)
  [/rgba\(\s*248\s*,\s*113\s*,\s*113\s*,\s*0?\.1?5\s*\)/g, 'color-mix(in srgb, var(--danger) 15%, transparent)'],
  // rgba(248,113,113,.95) — 几乎是 danger 实色
  [/rgba\(\s*248\s*,\s*113\s*,\s*113\s*,\s*0?\.95\s*\)/g, 'color-mix(in srgb, var(--danger) 95%, transparent)'],
  // rgba(239,68,68,.22 / .1)
  [/rgba\(\s*239\s*,\s*68\s*,\s*68\s*,\s*0?\.1\s*\)/g, 'color-mix(in srgb, var(--danger) 14%, transparent)'],
  [/rgba\(\s*239\s*,\s*68\s*,\s*68\s*,\s*0?\.2?2\s*\)/g, 'color-mix(in srgb, var(--danger) 22%, transparent)'],
  // rgba(63,185,80,.08)
  [/rgba\(\s*63\s*,\s*185\s*,\s*80\s*,\s*0?\.0?8\s*\)/g, 'color-mix(in srgb, var(--success) 12%, transparent)'],
  // rgba(156,163,175,.95) → muted 高 alpha
  [/rgba\(\s*156\s*,\s*163\s*,\s*175\s*,\s*0?\.95\s*\)/g, 'color-mix(in srgb, var(--muted) 95%, transparent)'],
  // rgba(139,148,158,.18)
  [/rgba\(\s*139\s*,\s*148\s*,\s*158\s*,\s*0?\.18\s*\)/g, 'color-mix(in srgb, var(--muted) 16%, transparent)'],
  // rgba(56,189,248,.95) — probe-cyan 实色
  [/rgba\(\s*56\s*,\s*189\s*,\s*248\s*,\s*0?\.95\s*\)/g, 'color-mix(in srgb, var(--probe-cyan) 95%, transparent)'],

  // 黑色 overlay 0.24 / 0.38
  [/rgba\(\s*0\s*,\s*0\s*,\s*0\s*,\s*0?\.2?4\s*\)/g, 'var(--overlay-light)'],
  [/rgba\(\s*0\s*,\s*0\s*,\s*0\s*,\s*0?\.38\s*\)/g, 'var(--overlay-medium)'],

  // ===== 残余零散 hex → 业务色 token =====
  // 深橙 #c97800 / #b8821a / #b88230 / #f39c12 / #fee140 / #d97706
  [/#c97800\b/gi, 'var(--warning-dark)'],
  [/#b8821a\b/gi, 'var(--warning-dark)'],
  [/#b88230\b/gi, 'var(--warning-dark)'],
  [/#f39c12\b/gi, 'var(--warning)'],
  [/#fee140\b/gi, 'var(--warning)'],
  // 浅蓝边框 #a8c0f5 / #b7c9ef
  [/#a8c0f5\b/gi, 'var(--info-bd)'],
  [/#b7c9ef\b/gi, 'var(--info-bd)'],
  // 浅蓝 info-bg #e7eefc / #eef3f8
  [/#e7eefc\b/gi, 'var(--info-bg)'],
  [/#eef3f8\b/gi, 'var(--info-bg)'],
  // 浅绿 success-bg #e7f3ec
  [/#e7f3ec\b/gi, 'var(--success-bg)'],
  // 深蓝 #1841b3 / #0f3aad / #344258
  [/#1841b3\b/gi, 'var(--accent-dark)'],
  [/#0f3aad\b/gi, 'var(--accent-darker)'],
  [/#344258\b/gi, 'var(--kx-text)'],
  // 主背景 #f4f6f9
  [/#f4f6f9\b/gi, 'var(--kx-bg)'],
  // 蓝紫渐变 #667eea / #764ba2
  [/#667eea\b/gi, 'var(--accent)'],
  [/#764ba2\b/gi, 'var(--purple)'],
  // GitHub 蓝 #79c0ff
  [/#79c0ff\b/gi, 'var(--probe-cyan-light)'],
  // Material 绿 #4caf50 / #2ecc71
  [/#4caf50\b/gi, 'var(--success)'],
  [/#2ecc71\b/gi, 'var(--success)'],
  // 渐变 #43e97b / #38f9d7 (营销渐变专用 token)
  [/#43e97b\b/gi, 'var(--success)'],
  [/#38f9d7\b/gi, 'var(--probe-cyan-light)'],
  // 危险 #b42318 / #f3b4b0
  [/#b42318\b/gi, 'var(--danger)'],
  [/#f3b4b0\b/gi, 'var(--danger-bg)'],

  // ===== 残余零散 rgba → 业务色 mix =====
  // warning 系 (230,162,60 — Element Plus warm)
  [/rgba\(\s*230\s*,\s*162\s*,\s*60\s*,\s*0?\.18\s*\)/g, 'color-mix(in srgb, var(--warning) 18%, transparent)'],
  // success 系 (63,185,80,.16)
  [/rgba\(\s*63\s*,\s*185\s*,\s*80\s*,\s*0?\.16\s*\)/g, 'color-mix(in srgb, var(--success) 16%, transparent)'],
  // danger 系 (248,81,73,.16) GitHub red
  [/rgba\(\s*248\s*,\s*81\s*,\s*73\s*,\s*0?\.16\s*\)/g, 'color-mix(in srgb, var(--danger) 14%, transparent)'],
  // warning 系 (210,153,34,.16)
  [/rgba\(\s*210\s*,\s*153\s*,\s*34\s*,\s*0?\.16\s*\)/g, 'color-mix(in srgb, var(--warning) 16%, transparent)'],
  // 深底遮罩 (21,32,51,.16 / .45)
  [/rgba\(\s*21\s*,\s*32\s*,\s*51\s*,\s*0?\.16\s*\)/g, 'var(--overlay-light)'],
  [/rgba\(\s*21\s*,\s*32\s*,\s*51\s*,\s*0?\.45\s*\)/g, 'var(--overlay-medium)'],
  // warning 系 (217,119,6,.1)
  [/rgba\(\s*217\s*,\s*119\s*,\s*6\s*,\s*0?\.1\s*\)/g, 'color-mix(in srgb, var(--warning) 12%, transparent)'],

  // ===== 全等长 rgba/hex 重复兜底 =====
  // 已被更具体规则覆盖过的模式,这里不再加。
]

function walk(dir) {
  const out = []
  for (const entry of readdirSync(dir)) {
    if (entry === 'node_modules' || entry === '.git' || entry === 'dist') continue
    const full = join(dir, entry)
    const st = statSync(full)
    if (st.isDirectory()) out.push(...walk(full))
    else if (/\.(vue|ts|css|scss)$/.test(entry)) out.push(full)
  }
  return out
}

const args = process.argv.slice(2)
const DRY = args.includes('--dry-run')

let totalFiles = 0
let totalChanges = 0
const log = []
const reGlobal = (re) => (re.flags.includes('g') ? re : new RegExp(re.source, re.flags + 'g'))
const countMatches = (text, re) => (text.match(reGlobal(re)) || []).length

for (const file of walk(ROOT)) {
  const rel = relative(ROOT, file).replace(/\\/g, '/')
  if (SKIP.has(rel)) continue

  const src = readFileSync(file, 'utf8')
  let out = src
  let fileChanges = 0
  const isVue = rel.endsWith('.vue')

  const apply = (text) => {
    let next = text
    for (const [re, rep] of RULES) {
      fileChanges += countMatches(next, re)
      next = next.replace(re, rep)
    }
    return next
  }

  if (isVue) {
    out = out.replace(/(<style[^>]*>)([\s\S]*?)(<\/style>)/g, (m, open, body, close) => open + apply(body) + close)
  } else {
    out = apply(out)
  }

  if (fileChanges > 0) {
    log.push(`${fileChanges.toString().padStart(4)}  ${rel}`)
    totalChanges += fileChanges
    if (!DRY) writeFileSync(file, out, 'utf8')
    totalFiles++
  }
}

log.sort((a, b) => parseInt(b) - parseInt(a))
console.log(`=== color-token-fix (${DRY ? 'DRY-RUN' : 'APPLIED'}) ===`)
console.log(`文件数: ${totalFiles}, 替换数: ${totalChanges}`)
console.log(log.slice(0, 20).join('\n'))
if (log.length > 20) console.log(`... ${log.length - 20} more`)

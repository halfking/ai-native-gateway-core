#!/usr/bin/env node
// scripts/export-menu-config.mjs — 构建时导出 Gateway 菜单配置供 Maintain 动态加载
// 2026-07-23: 从 src/config/appNav.ts 导出菜单配置为 JSON

import fs from 'fs'
import path from 'path'
import { fileURLToPath } from 'url'

const __filename = fileURLToPath(import.meta.url)
const __dirname = path.dirname(__filename)

// 菜单配置（从 appNav.ts 提取的核心结构）
const menuConfig = {
  version: '1.0.0',
  exported_at: new Date().toISOString(),
  menus: [
    {
      path: '/dashboard',
      labelKey: 'nav.dashboard',
      labelFallback: '总览',
      icon: 'dashboard',
      tenantFilter: null, // null = 所有租户可见
    },
    {
      path: '/users',
      labelKey: 'nav.users',
      labelFallback: '用户',
      icon: 'users',
      tenantFilter: null,
    },
    {
      path: '/tenants',
      labelKey: 'nav.tenants',
      labelFallback: '租户',
      icon: 'tenants',
      tenantFilter: 'default', // 仅 default 租户可见
    },
    {
      path: '/providers',
      labelKey: 'nav.providers',
      labelFallback: '凭据池',
      icon: 'providers',
      tenantFilter: null,
    },
    {
      path: '/keys',
      labelKey: 'nav.keys',
      labelFallback: 'API 密钥',
      icon: 'keys',
      tenantFilter: null,
    },
    {
      path: '/mcp-gateway',
      labelKey: 'nav.mcpGateway',
      labelFallback: 'MCP 网关',
      icon: 'mcp',
      tenantFilter: null,
    },
    {
      path: '/gateway-management',
      labelKey: 'nav.gatewayManagement',
      labelFallback: '网关管理',
      icon: 'gateway',
      tenantFilter: null,
      children: [
        {
          path: '/routing-overview',
          labelKey: 'nav.routingOverview',
          labelFallback: '路由总览',
        },
        {
          path: '/examples',
          labelKey: 'nav.examples',
          labelFallback: '示例代码',
        },
        {
          path: '/chat',
          labelKey: 'nav.chat',
          labelFallback: '对话测试',
        },
      ],
    },
    {
      path: '/ops-center',
      labelKey: 'nav.opsCenter',
      labelFallback: '运维中心',
      icon: 'ops',
      tenantFilter: 'default', // 仅 default 租户可见
      children: [
        {
          path: '/logs',
          labelKey: 'nav.logs',
          labelFallback: '请求日志',
        },
        {
          path: '/metrics',
          labelKey: 'nav.metrics',
          labelFallback: '指标监控',
        },
        {
          path: '/probe-health',
          labelKey: 'nav.probeHealth',
          labelFallback: '健康探测',
        },
        {
          path: '/credential-monitor',
          labelKey: 'nav.credentialMonitor',
          labelFallback: '凭据监控',
        },
        {
          path: '/modules',
          labelKey: 'nav.modules',
          labelFallback: '模块管理',
        },
      ],
    },
  ],
}

// 输出路径
const outputPath = path.resolve(__dirname, '../public/menu-config.json')

// 写入文件
fs.writeFileSync(outputPath, JSON.stringify(menuConfig, null, 2), 'utf-8')

console.log(`✅ 菜单配置已导出: ${outputPath}`)
console.log(`   版本: ${menuConfig.version}`)
console.log(`   菜单数: ${menuConfig.menus.length}`)
console.log(`   导出时间: ${menuConfig.exported_at}`)

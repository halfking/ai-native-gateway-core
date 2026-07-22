#!/usr/bin/env tsx
/**
 * Export Gateway navigation config as JSON during build.
 * Output: public/nav-config.json (deployed with dist/)
 * 
 * This script runs automatically during `pnpm run build` to ensure
 * Maintain always loads the latest Gateway menu structure.
 */
import { writeFileSync, mkdirSync, readFileSync } from 'fs'
import { resolve, dirname } from 'path'

// Manually extract navigation items from appNav.ts source
// This avoids runtime imports that depend on Vite's import.meta
const appNavSource = readFileSync(resolve(__dirname, '../src/config/appNav.ts'), 'utf-8')

// Extract NAV_PRIMARY_ITEMS
const primaryMatch = appNavSource.match(/export const NAV_PRIMARY_ITEMS[^=]*=\s*\[([\s\S]*?)\] as const/)
const primaryJson = primaryMatch ? `[${primaryMatch[1]}]` : '[]'

// Extract NAV_GROUPS
const groupsMatch = appNavSource.match(/export const NAV_GROUPS[^=]*=\s*\[([\s\S]*?)\]\s*(?:as const)?(?:export|$)/)
const groupsJson = groupsMatch ? `[${groupsMatch[1]}]` : '[]'

// Parse and sanitize (remove comments, trailing commas)
function parseNavArray(jsonStr: string): any[] {
  // Remove comments
  const cleaned = jsonStr
    .replace(/\/\/[^\n]*/g, '')
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/,(\s*[}\]])/g, '$1') // trailing commas
  
  try {
    return eval(`(${cleaned})`)
  } catch (err) {
    console.error('[nav-export] Parse error:', err)
    return []
  }
}

const primary = parseNavArray(primaryJson)
const groups = parseNavArray(groupsJson)

const config = {
  version: '1.0',
  exportedAt: new Date().toISOString(),
  primary: primary.map(sanitizeNavItem),
  groups: groups.map((g: any) => ({
    id: g.id,
    labelKey: g.labelKey,
    items: (g.items || []).map(sanitizeNavItem),
  })),
}

function sanitizeNavItem(item: any): any {
  return {
    path: item.path || '',
    label: item.label || '',
    labelKey: item.labelKey,
    icon: item.icon || '',
    external: item.external || false,
    maintainOwned: item.maintainOwned || false,
    super: item.super || false,
    platformOps: item.platformOps || false,
    opsPlatform: item.opsPlatform || false,
    tenantOnly: item.tenantOnly || false,
    hideForTenant: item.hideForTenant || false,
  }
}

const outPath = resolve(__dirname, '../public/menu-config.json')
mkdirSync(dirname(outPath), { recursive: true })
writeFileSync(outPath, JSON.stringify(config, null, 2), 'utf-8')

console.log(`✅ [nav-export] Written to ${outPath}`)
console.log(`   - Primary items: ${config.primary.length}`)
console.log(`   - Groups: ${config.groups.length}`)
console.log(`   - Ops platform items: ${config.groups.find((g: any) => g.id === 'opsplatform')?.items.length || 0}`)

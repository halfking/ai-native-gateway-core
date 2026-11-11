// operations.permissions.test.ts —— operations 组权限对齐门（A-8，定稿 M3-T4）。
//
// 裁决（总体设计定稿 2026-11-11 §1 A-8）：operations 组对 platformOps 开放 ——
// marketing 页从 super-only 对齐到 platformOps（与组内 /pricing 同档）。
// 本门钉住三件事：
//   ① /operations/marketing 必须 requiresPlatformOps（不得退回 requiresSuper）；
//   ② appNav 里 marketing 菜单项必须 platformOps 旗（不得退回 super 旗）；
//   ③ 组内既有档位不被本轮误伤：sales 维持 requiresAuth（写权限应用层闸）、
//     pricing / model-pricing 维持 requiresPlatformOps。
// 文本级断言（与仓内 reconciliation.route.test.ts 同形态：读源码按标识符判）。

import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const root = resolve(process.cwd(), 'src')
const routerSrc = readFileSync(resolve(root, 'router.ts'), 'utf-8')
const navSrc = readFileSync(resolve(root, 'config', 'appNav.ts'), 'utf-8')

const routeLine = (path: string): string => {
  const hit = routerSrc
    .split('\n')
    .find((l) => l.includes(`path: '${path}'`))
  expect(hit, `router.ts 缺少路由 ${path}`).toBeTruthy()
  return hit as string
}

const navLine = (path: string): string => {
  const hit = navSrc
    .split('\n')
    .find((l) => l.includes(`path: '${path}'`))
  expect(hit, `appNav.ts 缺少菜单项 ${path}`).toBeTruthy()
  return hit as string
}

describe('operations 组权限对齐（A-8 / 定稿 M3-T4）', () => {
  it('/operations/marketing 路由档 = requiresPlatformOps（不得 super-only）', () => {
    const line = routeLine('/operations/marketing')
    expect(line).toContain('requiresPlatformOps: true')
    expect(line).not.toContain('requiresSuper: true')
  })

  it('/operations/marketing 菜单旗 = platformOps（不得 super）', () => {
    const line = navLine('/operations/marketing')
    expect(line).toContain('platformOps: true')
    expect(line).not.toContain('super: true')
  })

  it('组内既有档位不被误伤（sales=requiresAuth；pricing/model-pricing=platformOps）', () => {
    expect(routeLine('/operations/sales')).toContain('requiresAuth: true')
    expect(routeLine('/pricing')).toContain('requiresPlatformOps: true')
    expect(routeLine('/model-pricing')).toContain('requiresPlatformOps: true')
  })
})

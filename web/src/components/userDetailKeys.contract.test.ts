import { beforeAll, describe, expect, it } from 'vitest'
import { readFile } from 'node:fs/promises'
import { resolve } from 'node:path'

// userDetailKeys.contract.test.ts — 2026-11-17「用户详情 API 密钥管理」审计轮
// 的前端契约钉测。
//
// 为什么是**源码契约**而不是渲染测试：这一轮修的六个缺陷里有五个是
// 「代码写法」层面的（空串 vs undefined、'close' vs 'cancel'、明文残留、
// 状态语义、角色门禁），它们的失败模式在渲染快照里看不出来或需要构造
// 大量前置状态。源码契约把这些写法钉死，改回去就红。
//
// 被钉住的行为：
//   F1  i18n 键必须落在 8 语种 locale 文件里（不许用 t(key, '中文兜底')）
//   F2  数字字段空值必须归一成 undefined（不许把 '' 发给后端）
//   F3  ElMessageBox 的 'close' 必须与 'cancel' 一同视为「用户取消」
//   F4  关抽屉 / 切用户必须清空明文密钥状态
//   F5  状态徽章按真 status 渲染；revoked 不得提供「启用」
//   F6  必须接入 revealKey（查看存量密钥完整信息）
//   F7  密钥列表必须服务端过滤（tenant + owner），不许拉全量再前端筛
//   F8  密钥标签页必须按角色门禁（超管 + 租户管理员）

async function readRepoFile(...segments: string[]): Promise<string> {
  const candidates = [
    resolve(process.cwd(), ...segments),
    resolve(process.cwd(), 'web', ...segments),
  ]
  for (const path of candidates) {
    try {
      return await readFile(path, 'utf8')
    } catch {
      // 试下一个候选路径（从仓根跑 vs 从 web/ 跑）
    }
  }
  throw new Error(`${segments.join('/')} not found in candidate paths`)
}

const LOCALES = ['zh-CN', 'zh-TW', 'en-US', 'ja-JP', 'de-DE', 'fr-FR', 'es-ES', 'ar-SA']

describe('用户详情 API 密钥管理 · 前端契约', () => {
  let drawer: string
  let keysApi: string

  beforeAll(async () => {
    drawer = await readRepoFile('src', 'components', 'UserDetailDrawer.vue')
    keysApi = await readRepoFile('src', 'api', 'keys.ts')
  })

  // ── F1 i18n ────────────────────────────────────────────────
  it('F1 不使用「t(key, 中文兜底)」写法（该写法在本仓不被接受）', () => {
    // 模板与脚本里凡是 t('x.y', '...') 的第二参数带中文 ⇒ 违规
    const fallbackWithCjk = drawer.match(/t\(\s*'[^']+'\s*,\s*'[^']*[一-鿿][^']*'/g)
    expect(fallbackWithCjk).toBeNull()
  })

  it('F1 8 个语种都定义了 users.detail 的密钥相关键', async () => {
    const required = [
      'statsTab', 'keysTab', 'keysCount', 'createKey', 'createKeyTitle', 'noKeys',
      'loadKeysFailed', 'createKeyFailed', 'keyCreated',
      'revealKey', 'revealKeyTitle', 'revealKeyWarn', 'revealKeyFailed',
      'copyKeySuccess', 'copyKeyFailed',
      'revokeKey', 'revokeKeyConfirm', 'keyRevoked', 'revokeKeyFailed',
      'enableKey', 'disableKey', 'keyEnabled', 'keyDisabled', 'toggleKeyFailed',
      'keyAlias', 'keyBudget', 'keyRateLimit', 'keyRemark', 'keyApp', 'keyAppCode',
      'keyStatus', 'keyStatusActive', 'keyStatusPending', 'keyStatusRevoked',
      'keyStatusDisabled', 'keyStatusExpired', 'keyStatusThrottled',
      'keyRevokedNote', 'keyIsSystem', 'keyRequestCount', 'keyLastUsed',
      'keyNeverUsed', 'keyCreatedAt', 'keyPrefix',
    ]
    for (const locale of LOCALES) {
      const src = await readRepoFile('src', 'locales', locale, 'users.ts')
      for (const key of required) {
        expect(src, `${locale}/users.ts 缺少 ${key}`).toContain(`${key}:`)
      }
    }
  })

  // ── F2 数字字段空值 ────────────────────────────────────────
  it('F2 数字字段经 numOrUndef 归一，空串不得直接发给后端', () => {
    expect(drawer).toContain('function numOrUndef(')
    expect(drawer).toMatch(/budget_usd:\s*numOrUndef\(/)
    expect(drawer).toMatch(/rate_limit_rpm:\s*numOrUndef\(/)
    // 模板里不得用 v-model.number（空输入会得到 '' 而不是 null）。
    // ★ 只查**带等号的属性用法**，不查裸串 —— 裸串会命中文件头的说明注释，
    //   那是守卫自身的假阳性（注释里写「v-model.number 空输入得到 ''」是
    //   在描述被修掉的缺陷，不是缺陷本身）。
    expect(drawer).not.toMatch(/v-model\.number\s*=/)
  })

  // ── F3 确认框取消 ──────────────────────────────────────────
  it('F3 ElMessageBox 的 cancel 与 close 都算「用户取消」', () => {
    expect(drawer).toContain("e === 'cancel' || e === 'close'")
    expect(drawer).toContain('isDialogDismiss(e)')
  })

  // ── F4 明文密钥残留 ────────────────────────────────────────
  it('F4 关抽屉与切用户都会清空明文密钥状态', () => {
    expect(drawer).toContain('function resetSecretState()')
    // 关抽屉（modelValue 变 false）⇒ reset。
    // 匹配解构形参 `([open]) => {` ⇒ 正则里要有闭括号。
    expect(drawer).toMatch(/\(\s*\[\s*open\s*\]\s*\)\s*=>\s*\{\s*if\s*\(!open\)\s*\{\s*resetSecretState\(\)/)
    // 切用户 ⇒ reset
    expect(drawer).toMatch(/\(\)\s*=>\s*props\.user\?\.id[\s\S]{0,200}resetSecretState\(\)/)
    // 明文只存在一个 state 变量里，且有清空
    expect(drawer).toMatch(/revealedSecret\.value\s*=\s*''/)
  })

  // ── F5 状态语义 ────────────────────────────────────────────
  it('F5 状态按真 status 渲染，revoked 不得提供「启用」', () => {
    expect(drawer).toContain("status === 'revoked'")
    expect(drawer).toContain("status === 'pending'")
    expect(drawer).toContain("status === 'throttled'")
    expect(drawer).toMatch(/function canEnable\(k: ApiKey\)[^{]*\{\s*return keyState\(k\) !== 'revoked'/)
  })

  // ── F6 查看完整密钥 ────────────────────────────────────────
  it('F6 接入了 revealKey（查看存量密钥完整信息）', () => {
    expect(drawer).toContain('revealKey')
    expect(drawer).toMatch(/await revealKey\(key\.id\)/)
    expect(drawer).toContain('revealKeyTitle')
  })

  // ── F7 服务端过滤 ──────────────────────────────────────────
  it('F7 密钥列表走服务端过滤（tenant + owner），不是拉全量再前端筛', () => {
    expect(drawer).toMatch(/getKeys\(\{\s*tenant:\s*target\.tenant_id,\s*owner:\s*target\.username\s*\}\)/)
    // 不得出现「先拉全量再 filter」的旧写法
    expect(drawer).not.toMatch(/getKeys\(\)[\s\S]{0,200}\.filter\(/)
    expect(keysApi).toContain("qs.set('owner', params.owner)")
    expect(keysApi).toContain("qs.set('tenant', params.tenant)")
  })

  // ── F8 角色门禁 ────────────────────────────────────────────
  it('F8 密钥标签页按角色门禁（超管 + 租户管理员）', () => {
    expect(drawer).toContain('isSuperAdmin() || isTenantAdmin()')
    expect(drawer).toMatch(/v-if="canManageKeys"/)
  })
})

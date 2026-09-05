# i18n: Complete `nav.item.pluginSessions` Key Across All 8 Locales

## Summary

The `nav.item.pluginSessions` i18n key, referenced by `web/src/config/appNav.ts:102`
under the **请求与会话** group (sibling of `请求日志`), was defined in **0 of 8**
locale files. As a result, every non-zh-CN locale was falling back to the
Chinese label "会话列表" regardless of the user's chosen language.

## Root Cause

`appNav.ts:102` declares:

```ts
{ path: '/plugins/ai-session-manager/sessions',
  label: '会话列表',
  labelKey: 'nav.item.pluginSessions',  // ← never defined in any locale
  icon: '💬', super: true, hideForTenant: true, external: true,
  plugin: 'ai-session-manager' }
```

This is the entry point for the `ai-session-manager` plugin (added 2026-07-23
as a soft-injected plugin), shown to super_admin users under
**请求与会话 → 会话列表** (sub-menu of `/request-logs`).

The `keys_referenced.test.ts` scanner (warn-only by default, strict via
`I18N_STRICT=1`) flags every reference missing in non-source locales; this
key was the only `nav.item.*` reference unaccounted for in zh-CN itself
(`missing-in-source = 1`), which is the root-cause signal.

## Changes

Added `pluginSessions` to the `item` map in all 8 `web/src/locales/*/nav.ts`:

| Locale   | Translation                       |
| -------- | --------------------------------- |
| zh-CN    | 插件会话列表 (source-of-truth)    |
| zh-TW    | 插件會話列表                      |
| en-US    | Plugin Sessions                   |
| ja-JP    | プラグインセッション一覧          |
| ar-SA    | جلسات البرنامج المساعد            |
| de-DE    | Plugin-Sitzungen                  |
| es-ES    | Sesiones del plugin               |
| fr-FR    | Sessions du plugin                |

Insertion point: immediately after the existing `sessions` entry (same
semantic group, alphabetical-ish position).

## Verification

- `I18N_STRICT=1 npx vitest run src/i18n/keys_referenced.test.ts`
  - **4 / 4 tests passed**
  - Audit output: `322 files, 4914 refs, 7291 keys in zh-CN, 0 missing, 0 missing-in-source`
- `npx vitest run src/config/appNav.test.ts` → **5 / 5 passed**
- `npx vue-tsc --noEmit` → **no errors**
- `npx vitest run src/i18n/parity.test.ts` → unchanged baseline (2 pre-existing
  failures for `dashboard.liveStream.modeLarge/Small/Title`, unrelated to this fix;
  confirmed via `git stash` that these failures predate this commit).

## Risk / Rollback

Zero risk: pure additive i18n strings; no runtime logic change. Revert by
`git revert <sha>` — the key is defined in zh-CN as the source-of-truth and
in 7 sibling locales; reverting simply causes the previously-shown fallback
("会话列表") to reappear.
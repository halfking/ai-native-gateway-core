# i18n: Complete `dashboard.liveStream.modeSmall/modeLarge` Titles Across All 8 Locales

## Summary

The dashboard live-stream view toggle (small vs large mode) was missing
i18n keys in 6 of 8 locales, causing `parity.test.ts` to fail
with "missing key" errors every CI run. This was the only remaining
`parity.test.ts` failure after the `nav.item.pluginSessions` fix
(commit `51df592be`).

## Root Cause

`zh-CN` and `en-US` define these keys under `dashboard.liveStream.*`:

```ts
liveStream: {
  ...
  modeSmall: '小',
  modeLarge: '大',
  modeSmallTitle: '小模式：竖条显示，可容纳更多请求（默认）',
  modeLargeTitle: '大模式：卡片显示，包含更多请求详情',
  ...
}
```

But 6 locales (ar-SA / de-DE / es-ES / fr-FR / ja-JP / zh-TW) never
defined them — the surrounding block was left in English fallback
(`groupByVendor: 'By vendor'`, `probeAll: 'All'`, etc., a pre-existing
debt from 2026-07-22 parity work).

The `parity.test.ts` hard-fails any locale whose zh-CN leaf-key set is
not a subset, so these 4 missing keys × 6 locales blocked the gate.

## Changes

Added `modeSmall` / `modeLarge` / `modeSmallTitle` / `modeLargeTitle`
to `liveStream` map in 6 `web/src/locales/*/dashboard.ts` files
(ar-SA / de-DE / es-ES / fr-FR / ja-JP / zh-TW).

Translations:

| Locale   | modeSmall | modeLarge | modeSmallTitle                                      | modeLargeTitle                       |
| -------- | --------- | --------- | --------------------------------------------------- | ------------------------------------ |
| ar-SA    | صغير      | كبير      | وضع صغير: أعمدة عمودية، تستوعب المزيد من الطلبات (افتراضي) | وضع كبير: بطاقات بتفاصيل طلب أكثر   |
| de-DE    | Klein     | Groß      | Kleiner Modus: vertikale Balken, fasst mehr Anfragen (Standard) | Großer Modus: Karten mit mehr Anfragedetails |
| es-ES    | Pequeño   | Grande    | Modo pequeño: barras verticales, caben más solicitudes (predeterminado) | Modo grande: tarjetas con más detalle de solicitud |
| fr-FR    | Petit     | Grand     | Mode petit : barres verticales, contient plus de requêtes (par défaut) | Mode grand : cartes avec plus de détails sur les requêtes |
| ja-JP    | 小        | 大        | 小モード：縦棒表示、より多くのリクエストを収容（デフォルト） | 大モード：カード表示、より詳細なリクエスト情報 |
| zh-TW    | 小        | 大        | 小模式：直條顯示，可容納更多請求（預設）             | 大模式：卡片顯示，包含更多請求詳情   |

Insertion point: immediately after the existing
`groupByVendor/groupByProvider/groupByModel` line and before
`probeAll/probeOnly` (matches zh-CN ordering).

## Verification

- `npx vitest run src/i18n/parity.test.ts`
  - **5 / 5 tests passed** (was 3/5 with 2 failures pre-fix)
  - The 2 previously-failing tests now pass:
    - "every locale explicitly defines all zh-CN leaf keys"
    - "every locale resolves zh-CN module keys through the configured English fallback"
- `I18N_STRICT=1 npx vitest run src/i18n/keys_referenced.test.ts`
  - **4 / 4 tests passed**
  - Audit: `322 files, 4914 refs, 7291 keys in zh-CN, 0 missing, 0 missing-in-source`
- `npx vitest run src/config/appNav.test.ts` → **5 / 5 passed**
- `npx vue-tsc --noEmit` → no errors

## Risk / Rollback

Zero risk: pure additive i18n strings. Revert by `git revert <sha>` —
fallback behavior reverts to "missing key → console warning in dev mode,
display raw key string in production" (i.e. the user briefly sees
`dashboard.liveStream.modeSmall` instead of "小"/"Small"/etc.).

## Follow-up (out of scope)

The dashboard.ts files still contain ~10 other untranslated English
fallback strings (`emptyWaiting`, `cacheWindow`, `connectionDetailTitle`,
`dimensionVendor/Provider/Model`, `statusOpen/Connecting/...`,
`sseDetailTitle`, `editUrl`, etc.) inherited from the 2026-07-22 parity
work. These do **not** break the parity gate (they appear in both zh-CN
and all other locales), but they should be localized in a future
sweep. Tracked outside this PR.
# i18n: Translate `dashboard.{moduleStats,errors,performance,providerUsage}` Blocks Across All 6 Locales

## Summary

The 4 remaining dashboard blocks (`moduleStats` / `errors` / `performance` /
`providerUsage`) were left in English (or simplified Chinese for ja-JP/zh-TW)
after the 2026-07-22 parity work. These blocks render in the dashboard's
"模块执行统计" / "错误统计" / "性能指标" / "Provider 用量" cards — all
high-visibility UI. This commit completes the localization sweep.

## Root Cause

The parity gate (per `docs/changelogs/2026-07-22-i18n-locale-key-parity.md`)
only checks that every locale has the **same key set** as zh-CN. It does
**not** check that the values are localized. So the 4 blocks were left as:

- **ar-SA / de-DE / es-ES / fr-FR**: raw English strings
  (`"Throughput"`, `"Error Trend"`, `"P50 Latency"`, `"Provider usage"`,
  `"Search name, code, or ID…"`, `"Export all (Excel)"`, etc.)
- **ja-JP**: most keys translated to Japanese, but the entire
  `providerUsage` block (21 keys) and a few `title` / `totalRequests` /
  `avgLatency` strings in `moduleStats`/`errors`/`performance` were left
  in simplified Chinese (e.g. `"模块执行统计"`, `"错误统计"`, `"供应商用量"`)
- **zh-TW**: same simplified-Chinese residue plus the `providerUsage` block
  was entirely in simplified Chinese instead of Traditional Chinese
  (e.g. `"供应商"` should be `"供應商"`, `"当前周期"` should be `"當前週期"`)

## Scope

| Block           | Keys | Where it renders in UI                            |
| --------------- | ---- | ------------------------------------------------- |
| `moduleStats`   | 9    | Dashboard → 模块执行统计 card (title + 8 metrics) |
| `errors`        | 10   | Dashboard → 错误统计 card (title + 9 metrics)     |
| `performance`   | 9    | Dashboard → 性能指标 card (title + 8 metrics)     |
| `providerUsage` | 21   | Dashboard → Provider 用量 section (full view)     |

**Totals**:
- ar-SA / de-DE / es-ES / fr-FR: 49 keys each fully translated from English
- ja-JP: 4 simplified-Chinese corrections (`moduleStats.title`, `errors.title`,
  `errors.totalRequests`, `errors.avgLatency`, `performance.title`,
  `performance.avgLatency`, `performance.latency` — 7 corrections) +
  21 keys in `providerUsage` rewritten from simplified Chinese to Japanese
- zh-TW: 7 simplified→traditional corrections + 21 keys in `providerUsage`
  rewritten from simplified Chinese to Traditional Chinese

Total: ~210 string replacements across 6 files.

## Changes

### ar-SA

```
moduleStats.* → Arabic
  executions: عمليات التنفيذ
  cacheHitRate: معدل إصابة ذاكرة التخزين المؤقت
  rate: المعدل
  totalModules: إجمالي الوحدات
  totalExecutions: إجمالي عمليات التنفيذ
  avgCacheHitRate: متوسط معدل إصابة ذاكرة التخزين المؤقت
  avgDuration: متوسط المدة
  title: إحصائيات تنفيذ الوحدات
  successRate: معدل النجاح

errors.* → Arabic
  trendTitle: اتجاه الأخطاء
  errorCount: عدد الأخطاء
  totalCount: إجمالي الطلبات
  errorRate: معدل الخطأ
  count: العدد
  rate: المعدل
  totalErrors: إجمالي الأخطاء
  topErrors: أهم الأخطاء
  title: إحصائيات الأخطاء
  totalRequests: إجمالي الطلبات
  avgLatency: متوسط تأخير الخطأ

performance.* → Arabic
  throughput: معدل النقل
  requests: الطلبات
  p50/p95/p99: تأخير P50/P95/P99
  latencyDist: توزيع التأخير
  slowQueries: الاستعلامات البطيئة
  title: مقاييس الأداء
  avgLatency: متوسط التأخير
  latency: التأخير

providerUsage.* → Arabic (21 keys fully translated)
  title: استخدام المورد
  subtitle: {period} — استهلاك جميع الموردين (جاهز للتسوية)
  ... (all 21 keys)
```

### de-DE

```
moduleStats.* → German
  executions: Ausführungen
  cacheHitRate: Cache-Trefferrate
  rate: Rate
  totalModules: Gesamtzahl Module
  totalExecutions: Gesamtausführungen
  avgCacheHitRate: Durchschn. Cache-Trefferrate
  avgDuration: Durchschn. Dauer
  title: Modul-Ausführungsstatistik
  successRate: Erfolgsrate

errors.* → German
  trendTitle: Fehlertrend
  errorCount: Fehleranzahl
  totalCount: Gesamtanfragen
  errorRate: Fehlerrate
  count: Anzahl
  rate: Rate
  totalErrors: Gesamtfehler
  topErrors: Top-Fehler
  title: Fehlerstatistik
  totalRequests: Gesamtanfragen
  avgLatency: Durchschn. Fehlerlatenz

performance.* → German
  throughput: Durchsatz
  requests: Anfragen
  p50/p95/p99: P50/P95/P99 Latenz
  latencyDist: Latenzverteilung
  slowQueries: Langsame Abfragen
  title: Leistungsmetriken
  avgLatency: Durchschn. Latenz
  latency: Latenz

providerUsage.* → German (21 keys fully translated)
  title: Anbieternutzung
  subtitle: {period} — Gesamtverbrauch aller Anbieter (abgleichsbereit)
  ... (all 21 keys)
```

### es-ES

```
moduleStats.* → Spanish
  executions: Ejecuciones
  cacheHitRate: Tasa de acierto de caché
  rate: Tasa
  totalModules: Total de módulos
  totalExecutions: Total de ejecuciones
  avgCacheHitRate: Tasa promedio de acierto de caché
  avgDuration: Duración promedio
  title: Estadísticas de ejecución de módulos
  successRate: Tasa de éxito

errors.* → Spanish
  trendTitle: Tendencia de errores
  errorCount: Número de errores
  totalCount: Total de solicitudes
  errorRate: Tasa de error
  count: Cantidad
  rate: Tasa
  totalErrors: Total de errores
  topErrors: Errores principales
  title: Estadísticas de errores
  totalRequests: Total de solicitudes
  avgLatency: Latencia promedio de error

performance.* → Spanish
  throughput: Rendimiento
  requests: Solicitudes
  p50/p95/p99: Latencia P50/P95/P99
  latencyDist: Distribución de latencia
  slowQueries: Consultas lentas
  title: Métricas de rendimiento
  avgLatency: Latencia promedio
  latency: Latencia

providerUsage.* → Spanish (21 keys fully translated)
  title: Uso del proveedor
  subtitle: {period} — consumo de todos los proveedores (listo para conciliación)
  ... (all 21 keys)
```

### fr-FR

```
moduleStats.* → French
  executions: Exécutions
  cacheHitRate: Taux de succès du cache
  rate: Taux
  totalModules: Total des modules
  totalExecutions: Total des exécutions
  avgCacheHitRate: Taux de succès moyen du cache
  avgDuration: Durée moyenne
  title: Statistiques d'exécution des modules
  successRate: Taux de réussite

errors.* → French
  trendTitle: Tendance des erreurs
  errorCount: Nombre d'erreurs
  totalCount: Total des requêtes
  errorRate: Taux d'erreur
  count: Nombre
  rate: Taux
  totalErrors: Total des erreurs
  topErrors: Principales erreurs
  title: Statistiques d'erreurs
  totalRequests: Total des requêtes
  avgLatency: Latence moyenne des erreurs

performance.* → French
  throughput: Débit
  requests: Requêtes
  p50/p95/p99: Latence P50/P95/P99
  latencyDist: Distribution de latence
  slowQueries: Requêtes lentes
  title: Métriques de performance
  avgLatency: Latence moyenne
  latency: Latence

providerUsage.* → French (21 keys fully translated)
  title: Utilisation des fournisseurs
  subtitle: {period} — consommation de tous les fournisseurs (prêt pour rapprochement)
  ... (all 21 keys)
```

### ja-JP (7 simplified-Chinese corrections + 21-key providerUsage rewrite)

```
moduleStats.title: 模块执行统计 → モジュール実行統計
errors.title: 错误统计 → エラー統計
errors.totalRequests: 总请求数 → 総リクエスト数
errors.avgLatency: 平均错误延迟 → 平均エラーレイテンシ
performance.title: 性能指标 → パフォーマンス指標
performance.avgLatency: 平均延迟 → 平均レイテンシ
performance.latency: 延迟 → レイテンシ

providerUsage.* → Japanese (21 keys fully re-translated from simplified Chinese)
  title: プロバイダー使用状況
  subtitle: {period} 全プロバイダーの消費量（照合可能）
  periodHint: 現在の期間：{period}
  more: 詳細
  search: 名前・コード・ID で検索…
  back: 一覧に戻る
  exportAll: すべてエクスポート (Excel)
  exportDetail: 詳細をエクスポート (Excel)
  colName: プロバイダー
  colCode: コード
  colRequests: リクエスト数
  colTokens: トークン
  colCost: コスト (USD)
  colSuccess: 成功率
  colModel: モデル
  colDate: 日付
  periodDay/Week/Month: 日次 / 週次 / 月次
  periodLabel: 期間：{period}
  periodRange: {start} から {end}
  modelBreakdown: モデル別
  dailyBreakdown: 日次モデル内訳
```

### zh-TW (7 simplified→traditional corrections + 21-key providerUsage rewrite)

```
moduleStats.title: 模块执行统计 → 模組執行統計
errors.title: 错误统计 → 錯誤統計
errors.totalRequests: 总请求数 → 總請求數
errors.avgLatency: 平均错误延迟 → 平均錯誤延遲
performance.title: 性能指标 → 效能指標
performance.avgLatency: 平均延迟 → 平均延遲
performance.latency: 延迟 → 延遲

providerUsage.* → Traditional Chinese (21 keys fully re-translated)
  title: 供應商用量
  subtitle: {period} 全站供應商消耗彙總（可用於對帳）
  periodHint: 當前週期：{period}
  more: 更多
  search: 搜尋供應商名稱、代碼或 ID…
  back: 返回列表
  exportAll: 匯出全部 Excel
  exportDetail: 匯出明細 Excel
  colName: 供應商
  colCode: 代碼
  colRequests: 請求數
  colTokens: Token
  colCost: 成本 (USD)
  colSuccess: 成功率
  colModel: 模型
  colDate: 日期
  periodDay/Week/Month: 按天 / 按週 / 按月
  periodLabel: 統計週期：{period}
  periodRange: {start} 至 {end}
  modelBreakdown: 模型彙總
  dailyBreakdown: 每日模型明細
```

## Verification

- `I18N_STRICT=1 npx vitest run src/i18n/keys_referenced.test.ts` → **4 / 4 passed**
  - Audit: `322 files, 4914 refs, 7291 keys in zh-CN, 0 missing, 0 missing-in-source`
- `npx vitest run src/i18n/parity.test.ts` → **5 / 5 passed**
- `npx vitest run src/config/appNav.test.ts` → **5 / 5 passed**
- `npx vue-tsc --noEmit` → no errors
- Manual scan of each locale file: zero remaining English fallbacks in the
  4 touched blocks; zero remaining simplified-Chinese strings in ja-JP /
  zh-TW blocks (verified via grep for both `'Throughput`/`'Error Trend`/
  `'Provider usage` etc. and simplified-only chars like `模块`/`错误`/
  `性能`/`供应商`/`对账`/`明细`).

## Risk / Rollback

Zero functional risk: pure string replacement. Revert by `git revert <sha>`
restores the previous English-fallback / simplified-Chinese state.

## Follow-up (out of scope)

The broader `node scripts/i18n-audit.mjs --strict` audit still reports
~3130 missing keys across many other modules (e.g. `agentRegistryView.*`,
`apiKeySelectModal.*`, `app.*`, `approval.*`, `auditLog.*`, etc.). Those
keys are not referenced in zh-CN (`missing-in-source = 0`), so they don't
block CI — but they would silently fall back to the raw key string in
zh-CN-mode production if a future code path references them. Recommended
for a future dedicated sweep per module.

For dashboard specifically: all 4 blocks are now localized across all 8
locales — the dashboard is done.
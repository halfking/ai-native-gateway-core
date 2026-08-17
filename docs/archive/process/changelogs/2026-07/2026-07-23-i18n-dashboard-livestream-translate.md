# i18n: Translate `dashboard.liveStream` English Fallbacks to All 6 Locales

## Summary

The `dashboard.liveStream` block in 6 of 8 locales (`ar-SA` / `de-DE` / `es-ES`
/ `fr-FR` / `ja-JP` / `zh-TW`) was left with English fallback strings for ~30
keys after the 2026-07-22 parity work. While these strings did not break the
parity gate (zh-CN had them, so the key set was a superset), every non-zh-CN
user saw raw English UI strings like "Show all requests (default)",
"Cache / window", "Heartbeat placeholder", etc.

This commit fully translates the block to the appropriate language per locale
and removes a broken-merge typo in zh-TW.

## Root Cause

After the 2026-07-22 parity pass (commit history: see
`docs/changelogs/2026-07-22-i18n-locale-key-parity.md`), the parity gate
ensures every locale has **at least** the zh-CN leaf key set. It does **not**
ensure the values are actually translated — English fallback values
("By vendor", "All", "Connected", etc.) are technically valid TS string
literals and pass parity.

Specifically affected keys (30 total per locale):

```
emptyWaiting, groupByVendor, groupByProvider, groupByModel,
probeAll, probeOnly, probeAllTitle, probeOnlyTitle,
cacheWindow, connectionDetailTitle,
dimensionVendor, dimensionProvider, dimensionModel,
statusOpen, statusConnecting, statusReconnecting, statusUnsupported, statusClosed,
sseDetailTitle, sseStatusLabel, sseUrlLabel,
editUrl, editUrlPlaceholder, save, resetDefault,
cancel, testConnection, close,
sseTestOk, sseTestFail,
redisWarning, redisFallbackError,
probeDirect, probeGateway, probeScheduled, probeGeneric,
probeOriginLabel, probeAttempt,
originGateway, originScheduled, originDirect,
idleHeartbeat, tileIdle,
idleUnderOneMin, idleMinutes, idleHours, idleHoursMinutes, idleReasonNoTraffic
```

Plus a broken-merge typo in zh-TW:

```ts
empty等待: '等待 for live request stream data…',  // ← duplicate of emptyWaiting with broken key
emptyWaiting: '等待实时请求流数据…',               // ← valid key below
```

## Changes

Replaced the English fallback block in 6 `web/src/locales/*/dashboard.ts`
files with the corresponding translations (37 keys × 6 locales + 1 typo fix
= 223 string changes).

### Per-locale translation summary

#### ar-SA (37 keys translated)

```
emptyWaiting: في انتظار بيانات البث المباشر للطلبات…
groupByVendor: حسب المورّد, groupByProvider: حسب المزوّد, groupByModel: حسب النموذج
probeAll: الكل, probeOnly: عمليات الفحص فقط
probeAllTitle: عرض جميع الطلبات (افتراضي), probeOnlyTitle: عرض طلبات الفحص فقط
cacheWindow: ذاكرة التخزين المؤقت / النافذة, connectionDetailTitle: انقر لعرض تفاصيل الاتصال
dimensionVendor: المورّد, dimensionProvider: المزوّد, dimensionModel: النموذج
statusOpen: متصل, statusConnecting: جارٍ الاتصال, statusReconnecting: جارٍ إعادة الاتصال
statusUnsupported: غير مدعوم, statusClosed: غير متصل
sseDetailTitle: تفاصيل اتصال SSE, sseStatusLabel: حالة الاتصال, sseUrlLabel: عنوان SSE
editUrl: تحرير, editUrlPlaceholder: أدخل عنوان SSE, save: حفظ, resetDefault: افتراضي
cancel: إلغاء, testConnection: اختبار الاتصال, close: إغلاق
sseTestOk: اتصال SSE ناجح!\nالحالة: متصل\nالعنوان: {url}
sseTestFail: SSE غير متصل\nالحالة: {status}\nالعنوان: {url}
redisWarning: Redis غير متوفر: {error}. البيانات المباشرة تعود إلى استعلامات قاعدة البيانات.
redisFallbackError: فشل الاتصال بخدمة التخزين المؤقت
probeDirect: فحص نشط (مباشر مع المنبع)
probeGateway: فحص نشط (عبر البوابة)
probeScheduled: فحص مجدول (المجدول)
probeGeneric: طلب فحص
probeOriginLabel: المصدر: {origin}
probeAttempt: المحاولة: رقم {n}
originGateway: مسار البوابة
originScheduled: فحص مجدول
originDirect: مباشر مع المنبع
idleHeartbeat: عنصر نائب لنبضات القلب
tileIdle: خامل
idleUnderOneMin: خامل < 1 دقيقة
idleMinutes: خامل {n} دقيقة
idleHours: خامل {h} ساعة
idleHoursMinutes: خامل {h} ساعة {m} دقيقة
idleReasonNoTraffic: لا توجد حركة مرور (5 دقائق خاملة)
```

#### de-DE

```
emptyWaiting: Warten auf Live-Anfragestream-Daten…
groupBy*: Nach Anbieter / Provider / Modell
probe*: Alle / Nur Probes / Alle Anfragen anzeigen (Standard) / Nur Probe-Anfragen anzeigen
cacheWindow: Cache / Fenster, connectionDetailTitle: Klicken für Verbindungsdetails
dimension*: Anbieter / Provider / Modell
status*: Verbunden / Verbinden / Wiederverbinden / Nicht unterstützt / Nicht verbunden
sse*: SSE-Verbindungsdetails / Verbindungsstatus / SSE-URL
editUrl: Bearbeiten, editUrlPlaceholder: SSE-URL eingeben, save: Speichern, resetDefault: Standard
cancel: Abbrechen, testConnection: Verbindung testen, close: Schließen
sseTestOk/Fail: SSE-Verbindung OK!/nicht verbunden ...
redisWarning: Redis nicht verfügbar: {error}. Live-Daten fallen auf DB-Abfragen zurück.
redisFallbackError: Cache-Service-Verbindung fehlgeschlagen
probeDirect: Aktive Sonde (direkt zum Upstream)
probeGateway: Aktive Sonde (Gateway-Pfad)
probeScheduled: Geplante Sonde (Scheduler)
probeGeneric: Probe-Anfrage
probeOriginLabel: Quelle: {origin}
probeAttempt: Versuch: Nr. {n}
originGateway: Gateway-Pfad
originScheduled: Geplante Sonde
originDirect: Direkt zum Upstream
idleHeartbeat: Heartbeat-Platzhalter
tileIdle: Leer
idleUnderOneMin: Leer < 1 Min
idleMinutes: Leer {n} Min
idleHours: Leer {h} Std
idleHoursMinutes: Leer {h} Std {m} Min
idleReasonNoTraffic: Kein Traffic (5 Min Leerlauf)
```

#### es-ES

```
emptyWaiting: Esperando datos del flujo de solicitudes en vivo…
groupBy*: Por proveedor / proveedor / modelo
probe*: Todos / Solo sondas / Mostrar todas las solicitudes (predeterminado) / Mostrar solo solicitudes de sonda
cacheWindow: Caché / ventana, connectionDetailTitle: Clic para ver detalles de la conexión
dimension*: Proveedor / Proveedor / Modelo
status*: Conectado / Conectando / Reconectando / No compatible / Desconectado
sse*: Detalles de conexión SSE / Estado de conexión / URL SSE
editUrl: Editar, editUrlPlaceholder: Ingrese URL SSE, save: Guardar, resetDefault: Predeterminado
cancel: Cancelar, testConnection: Probar conexión, close: Cerrar
sseTestOk/Fail: ¡Conexión SSE correcta!/SSE no conectado ...
redisWarning: Redis no disponible: {error}. Los datos en vivo recurren a consultas a la base de datos.
redisFallbackError: Error de conexión del servicio de caché
probeDirect: Sonda activa (directa al upstream)
probeGateway: Sonda activa (ruta de gateway)
probeScheduled: Sonda programada (scheduler)
probeGeneric: Solicitud de sonda
probeOriginLabel: Origen: {origin}
probeAttempt: Intento: nº {n}
originGateway: Ruta del gateway
originScheduled: Sonda programada
originDirect: Directa al upstream
idleHeartbeat: Marcador de posición de latido
tileIdle: Inactivo
idleUnderOneMin: Inactivo < 1 min
idleMinutes: Inactivo {n} min
idleHours: Inactivo {h} h
idleHoursMinutes: Inactivo {h} h {m} min
idleReasonNoTraffic: Sin tráfico (5 min inactivo)
```

#### fr-FR

```
emptyWaiting: En attente des données du flux de requêtes en direct…
groupBy*: Par fournisseur / fournisseur / modèle
probe*: Tous / Sondes uniquement / Afficher toutes les requêtes (par défaut) / Afficher uniquement les requêtes de sonde
cacheWindow: Cache / fenêtre, connectionDetailTitle: Cliquer pour les détails de connexion
dimension*: Fournisseur / Fournisseur / Modèle
status*: Connecté / Connexion en cours / Reconnexion / Non pris en charge / Déconnecté
sse*: Détails de connexion SSE / État de connexion / URL SSE
editUrl: Modifier, editUrlPlaceholder: Entrer l'URL SSE, save: Enregistrer, resetDefault: Par défaut
cancel: Annuler, testConnection: Tester la connexion, close: Fermer
sseTestOk/Fail: Connexion SSE OK !/SSE non connecté ...
redisWarning: Redis indisponible : {error}. Les données en direct basculent vers des requêtes DB.
redisFallbackError: Échec de connexion du service de cache
probeDirect: Sonde active (directe vers l'upstream)
probeGateway: Sonde active (chemin passerelle)
probeScheduled: Sonde planifiée (planificateur)
probeGeneric: Requête de sonde
probeOriginLabel: Origine : {origin}
probeAttempt: Tentative : n°{n}
originGateway: Chemin passerelle
originScheduled: Sonde planifiée
originDirect: Directe vers l'upstream
idleHeartbeat: Espace réservé pour battement de cœur
tileIdle: Inactif
idleUnderOneMin: Inactif < 1 min
idleMinutes: Inactif {n} min
idleHours: Inactif {h} h
idleHoursMinutes: Inactif {h} h {m} min
idleReasonNoTraffic: Aucun trafic (5 min d'inactivité)
```

#### ja-JP (re-translated the Chinese-using bottom half too)

```
emptyWaiting: リアルタイムリクエストストリームデータを待機中…
groupBy*: ベンダー別 / プロバイダー別 / モデル別
probe*: すべて / プローブのみ / すべてのリクエストを表示（デフォルト） / プローブリクエストのみ表示
cacheWindow: キャッシュ / ウィンドウ, connectionDetailTitle: クリックで接続詳細を表示
dimension*: ベンダー / プロバイダー / モデル
status*: 接続済み / 接続中 / 再接続中 / 未対応 / 未接続
sse*: SSE 接続詳細 / 接続状態 / SSE URL
editUrl: 編集, editUrlPlaceholder: SSE URL を入力, save: 保存, resetDefault: デフォルト
cancel: キャンセル, testConnection: 接続テスト, close: 閉じる
sseTestOk/Fail: SSE 接続 OK！/SSE 未接続 ...
redisWarning: Redis 利用不可: {error}。ライブデータは DB クエリにフォールバックします。
redisFallbackError: キャッシュサービス接続失敗
probeDirect: アクティブプローブ（アップストリーム直接）
probeGateway: アクティブプローブ（ゲートウェイ経由）
probeScheduled: スケジュールプローブ（スケジューラ）
probeGeneric: プローブリクエスト
probeOriginLabel: ソース: {origin}
probeAttempt: 試行: 第{n}回
originGateway: ゲートウェイパス
originScheduled: スケジュールプローブ
originDirect: アップストリーム直接
idleHeartbeat: ハートビートプレースホルダ
tileIdle: アイドル
idleUnderOneMin: アイドル < 1分
idleMinutes: アイドル {n}分
idleHours: アイドル {h}時間
idleHoursMinutes: アイドル {h}時間{m}分
idleReasonNoTraffic: トラフィックなし（5分間アイドル）
```

#### zh-TW (translated English top half + simplified→traditional bottom + removed `empty等待:` typo)

```
emptyWaiting: 等待即時請求流資料…
groupBy*: 按原廠 / 按供應商 / 按模型
probe*: 全部 / 僅探測 / 顯示所有請求（預設） / 僅顯示探測請求
cacheWindow: 快取 / 視窗, connectionDetailTitle: 點擊查看連線詳情
dimension*: 原廠 / 供應商 / 模型
status*: 已連線 / 連線中 / 重新連線中 / 不支援 / 未連線
sse*: SSE 連線詳情 / 連線狀態 / SSE 位址
editUrl: 編輯, editUrlPlaceholder: 輸入 SSE 位址, save: 儲存, resetDefault: 預設
cancel: 取消, testConnection: 測試連線, close: 關閉
sseTestOk/Fail: SSE 連線正常！/SSE 未連線 ...
redisWarning: Redis 不可用：{error}。即時資料降級為資料庫查詢。
redisFallbackError: 快取服務連線失敗
probeDirect: 主動探測（直連上游）
probeGateway: 主動探測（閘道路徑）
probeScheduled: 週期探測（排程器）
probeGeneric: 探測請求
probeOriginLabel: 來源: {origin}
probeAttempt: 輪次: 第 {n} 輪
originGateway: 閘道路徑
originScheduled: 排程探測
originDirect: 直連上游
idleHeartbeat: 心跳佔位
tileIdle: 閒置
idleUnderOneMin: 閒置 < 1 分鐘
idleMinutes: 閒置 {n} 分鐘
idleHours: 閒置 {h} 小時
idleHoursMinutes: 閒置 {h} 小時 {m} 分鐘
idleReasonNoTraffic: 無流量（5 分鐘無請求）
```

## Verification

- `I18N_STRICT=1 npx vitest run src/i18n/keys_referenced.test.ts` → **4 / 4 passed**
  - Audit: `322 files, 4914 refs, 7291 keys in zh-CN, 0 missing, 0 missing-in-source`
- `npx vitest run src/i18n/parity.test.ts` → **5 / 5 passed**
- `npx vitest run src/config/appNav.test.ts` → **5 / 5 passed**
- `npx vue-tsc --noEmit` → no errors
- `git diff --stat`: 6 files changed, ~186 insertions / ~188 deletions (zh-TW
  has 1 net deletion because the `empty等待:` typo line was removed).

## Risk / Rollback

Zero functional risk: pure string replacement, no key renames, no key additions
or removals. Revert by `git revert <sha>` and all locales return to English
fallback strings (same UX as before this commit).
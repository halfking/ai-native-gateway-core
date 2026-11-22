#!/usr/bin/env python3
"""2026-11-23 统计标准轮：修正 dashboard.<locale>.md 里的两处**错误口径**。

① 时间窗口起点写的是「UTC 零点」——真实口径是**系统时区（默认 Asia/Shanghai）
   日历日零点**，且终点是当前时刻（不是整日）。2026-11-20 时区轮已把日切从
   UTC 改成系统时区，正文没跟上。
② 「总积分」写成「每笔扣减的积分累加」——真实口径是「基线之后的实时增量」，
   不随时间范围变化（请求级事实表的积分列当前恒为 0）。

另外在正文末尾补一节：与「会话与统计」页的关系（请求级 vs 会话级 ⇒ 数字本来
就不该相等），并指向两个页签里的「统计标准」按钮。

脚本幂等：目标串不在则跳过对应替换。
"""
import os
import sys

ROOT = os.path.join(os.path.dirname(os.path.abspath(__file__)), '..', 'content', 'page-help')

# locale: (UTC 行旧/新, 积分行旧/新, 积分补充段落, 末尾新增节)
EDITS = {
    'zh-CN': (
        '- 「今天」「近 7 天」「近 30 天」三种快捷范围按自然日切分，统计窗口起点是当天零点（世界标准时间 UTC）。',
        '- 「今天」「近 7 天」「近 30 天」三种快捷范围按自然日切分，统计窗口起点是**系统时区（默认 Asia/Shanghai）**当天零点，终点是当前时刻（不是整日）。',
        '| 总积分 | 平台内部计费口径的积分数 | 每笔扣减的积分累加 |',
        '| 总积分 | 平台内部计费口径的积分数 | ★ 当前显示的是基线快照之后的**实时增量**，不随时间范围变化（见下方说明） |',
        '积分卡的口径特殊：请求级事实表里的积分列当前恒为 0，页面显示的实际是「基线之后的实时增量」——切换「今天 / 近 7 天 / 近 30 天」积分卡的数字完全一样；同一页的用量趋势图里另有积分合计（另一个数据源），与积分卡不是一个数。需要可信的积分账目时，请用「用量成本」「对账报表」页。',
        '## 与「会话与统计」页对比\n\n本页是**请求级**口径（每一笔模型调用算一条），「会话与统计」页是**会话级**口径（一个会话算一条，取会话的累计值），两页的时间窗口与探测剔除规则也不同 ⇒ 数字本来就不该相等。需要逐项对照时，看「看板」与「会话与统计」两个页签里的 **统计标准** 按钮（那篇正文给出两页的逐项口径与差异对照表）。',
    ),
    'zh-TW': (
        '- 「今天」「近 7 天」「近 30 天」三種快捷範圍按自然日切分，統計視窗起點是當天零點（世界標準時間 UTC）。',
        '- 「今天」「近 7 天」「近 30 天」三種快捷範圍按自然日切分，統計視窗起點是**系統時區（預設 Asia/Shanghai）**當天零點，終點是當下時刻（不是整日）。',
        '| 總積分 | 平臺內部計費口徑的積分數 | 每筆扣減的積分累加 |',
        '| 總積分 | 平臺內部計費口徑的積分數 | ★ 目前顯示的是基線快照之後的**即時增量**，不隨時間範圍變化（見下方說明） |',
        '積分卡的口徑特殊：請求級事實表裡的積分欄目前恆為 0，頁面顯示的實際是「基線之後的即時增量」——切換「今天／近 7 天／近 30 天」積分卡的數字完全一樣；同一頁的用量趨勢圖裡另有積分合計（另一個資料來源），與積分卡不是同一個數。需要可信的積分帳目時，請用「用量成本」「對賬報表」頁。',
        '## 與「會話與統計」頁對比\n\n本頁是**請求級**口徑（每一筆模型呼叫算一條），「會話與統計」頁是**會話級**口徑（一個會話算一條，取會話的累計值），兩頁的時間視窗與探測剔除規則也不同 ⇒ 數字本來就不該相等。需要逐項對照時，看「看板」與「會話與統計」兩個頁簽裡的 **統計標準** 按鈕（那篇正文給出兩頁的逐項口徑與差異對照表）。',
    ),
    'en-US': (
        '- The three shortcut ranges, “Today,” “Last 7 Days,” and “Last 30 Days,” are split by calendar day, with the statistics window starting at midnight (Coordinated Universal Time UTC).',
        '- The three shortcut ranges, “Today,” “Last 7 Days,” and “Last 30 Days,” are split by calendar day; the statistics window starts at midnight in the **system time zone (Asia/Shanghai by default)** and ends at the current moment (not at the end of the day).',
        '| Total Credits | Number of credits under the platform’s internal billing system | Sums the credits deducted for each request |',
        '| Total Credits | Number of credits under the platform’s internal billing system | ★ Currently shows the **live delta after the cached baseline**, so it does not change with the time range (see below) |',
        'The credits card has a special rule: the credits column of the request-level fact table is currently always 0, so what the card shows is the “live delta after the baseline” — switching between “Today / Last 7 Days / Last 30 Days” leaves the credits card exactly unchanged. The usage trend chart on the same page carries a separate credits total (a different source) that is not the same number. For a trustworthy credit ledger, use the “Usage & Cost” and “Reconciliation” pages.',
        '## Compared with the “Session & Statistics” tab\n\nThis page uses the **request-level** caliber (one model call counts as one row), while “Session & Statistics” uses the **session-level** caliber (one session counts as one row, carrying that session’s cumulative values); the two tabs also differ in time window and in how probe traffic is excluded ⇒ the numbers are not supposed to be equal. For an item-by-item comparison, use the **Calculation rules** button inside the “Board” and “Session & Statistics” tabs (that document lists both calibers and the differences side by side).',
    ),
    'ja-JP': (
        '- 「今日」「直近 7 日」「直近 30 日」の3つのクイック範囲は自然日単位で区切られ、集計ウィンドウの開始時刻は当日0時（世界標準時刻 UTC）です。',
        '- 「今日」「直近 7 日」「直近 30 日」の3つのクイック範囲は自然日単位で区切られ、集計ウィンドウの開始時刻は**システムタイムゾーン（既定 Asia/Shanghai）の当日0時**、終了時刻は現在の瞬間です（日中の終わりではありません）。',
        '| 総クレジット | プラットフォーム内部の請求基準によるクレジット数 | 各リクエストから差し引かれたクレジットを累積 |',
        '| 総クレジット | プラットフォーム内部の請求基準によるクレジット数 | ★ 現在は基準値以降の**ライブ差分**を表示するため、時間範囲では変化しません（下記参照） |',
        'クレジットカードの基準は特殊です。リクエスト単位の事実テーブルのクレジット列は現在常に 0 のため、画面に出るのは「基準値以降のライブ差分」で、「今日／直近 7 日／直近 30 日」を切り替えてもクレジットカードの数字は変わりません。同じページの使用量トレンドには別のクレジット合計（別のデータソース）があり、カードの数字とは別物です。信頼できるクレジット台帳が必要な場合は「使用量コスト」「照合レポート」ページを使ってください。',
        '## 「セッション統計」タブとの比較\n\nこのページの集計基準は**リクエスト単位**（モデル呼び出し 1 回を 1 行として数えます）、「セッション統計」タブは**セッション単位**（セッション 1 件を 1 行とし、そのセッションの累計値を持ちます）で、両タブの時間範囲もプローブ除外の判定も異なります ⇒ 数字が一致するのは想定外です。項目ごとの比較が必要な場合は、「ボード」「セッション統計」タブ内の **集計ルール** ボタン（両ページの集計基準と差異を並べた文書）をご確認ください。',
    ),
    'de-DE': (
        '- Die drei Schnellbereiche „Heute“, „Letzte 7 Tage“ und „Letzte 30 Tage“ werden nach Kalendertagen unterteilt. Das statistische Zeitfenster beginnt um null Uhr des aktuellen Tages (koordinierte Weltzeit UTC).',
        '- Die drei Schnellbereiche „Heute“, „Letzte 7 Tage“ und „Letzte 30 Tage“ werden nach Kalendertagen unterteilt. Das statistische Zeitfenster beginnt um null Uhr in der **Systemzeitzone (Standard Asia/Shanghai)** und endet im aktuellen Moment (nicht am Tagesende).',
        '| Gesamtpunkte | Die Summe der Punkte gemäß der internen Abrechnung der Plattform | Summe der je Eintrag abgezogenen Punkte |',
        '| Gesamtpunkte | Die Summe der Punkte gemäß der internen Abrechnung der Plattform | ★ Zeigt derzeit die **Live-Differenz nach dem Cache-Stand** und ändert sich daher nicht mit dem Zeitfenster (siehe unten) |',
        'Die Punktekarte folgt einer besonderen Regel: Die Punktespalte der Anfrage-Faktentabelle ist derzeit immer 0, angezeigt wird die „Live-Differenz nach dem Baseline-Stand“ — der Wechsel zwischen „Heute / Letzte 7 Tage / Letzte 30 Tage“ lässt die Karte unverändert. Der Nutzungstrend derselben Seite führt eine eigene Punktsumme (aus einer anderen Quelle), die nicht der Zahl der Karte entspricht. Für ein belastbares Punktekonto nutzen Sie die Seiten „Nutzung & Kosten“ und „Abgleich“.',
        '## Vergleich mit dem Tab „Sitzungsstatistik“\n\nDiese Seite nutzt die **Anfrage-Ebene** (ein Modellaufruf zählt als eine Zeile), der Tab „Sitzungsstatistik“ die **Sitzungs-Ebene** (eine Sitzung zählt als eine Zeile mit den kumulierten Werten); Zeitfenster und Probe-Ausschluss unterscheiden sich ebenfalls ⇒ die Zahlen sollen nicht übereinstimmen. Für den Abgleich Position für Position nutzen Sie die Schaltfläche **Berechnungsregeln** in den Tabs „Board“ und „Sitzungsstatistik“ (dort stehen beide Berechnungsgrundlagen und die Unterschiede nebeneinander).',
    ),
    'es-ES': (
        '- Los tres intervalos rápidos «Hoy», «Últimos 7 días» y «Últimos 30 días» se dividen por días naturales; el inicio de la ventana de estadísticas es la medianoche del día actual (hora universal Coordinada UTC).',
        '- Los tres intervalos rápidos «Hoy», «Últimos 7 días» y «Últimos 30 días» se dividen por días naturales; la ventana de estadísticas empieza a la medianoche de la **zona horaria del sistema (Asia/Shanghai por defecto)** y termina en el momento actual (no al final del día).',
        '| Créditos totales | Número de créditos según el criterio de facturación interno de la plataforma | Suma de los créditos deducidos en cada solicitud |',
        '| Créditos totales | Número de créditos según el criterio de facturación interno de la plataforma | ★ Muestra el **delta en vivo posterior a la línea base en caché**, por lo que no cambia con el intervalo (véase abajo) |',
        'La tarjeta de créditos tiene una regla particular: la columna de créditos de la tabla de hechos a nivel de solicitud está siempre a 0, así que lo que muestra es el «delta en vivo posterior a la línea base»: cambiar entre «Hoy / Últimos 7 días / Últimos 30 días» deja la tarjeta exactamente igual. La tendencia de uso de la misma página lleva otra suma de créditos (otra fuente) que no coincide con la tarjeta. Para una contabilidad de créditos fiable use las páginas «Uso y coste» y «Conciliación».',
        '## Comparación con la pestaña «Estadísticas de sesión»\n\nEsta página usa el criterio de **nivel de solicitud** (una llamada a modelo cuenta como una fila), mientras que «Estadísticas de sesión» usa el criterio de **nivel de sesión** (una sesión cuenta como una fila con sus valores acumulados); la ventana temporal y la exclusión de sondeo también difieren ⇒ los números no deben coincidir. Para un contraste posición por posición, use el botón **Reglas de cálculo** de las pestañas «Panel» y «Estadísticas de sesión» (allí se listan ambos criterios y sus diferencias).',
    ),
    'fr-FR': (
        '- Les trois périodes rapides « Aujourd’hui », « 7 derniers jours » et « 30 derniers jours » sont délimitées par jour calendaire. Le début de la fenêtre statistique correspond à minuit le jour même (temps universel coordonné UTC).',
        '- Les trois périodes rapides « Aujourd’hui », « 7 derniers jours » et « 30 derniers jours » sont délimitées par jour calendaire. La fenêtre statistique commence à minuit dans le **fuseau du système (Asia/Shanghai par défaut)** et se termine à l’instant présent (pas à la fin de la journée).',
        '| Total des crédits | Nombre de crédits selon le référentiel de facturation interne de la plateforme | Somme des crédits déduits pour chaque appel |',
        '| Total des crédits | Nombre de crédits selon le référentiel de facturation interne de la plateforme | ★ Affiche actuellement le **delta temps réel postérieur à la base en cache**, donc il ne change pas avec la période (voir ci-dessous) |',
        'La carte des crédits suit une règle particulière : la colonne des crédits de la table de faits au niveau requête vaut toujours 0, si bien que l’affichage correspond au « delta temps réel postérieur à la ligne de base » — passer de « Aujourd’hui / 7 derniers jours / 30 derniers jours » laisse la carte strictement inchangée. La courbe d’usage de la même page porte une autre somme de crédits (autre source) qui ne correspond pas à la carte. Pour un registre de crédits fiable, utilisez les pages « Coûts d’usage » et « Rapprochement »..',
        '## Comparaison avec l’onglet « Statistiques de session »\n\nCette page applique le référentiel **au niveau requête** (un appel de modèle compte pour une ligne), tandis que « Statistiques de session » applique le référentiel **au niveau session** (une session compte pour une ligne avec ses valeurs cumulées) ; la fenêtre temporelle et l’exclusion de sonde diffèrent également ⇒ les chiffres ne sont pas censés coïncider. Pour un rapprochement poste par poste, utilisez le bouton **Règles de calcul** des onglets « Tableau de bord » et « Statistiques de session » (ce document y liste les deux référentiels et leurs écarts).',
    ),
}


def insert_before_next_heading(body: str, marker: str, paragraph: str) -> str:
    """在含 marker 的那张表之后、下一段 H2 之前插入段落（不把段落塞进表格中间）。"""
    lines = body.split('\n')
    marker_idx = next((i for i, line in enumerate(lines) if marker in line), None)
    if marker_idx is None:
        raise SystemExit(f'未找到积分行: {marker[:40]}…')
    heading_idx = next(
        (i for i in range(marker_idx + 1, len(lines)) if lines[i].startswith('## ')),
        len(lines),
    )
    before = '\n'.join(lines[:heading_idx]).rstrip('\n')
    after = '\n'.join(lines[heading_idx:])
    return f'{before}\n\n{paragraph}\n\n{after}'


def main() -> None:
    for locale, (utc_old, utc_new, cred_old, cred_new, cred_note, tail) in EDITS.items():
        path = os.path.join(ROOT, f'dashboard.{locale}.md')
        with open(path, encoding='utf-8') as handle:
            body = handle.read()

        if utc_old in body:
            body = body.replace(utc_old, utc_new, 1)
        else:
            print(f'{locale}: UTC 行未命中（可能已改）', file=sys.stderr)

        if cred_old in body:
            body = body.replace(cred_old, cred_new, 1)
        else:
            print(f'{locale}: 积分行未命中（可能已改）', file=sys.stderr)

        # 积分补充段落：插在积分卡所在小节的末尾（下一段 H2 之前）
        if cred_note not in body:
            body = insert_before_next_heading(body, cred_new, cred_note)

        # 末尾新增节
        heading = tail.split('\n', 1)[0]
        if heading not in body:
            body = body.rstrip('\n') + '\n\n' + tail + '\n'

        with open(path, 'w', encoding='utf-8') as handle:
            handle.write(body)
        print(f'{locale}: updated')


if __name__ == '__main__':
    main()

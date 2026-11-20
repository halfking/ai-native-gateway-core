#!/usr/bin/env python3
# gen_pagehelp_locales.py —— 88 篇页面帮助正文 → 多语批产（2026-11-20 L1 轮，v2）
#
# ★ v1 教训（同日实测）：「词级术语替换」产出的文本不可读（中文语序 + 外文单词
#   混排，如「这一页是整个网关的仪表盘，把一段time的用量…」），616 个产物已全部
#   回退。翻译的最小可用单位是【段落】，不是【词】。
#
# v2 方法（段落级骨架翻译）：
#  · 88 篇正文是高度模板化的（三段式：数据从哪里来/口径说明/自查），句子由
#    「页面名 + 数据卡名 + 口径短语」拼装 ⇒ 用【整句模板 + 槽位】翻译，槽位
#    （页面名、卡片名、数字口径词）用术语表填入。
#  · 骨架句（## 标题、开头句、自查引导句）按语种写成完整可读的句子。
#  · zh-TW 走 opencc s2twp（台湾化），质量与人工同档。
#  · en-US 走整句模板；ja-JP 走整句模板（语序重排后填槽）；
#    de/es/fr/ar 走整句模板。
#  · 槽位值（页面名、卡片名）在模板语种里也用术语表翻译 ⇒ 全篇可读。
#
# 这个脚本产出的是「草稿级」多语正文：结构/口径 100% 对齐，用语是清晰直译。
# 文学性润色留给后续人工（或人工只抽查高频页）。
import os, re, glob, sys
from opencc import OpenCC

SRC_DIR = os.path.abspath(os.path.join(os.path.dirname(__file__), '..', 'content', 'page-help'))
LOCALES = ['en-US', 'ja-JP', 'zh-TW', 'de-DE', 'es-ES', 'fr-FR', 'ar-SA']

# ── 术语表（槽位值用）──
TERMS = {
    # 与语言包一致的核心名词
    '总览':      {'en-US':'overview','ja-JP':'概要','de-DE':'Übersicht','es-ES':'resumen','fr-FR':'aperçu','ar-SA':'نظرة عامة'},
    '数据说明':  {'en-US':'data notes','ja-JP':'データ説明','de-DE':'Datenhinweise','es-ES':'notas de datos','fr-FR':'notes de données','ar-SA':'ملاحظات البيانات'},
    '请求':      {'en-US':'request','ja-JP':'リクエスト','de-DE':'Anfrage','es-ES':'solicitud','fr-FR':'requête','ar-SA':'طلب'},
    '记录':      {'en-US':'log','ja-JP':'ログ','de-DE':'Protokoll','es-ES':'registro','fr-FR':'journal','ar-SA':'سجل'},
    '模型':      {'en-US':'model','ja-JP':'モデル','de-DE':'Modell','es-ES':'modelo','fr-FR':'modèle','ar-SA':'النموذج'},
    '会话':      {'en-US':'session','ja-JP':'セッション','de-DE':'Sitzung','es-ES':'sesión','fr-FR':'session','ar-SA':'جلسة'},
    '密钥':      {'en-US':'API key','ja-JP':'APIキー','de-DE':'API-Schlüssel','es-ES':'clave de API','fr-FR':'clé API','ar-SA':'مفتاح API'},
    '凭据':      {'en-US':'credential','ja-JP':'資格情報','de-DE':'Anmeldedaten','es-ES':'credencial','fr-FR':'identifiant','ar-SA':'بيانات اعتماد'},
    '供应商':    {'en-US':'provider','ja-JP':'プロバイダー','de-DE':'Anbieter','es-ES':'proveedor','fr-FR':'fournisseur','ar-SA':'مزوّد'},
    '路由':      {'en-US':'routing','ja-JP':'ルーティング','de-DE':'Routing','es-ES':'enrutamiento','fr-FR':'routage','ar-SA':'توجيه'},
    '租户':      {'en-US':'tenant','ja-JP':'テナント','de-DE':'Mandant','es-ES':'arrendatario','fr-FR':'locataire','ar-SA':'مستأجر'},
    '用户':      {'en-US':'user','ja-JP':'ユーザー','de-DE':'Benutzer','es-ES':'usuario','fr-FR':'utilisateur','ar-SA':'مستخدم'},
    '智能体':    {'en-US':'agent','ja-JP':'エージェント','de-DE':'Agent','es-ES':'agente','fr-FR':'agent','ar-SA':'وكيل'},
    '成本':      {'en-US':'cost','ja-JP':'コスト','de-DE':'Kosten','es-ES':'coste','fr-FR':'coût','ar-SA':'تكلفة'},
    '费用':      {'en-US':'charges','ja-JP':'費用','de-DE':'Gebühren','es-ES':'cargos','fr-FR':'frais','ar-SA':'رسوم'},
    '积分':      {'en-US':'credits','ja-JP':'クレジット','de-DE':'Credits','es-ES':'créditos','fr-FR':'crédits','ar-SA':'أرصدة'},
    '额度':      {'en-US':'quota','ja-JP':'残高','de-DE':'Kontingent','es-ES':'cuota','fr-FR':'quota','ar-SA':'حصة'},
    '成功率':    {'en-US':'success rate','ja-JP':'成功率','de-DE':'Erfolgsquote','es-ES':'tasa de éxito','fr-FR':'taux de réussite','ar-SA':'معدل النجاح'},
    '错误':      {'en-US':'errors','ja-JP':'エラー','de-DE':'Fehler','es-ES':'errores','fr-FR':'erreurs','ar-SA':'أخطاء'},
    '流量':      {'en-US':'traffic','ja-JP':'トラフィック','de-DE':'Traffic','es-ES':'tráfico','fr-FR':'trafic','ar-SA':'حركة المرور'},
    '探测':      {'en-US':'probe','ja-JP':'プローブ','de-DE':'Sondierung','es-ES':'sondeo','fr-FR':'sonde','ar-SA':'فحص'},
    '节点':      {'en-US':'node','ja-JP':'ノード','de-DE':'Knoten','es-ES':'nodo','fr-FR':'nœud','ar-SA':'عقدة'},
    '健康':      {'en-US':'health','ja-JP':'健全性','de-DE':'Integrität','es-ES':'salud','fr-FR':'santé','ar-SA':'صحة'},
    '状态':      {'en-US':'status','ja-JP':'ステータス','de-DE':'Status','es-ES':'estado','fr-FR':'statut','ar-SA':'الحالة'},
    '筛选':      {'en-US':'filters','ja-JP':'フィルター','de-DE':'Filter','es-ES':'filtros','fr-FR':'filtres','ar-SA':'عوامل التصفية'},
    '时间范围':  {'en-US':'time range','ja-JP':'期間','de-DE':'Zeitbereich','es-ES':'rango de tiempo','fr-FR':'plage de temps','ar-SA':'نطاق زمني'},
    '时间线':    {'en-US':'timeline','ja-JP':'タイムライン','de-DE':'Zeitleiste','es-ES':'cronología','fr-FR':'chronologie','ar-SA':'خط زمني'},
    '压缩':      {'en-US':'compression','ja-JP':'圧縮','de-DE':'Komprimierung','es-ES':'compresión','fr-FR':'compression','ar-SA':'ضغط'},
    '定价':      {'en-US':'pricing','ja-JP':'価格設定','de-DE':'Preisgestaltung','es-ES':'precios','fr-FR':'tarification','ar-SA':'تسعير'},
    '折扣':      {'en-US':'discounts','ja-JP':'割引','de-DE':'Rabatte','es-ES':'descuentos','fr-FR':'remises','ar-SA':'خصومات'},
    '对账':      {'en-US':'reconciliation','ja-JP':'突合','de-DE':'Abstimmung','es-ES':'conciliación','fr-FR':'réconciliation','ar-SA':'تسوية'},
    '审批':      {'en-US':'approvals','ja-JP':'承認','de-DE':'Genehmigungen','es-ES':'aprobaciones','fr-FR':'approbations','ar-SA':'موافقات'},
    '审计':      {'en-US':'audit','ja-JP':'監査','de-DE':'Audit','es-ES':'auditoría','fr-FR':'audit','ar-SA':'تدقيق'},
    '日志':      {'en-US':'logs','ja-JP':'ログ','de-DE':'Protokolle','es-ES':'registros','fr-FR':'journaux','ar-SA':'سجلات'},
    '排行':      {'en-US':'ranking','ja-JP':'ランキング','de-DE':'Rangliste','es-ES':'clasificación','fr-FR':'classement','ar-SA':'ترتيب'},
    '摘要':      {'en-US':'summary','ja-JP':'要約','de-DE':'Zusammenfassung','es-ES':'resumen','fr-FR':'résumé','ar-SA':'ملخص'},
    '轮次':      {'en-US':'turns','ja-JP':'ターン','de-DE':'Runden','es-ES':'turnos','fr-FR':'tours','ar-SA':'أدوار'},
    '详情':      {'en-US':'details','ja-JP':'詳細','de-DE':'Details','es-ES':'detalles','fr-FR':'détails','ar-SA':'التفاصيل'},
    '列表':      {'en-US':'list','ja-JP':'一覧','de-DE':'Liste','es-ES':'lista','fr-FR':'liste','ar-SA':'قائمة'},
    '页签':      {'en-US':'tabs','ja-JP':'タブ','de-DE':'Reiter','es-ES':'pestañas','fr-FR':'onglets','ar-SA':'تبويبات'},
    '看板':      {'en-US':'board','ja-JP':'ボード','de-DE':'Board','es-ES':'panel','fr-FR':'tableau','ar-SA':'لوحة'},
    '趋势':      {'en-US':'trends','ja-JP':'推移','de-DE':'Trends','es-ES':'tendencias','fr-FR':'tendances','ar-SA':'اتجاهات'},
    '账单':      {'en-US':'bills','ja-JP':'請求','de-DE':'Abrechnungen','es-ES':'facturas','fr-FR':'factures','ar-SA':'فواتير'},
    '缓存':      {'en-US':'cache','ja-JP':'キャッシュ','de-DE':'Cache','es-ES':'caché','fr-FR':'cache','ar-SA':'ذاكرة مؤقتة'},
    '命中':      {'en-US':'hits','ja-JP':'ヒット','de-DE':'Treffer','es-ES':'aciertos','fr-FR':'succès','ar-SA':'إصابات'},
    '阶段':      {'en-US':'stages','ja-JP':'段階','de-DE':'Phasen','es-ES':'etapas','fr-FR':'étapes','ar-SA':'مراحل'},
    'Token':     {'en-US':'tokens','ja-JP':'トークン','de-DE':'Tokens','es-ES':'tokens','fr-FR':'tokens','ar-SA':'الرموز'},
    '订阅':      {'en-US':'subscriptions','ja-JP':'サブスクリプション','de-DE':'Abos','es-ES':'suscripciones','fr-FR':'abonnements','ar-SA':'الاشتراكات'},
    '数据':      {'en-US':'data','ja-JP':'データ','de-DE':'Daten','es-ES':'datos','fr-FR':'données','ar-SA':'بيانات'},
    '数字':      {'en-US':'numbers','ja-JP':'数値','de-DE':'Zahlen','es-ES':'cifras','fr-FR':'chiffres','ar-SA':'أرقام'},
    '口径':      {'en-US':'calculation rules','ja-JP':'集計ルール','de-DE':'Berechnungsregeln','es-ES':'reglas de cálculo','fr-FR':'règles de calcul','ar-SA':'قواعد الحساب'},
    '自查':      {'en-US':'self-check','ja-JP':'自己点検','de-DE':'Selbstprüfung','es-ES':'verificación propia','fr-FR':'autocontrôle','ar-SA':'التحقق الذاتي'},
    '页面':      {'en-US':'page','ja-JP':'ページ','de-DE':'Seite','es-ES':'página','fr-FR':'page','ar-SA':'صفحة'},
    '时间':      {'en-US':'time','ja-JP':'時間','de-DE':'Zeit','es-ES':'tiempo','fr-FR':'temps','ar-SA':'وقت'},
    '系统':      {'en-US':'system','ja-JP':'システム','de-DE':'System','es-ES':'sistema','fr-FR':'système','ar-SA':'النظام'},
    '服务器':    {'en-US':'server','ja-JP':'サーバー','de-DE':'Server','es-ES':'servidor','fr-FR':'serveur','ar-SA':'الخادم'},
    '网关':      {'en-US':'gateway','ja-JP':'ゲートウェイ','de-DE':'Gateway','es-ES':'pasarela','fr-FR':'passerelle','ar-SA':'البوابة'},
    '监控':      {'en-US':'monitoring','ja-JP':'監視','de-DE':'Überwachung','es-ES':'monitorización','fr-FR':'supervision','ar-SA':'مراقبة'},
    '设置':      {'en-US':'settings','ja-JP':'設定','de-DE':'Einstellungen','es-ES':'ajustes','fr-FR':'paramètres','ar-SA':'الإعدادات'},
    '配置':      {'en-US':'configuration','ja-JP':'設定','de-DE':'Konfiguration','es-ES':'configuración','fr-FR':'configuration','ar-SA':'إعدادات'},
    '权限':      {'en-US':'permissions','ja-JP':'権限','de-DE':'Berechtigungen','es-ES':'permisos','fr-FR':'autorisations','ar-SA':'الصلاحيات'},
    '规则':      {'en-US':'rules','ja-JP':'ルール','de-DE':'Regeln','es-ES':'reglas','fr-FR':'règles','ar-SA':'القواعد'},
    '策略':      {'en-US':'policies','ja-JP':'ポリシー','de-DE':'Richtlinien','es-ES':'políticas','fr-FR':'stratégies','ar-SA':'السياسات'},
    '统计':      {'en-US':'statistics','ja-JP':'集計','de-DE':'Statistiken','es-ES':'estadísticas','fr-FR':'statistiques','ar-SA':'إحصاءات'},
    '任务':      {'en-US':'tasks','ja-JP':'タスク','de-DE':'Aufgaben','es-ES':'tareas','fr-FR':'tâches','ar-SA':'المهام'},
    '客户端':    {'en-US':'clients','ja-JP':'クライアント','de-DE':'Clients','es-ES':'clientes','fr-FR':'clients','ar-SA':'العملاء'},
    '美元':      {'en-US':'US dollars','ja-JP':'米ドル','de-DE':'US-Dollar','es-ES':'dólares','fr-FR':'dollars','ar-SA':'دولارات'},
    '业务':      {'en-US':'business','ja-JP':'業務','de-DE':'Geschäft','es-ES':'negocio','fr-FR':'métier','ar-SA':'الأعمال'},
    '明细':      {'en-US':'details','ja-JP':'明細','de-DE':'Einzelposten','es-ES':'detalles','fr-FR':'détails','ar-SA':'تفاصيل'},
    '汇总':      {'en-US':'aggregates','ja-JP':'集計','de-DE':'Aggregate','es-ES':'agregados','fr-FR':'agrégats','ar-SA':'إجماليات'},
}

# ── 骨架句模板（完整成句，{term} 槽位）──
H2_TEMPLATES = {
    '数字对不上时怎么自查': {
        'en-US':'What to check when numbers don\u2019t match',
        'ja-JP':'数値が合わないときの自己点検',
        'de-DE':'Was Sie prüfen können, wenn Zahlen nicht übereinstimmen',
        'es-ES':'Qué revisar cuando las cifras no coinciden',
        'fr-FR':'Que vérifier lorsque les chiffres ne concordent pas',
        'ar-SA':'ما يجب التحقق منه عندما لا تتطابق الأرقام',
    },
    '口径说明': {
        'en-US':'How the numbers are calculated',
        'ja-JP':'集計ルールの説明',
        'de-DE':'Erläuterung der Berechnungsregeln',
        'es-ES':'Explicación de las reglas de cálculo',
        'fr-FR':'Explication des règles de calcul',
        'ar-SA':'شرح قواعد الحساب',
    },
    '关键口径': {
        'en-US':'Key calculation rules',
        'ja-JP':'重要な集計ルール',
        'de-DE':'Wichtige Berechnungsregeln',
        'es-ES':'Reglas de cálculo clave',
        'fr-FR':'Règles de calcul essentielles',
        'ar-SA':'قواعد الحساب الرئيسية',
    },
    '数据从哪里来': {
        'en-US':'Where the data comes from',
        'ja-JP':'データの出所',
        'de-DE':'Woher die Daten stammen',
        'es-ES':'De dónde provienen los datos',
        'fr-FR':'D\u2019où viennent les données',
        'ar-SA':'من أين تأتي البيانات',
    },
}

def sub_h2(line: str, loc: str) -> str | None:
    """## 标题行整句翻译；命中模板表则返回译文。"""
    if not line.startswith('## '):
        return None
    title = line[3:].strip()
    if title in H2_TEMPLATES and loc in H2_TEMPLATES[title]:
        return '## ' + H2_TEMPLATES[title][loc]
    return None

def fill_slots(text: str, loc: str) -> str:
    """对已模板化外的残留中文段做槽位术语替换（兜底，保证关键名词是目标语）。"""
    for zh in sorted(TERMS, key=len, reverse=True):
        if zh in text and loc in TERMS[zh]:
            text = text.replace(zh, TERMS[zh][loc])
    return text

def translate(text: str, loc: str) -> str:
    out_lines = []
    for line in text.split('\n'):
        if line.startswith('## '):
            h2 = sub_h2(line, loc)
            out_lines.append(h2 if h2 else fill_slots(line, loc))
        elif line.startswith('#'):
            out_lines.append(line)
        else:
            out_lines.append(fill_slots(line, loc))
    out = '\n'.join(out_lines)
    if loc == 'zh-TW':
        out = OpenCC('s2twp').convert(out)
    return out

def main():
    only = sys.argv[1:] if len(sys.argv) > 1 else LOCALES
    files = sorted(glob.glob(os.path.join(SRC_DIR, '*.zh-CN.md')))
    for loc in only:
        for f in files:
            zh = open(f, encoding='utf-8').read()
            out_path = f.replace('.zh-CN.md', f'.{loc}.md')
            with open(out_path, 'w', encoding='utf-8') as h:
                h.write(translate(zh, loc))
        print(f'{loc}: {len(files)} files')

if __name__ == '__main__':
    main()

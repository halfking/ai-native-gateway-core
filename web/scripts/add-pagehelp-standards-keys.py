#!/usr/bin/env python3
"""2026-11-23 统计标准轮：给 8 个语种的 pageHelp.ts 补两处键。

· title.dashboardStatsStandards —— 新正文（看板与会话统计的统计标准）的抽屉标题
· button.standards —— 页面内嵌帮助按钮的文案（图标 + 文字形态）

插入位置固定：title 键跟在 dashboardSimple 之后；button 块放在 title 之前。
脚本幂等：已存在则跳过（不重复插入）。
"""
import os
import re

ROOT = os.path.join(os.path.dirname(os.path.abspath(__file__)), '..', 'src', 'locales')

TITLES = {
    'zh-CN': '看板与会话统计的统计标准',
    'zh-TW': '看板與會話統計的統計標準',
    'en-US': 'Calculation standards for the board and session statistics',
    'ja-JP': 'ボードとセッション統計の集計ルール',
    'de-DE': 'Berechnungsregeln für Board und Sitzungsstatistik',
    'es-ES': 'Reglas de cálculo del panel y de las estadísticas de sesión',
    'fr-FR': 'Règles de calcul du tableau de bord et des statistiques de session',
    'ar-SA': 'قواعد حساب لوحة المعلومات وإحصائيات الجلسات',
}

BUTTONS = {
    'zh-CN': '统计标准',
    'zh-TW': '統計標準',
    'en-US': 'Calculation rules',
    'ja-JP': '集計ルール',
    'de-DE': 'Berechnungsregeln',
    'es-ES': 'Reglas de cálculo',
    'fr-FR': 'Règles de calcul',
    'ar-SA': 'قواعد الحساب',
}


def quote(value: str) -> str:
    """单引号字符串；内部单引号按 JS 惯例转义。"""
    return "'" + value.replace('\\', '\\\\').replace("'", "\\'") + "'"


def main() -> None:
    for locale, title in TITLES.items():
        path = os.path.join(ROOT, locale, 'pageHelp.ts')
        with open(path, encoding='utf-8') as handle:
            source = handle.read()

        changed = False

        if 'dashboardStatsStandards:' not in source:
            pattern = re.compile(r"^(    dashboardSimple:.*)$", re.MULTILINE)
            if not pattern.search(source):
                raise SystemExit(f'{locale}: 未找到 dashboardSimple 行')
            source = pattern.sub(
                lambda m: m.group(1) + "\n    dashboardStatsStandards: " + quote(title) + ",",
                source,
                count=1,
            )
            changed = True

        if 'button:' not in source:
            pattern = re.compile(r"^  title: \{$", re.MULTILINE)
            if not pattern.search(source):
                raise SystemExit(f'{locale}: 未找到 title 块')
            source = pattern.sub(
                "  button: {\n    standards: " + quote(BUTTONS[locale]) + ",\n  },\n  title: {",
                source,
                count=1,
            )
            changed = True

        if changed:
            with open(path, 'w', encoding='utf-8') as handle:
                handle.write(source)
            print(f'{locale}: updated')
        else:
            print(f'{locale}: already up to date')


if __name__ == '__main__':
    main()

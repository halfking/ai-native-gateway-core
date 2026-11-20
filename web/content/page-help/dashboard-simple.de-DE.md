# Datenübersicht der Seite im Einfachmodus

Diese Seite ist die auf einen Blick verständliche Version der Workbench: Gesamtstatus + Verbrauch + Schnellaktionen – entwickelt für Nutzer, die nicht ständig das Dashboard beobachten.

## Die Seite ist in drei Bereiche gegliedert

- **Statistikkarten**: Kernzahlen wie die heutige Anfrageanzahl, Erfolgsquote und Kosten.
- **Systemstatus**: Ob die einzelnen zentralen Komponenten ordnungsgemäß funktionieren, direkt anhand von Farben und Text angegeben.
- **Trend der letzten 7 Tage**: Tägliches Balkendiagramm der Anfrageanzahl.

## Erläuterung der Datenbasis

- Für die Statistikkarten gilt die **heutige Datenbasis** (heute von Mitternacht bis jetzt, mit Tageswechsel in Weltstandardzeit UTC).
- Das Trenddiagramm und die Expertenansicht verwenden **dieselbe Datenquelle**: Die Zahlen in den beiden Modi widersprechen sich nicht.
- Das Trenddiagramm zeigt nur **Tage mit Daten** an: Am 1. Tag nach einer neuen Bereitstellung ist nur ein Balken vorhanden; es werden keine unbelegten Nullwerte angezeigt.

## So prüfen Sie abweichende Zahlen selbst

1. „Heutige Anfragen“ basiert auf dem Tageswechsel um Mitternacht UTC: Vor 8 Uhr morgens Pekinger Zeit zeigt das Diagramm für „heute“ weiterhin den letzten Eintrag von gestern.
2. Der Nenner der Erfolgsquote sind alle Anfragen (einschließlich blockierter Anfragen): Wenn die Zahl nicht mit „erfolgreiche Anfragen ÷ von Ihnen erwartete Gesamtzahl“ übereinstimmt, prüfen Sie zunächst, ob es blockierte Anfragen gibt.
3. Für die Kosten wird nur eine Datenbasis angezeigt: Interne Kosten (Dollar) und externe Guthabenpunkte werden nicht zusammengeführt, sondern getrennt geprüft.
4. Wenn ein Systemstatus als „Normal“ angezeigt wird, sich eine bestimmte Seite aber nicht öffnen lässt: Der Status prüft lediglich, ob die Komponente aktiv ist, nicht die Verfügbarkeit jeder einzelnen Funktion. Konkrete Probleme finden Sie in den Hinweisen zur jeweiligen Seite.

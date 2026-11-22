# Datenhinweise zur Übersichtsseite

Diese Seite ist das „Dashboard“ des gesamten Gateways und fasst Nutzung, Kosten und Status für einen bestimmten Zeitraum auf einem Bildschirm zusammen.

## Woher die Daten stammen

Alle Zahlen auf der Seite stammen aus den Protokollen der einzelnen Modellaufrufe, die das Gateway aufgezeichnet hat. Das System erfasst zunächst jede Anfrage und aggregiert sie anschließend gemäß dem ausgewählten Zeitraum zu verschiedenen Statistiken.

## Berechnung des Zeitraums

- Die drei Schnellbereiche „Heute“, „Letzte 7 Tage“ und „Letzte 30 Tage“ werden nach Kalendertagen unterteilt. Das statistische Zeitfenster beginnt um null Uhr in der **Systemzeitzone (Standard Asia/Shanghai)** und endet im aktuellen Moment (nicht am Tagesende).
- Bei einem benutzerdefinierten Zeitraum werden die Daten der beiden ausgewählten Tage aggregiert (von null Uhr des ersten Tages bis null Uhr des letzten Tages, einschließlich des gesamten letzten Tages).
- Die Seite wird alle 10 Sekunden automatisch aktualisiert. Ist der Echtzeitkanal online, werden neu abgeschlossene Anfragen sofort addiert; ein Warten auf die Aktualisierung ist nicht erforderlich.

## Die vier wichtigsten Karten

| Karte | Bedeutung | Berechnung |
| --- | --- | --- |
| Gesamtanfragen | Wie viele Modellaufrufe in diesem Zeitraum verarbeitet wurden | Direkte Anzahl der Protokolleinträge |
| Erfolgsquote | Welcher Anteil der Anfragen normal beantwortet wurde | Anzahl der Einträge mit Status „Erfolg“ ÷ Gesamtzahl der Einträge |
| Gesamte Token-Zahl | Wie viele Token in diesem Zeitraum verbraucht wurden (Eingabe + Ausgabe) | Summe der Eingabe- und Ausgabe-Token jedes Eintrags |
| Gesamtkosten | Die in US-Dollar berechneten Kosten in diesem Zeitraum | Summe der Kosten jedes Eintrags (in US-Dollar) |
| Gesamtpunkte | Die Summe der Punkte gemäß der internen Abrechnung der Plattform | ★ Zeigt derzeit die **Live-Differenz nach dem Cache-Stand** und ändert sich daher nicht mit dem Zeitfenster (siehe unten) |

Hinweis: **Kosten (USD) und Punkte verwenden zwei getrennte Berechnungsgrundlagen und werden nicht addiert**. Die Kosten entsprechen den US-Dollar-Kosten gegenüber dem vorgelagerten Anbieter. Punkte sind die Einheit, mit der die Plattform ihre Mandanten abrechnet. Beide Werte sind nicht direkt vergleichbar und werden daher getrennt angezeigt.

Die Punktekarte folgt einer besonderen Regel: Die Punktespalte der Anfrage-Faktentabelle ist derzeit immer 0, angezeigt wird die „Live-Differenz nach dem Baseline-Stand“ — der Wechsel zwischen „Heute / Letzte 7 Tage / Letzte 30 Tage“ lässt die Karte unverändert. Der Nutzungstrend derselben Seite führt eine eigene Punktsumme (aus einer anderen Quelle), die nicht der Zahl der Karte entspricht. Für ein belastbares Punktekonto nutzen Sie die Seiten „Nutzung & Kosten“ und „Abgleich“.

## Fehleranzahl und Fehlerursachen

- Die „Fehleranzahl“ wird bevorzugt anhand des Kreisdiagramms der Fehlerursachen kategorienweise addiert. Ist kein Kreisdiagramm vorhanden, wird sie als „Gesamtanfragen × (1 − Erfolgsquote)“ geschätzt.
- Durch Klick auf die Karte „Fehleranzahl“ gelangen Sie zur Fehlerverteilung und können dort den Anteil jeder Fehlerart einsehen.

## Definition der „Aktiv“-Labels

- „Aktive Modelle“ = die Anzahl der Modelle, die in diesem Zeitraum **tatsächlichen Datenverkehr** hatten, nicht die Gesamtzahl der auf der Plattform verfügbaren Modelle.
- „Aktive Schlüssel“ = die Anzahl der Schlüssel mit aktuellem **Aktivierungsstatus** (Konfigurationswert, unabhängig vom Zeitraum).
- „Anbieteranzahl“ = die Anzahl der Anbieter, die in diesem Zeitraum **tatsächlichen Datenverkehr** hatten.

## Selbstprüfung bei abweichenden Zahlen

1. Prüfen Sie zuerst, ob der Zeitraum übereinstimmt: Bei unterschiedlichen Zeitfenstern auf zwei Seiten müssen die Zahlen voneinander abweichen.
2. Achten Sie auf die Markierung „Herabgestuft“ auf den Karten: Wenn aggregierte Daten vorübergehend nicht berechnet werden können, ist dies deutlich gekennzeichnet. Eine 0 bedeutet dann nicht, dass kein Datenverkehr vorhanden war.
3. Wenn auf der Punktekarte „Nicht vertrauenswürdig/Nicht berechnet“ angezeigt wird, fehlt vorübergehend die Datenquelle für die Punkteabrechnung. Die Anfragezahl und die Kosten sind dennoch korrekt.
4. Prüfdatenverkehr wird nicht in den geschäftlichen Statistiken berücksichtigt: Die internen Health-Check-Anfragen des Systems werden weder bei den Gesamtanfragen noch bei den Kosten einbezogen.
5. Echtzeitaktualisierung und Diagramme können um wenige Sekunden voneinander abweichen: Die Kartenzahlen einschließlich der in Echtzeit addierten Anfragen können daher etwas höher als die Diagramme ausfallen. Dies ist normal.

## Vergleich mit dem Tab „Sitzungsstatistik“

Diese Seite nutzt die **Anfrage-Ebene** (ein Modellaufruf zählt als eine Zeile), der Tab „Sitzungsstatistik“ die **Sitzungs-Ebene** (eine Sitzung zählt als eine Zeile mit den kumulierten Werten); Zeitfenster und Probe-Ausschluss unterscheiden sich ebenfalls ⇒ die Zahlen sollen nicht übereinstimmen. Für den Abgleich Position für Position nutzen Sie die Schaltfläche **Berechnungsregeln** in den Tabs „Board“ und „Sitzungsstatistik“ (dort stehen beide Berechnungsgrundlagen und die Unterschiede nebeneinander).

# Datenbeschreibung der Preisseite

Diese Seite verwaltet die „Kostenpreistabelle“ – sie zeigt, was ein einzelner Modellaufruf beim vorgelagerten Anbieter kostet, und bildet die Grundlage für die Kostenkalkulation und die interne Preisberechnung.

## Ansichten

- **Baumansicht**: Hier können Sie die Hierarchie „Anbieter → Modell“ durchsuchen, was sich gut für die einzelne Prüfung eignet.
- **Tabellenansicht**: Hier werden alle Preise in einer Tabelle dargestellt, was sich gut für die Massenprüfung und den Export eignet.

## Preisbasis

- Preise werden pro eine Million Token angegeben: Der Eingangspreis und der Ausgangspreis sind getrennt; der Ausgangspreis ist in der Regel höher.
- Bei Cache-Treffern wird der Eingangspreis für den zwischengespeicherten Anteil zum reduzierten Preis berechnet (bei einigen Anbietern 10%); Modelle mit Cache-Preis werden mit einer eigenen Zeile für den Cache-Preis dargestellt.
- Preisänderungen wirken sich nur auf Aufrufe **nach ihrem Inkrafttreten** aus; historische Rechnungen werden mit dem damals gültigen Preis berechnet und nicht rückwirkend neu kalkuliert.

## So prüfen Sie Abweichungen bei Zahlen

1. Kosten und Anbieterrechnung unterscheiden sich um eine Nachkommastelle: Prüfen Sie die Preiseinheit (pro eine Million vs. pro tausend Token); die Werte unterscheiden sich um den Faktor 1000.
2. Für dasselbe Modell sind zwei Preiszeilen vorhanden: Eine enthält den Standardpreis, die andere den Cache-Preis, oder die Zeilen gehören zu unterschiedlichen Anbieter-Integrationen – prüfen Sie zuerst die Spalte „Anbieter“.
3. Der Tagesbericht hat sich nach einer Preisänderung nicht verändert: Berichte werden täglich eingefroren; erst der Snapshot des Folgetags zeigt den neuen Preis.
4. Abweichungen beim Wechselkurs: Wenn Kosten in US-Dollar in RMB angezeigt werden, wird der in der Plattform konfigurierte Wechselkurs verwendet. Abweichungen vom aktuellen Kurs der Bank sind normal.

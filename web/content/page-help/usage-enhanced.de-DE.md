# Datenbeschreibung der erweiterten Nutzungsanalyseseite

Diese Seite stellt Ergebnisse aus fünf APIs nebeneinander dar: Übersicht, Kostentrend, Monatsvergleich, Cache-Ökonomie und meistgenutzte API-Schlüssel. Es handelt sich nicht um wiederholte Messungen aus derselben Tabelle oder demselben Zeitfenster.

## Kernkarten

- **Gesamtkosten (USD)**: Summe der im Zeitfenster an den Upstream-Anbieter gezahlten Kosten.
- **Gesamtanfragen**: Anzahl der Anfragen im selben Zeitfenster.
- **Veränderung zur Vorperiode**: Prozentualer Vergleich des aktuellen Zeitfensters mit dem vorherigen Zeitfenster gleicher Länge.
- Detailaufstellung: Listet den jeweiligen Beitrag nach Dimensionen wie Modell, Schlüssel oder Tag auf.

## Berechnungsgrundlage

- Die Seite kombiniert fünf unabhängige Endpunkte. Übersicht und meistgenutzte API-Schlüssel verwenden ein Tagesfenster; Kostentrend und Cache-Ökonomie verwenden den ausgewählten Datumsbereich; die Vergleichskarte vergleicht den aktuellen mit dem vorherigen Kalendermonat. Datenquellen und Zeitfenster unterscheiden sich, daher müssen die Werte weder mit dem Dashboard noch untereinander übereinstimmen.
- Der Vergleichsendpunkt summiert die Upstream-Kosten in USD je Kalendermonat und berechnet (Kosten des aktuellen Monats − Kosten des Vormonats) ÷ Kosten des Vormonats. Er prüft nicht, ob beide Monate vollständig sind. Betragen die Kosten des Vormonats null, ist die Veränderungsrate null; das belegt weder vollständige Monatsdaten noch eine unveränderte Nutzung.

## So prüfen Sie Abweichungen bei den Zahlen

1. Beim Vergleich der Periodenkarte mit Kennzahlen für den ausgewählten Datumsbereich beachten Sie: Die Karte vergleicht immer den aktuellen mit dem vorherigen Kalendermonat und folgt nicht dem ausgewählten Start- und Enddatum.
2. Der Kostentrend gruppiert nach Dimension. Fehlende Dimensionswerte werden als „unknown“ gruppiert; Gruppen mit geringem Anteil können als „Other“ zusammengefasst werden. Bei abweichenden Quellen oder Zeitfenstern können Summen voneinander abweichen.
3. Übersicht und meistgenutzte API-Schlüssel verwenden ein Tagesfenster, Kostentrend und Cache-Ökonomie dagegen den ausgewählten Datumsbereich. Fenstergrenzen, Aktualisierungszeitpunkt und der noch laufende aktuelle Tag können Abweichungen verursachen. Bei einem Hinweis auf degradierte Daten sind Null- oder Leerwerte kein gemessener Nullverbrauch.

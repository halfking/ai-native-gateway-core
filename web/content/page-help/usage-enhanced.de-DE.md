# Datenbeschreibung der erweiterten Nutzungsanalyseseite

Diese Seite fasst die über fünf APIs verteilten Nutzungsdaten in einer Gesamtansicht zusammen: Gesamtkosten, Gesamtzahl der Anfragen, Veränderung zur Vorperiode und Detailaufstellung auf einen Blick.

## Kernkarten

- **Gesamtkosten (USD)**: Summe der im Zeitfenster an den Upstream-Anbieter gezahlten Kosten.
- **Gesamtanfragen**: Anzahl der Anfragen im selben Zeitfenster.
- **Veränderung zur Vorperiode**: Prozentualer Vergleich des aktuellen Zeitfensters mit dem vorherigen Zeitfenster gleicher Länge.
- Detailaufstellung: Listet den jeweiligen Beitrag nach Dimensionen wie Modell, Schlüssel oder Tag auf.

## Berechnungsgrundlage

- Die Daten auf dieser Seite stammen aus derselben Aggregations-API wie das Dashboard und sollten mit dem Dashboard übereinstimmen. Bei Abweichungen sollten zunächst die Zeitfenster abgeglichen werden.
- Der Vergleich zur Vorperiode wird nur angezeigt, wenn für beide Zeitfenster vollständige Daten vorliegen. Fehlt für einen Tag ein Wert, kennzeichnet die Seite dies mit „Daten unvollständig“. Der Prozentwert ist in diesem Fall nur ein Richtwert.

## So prüfen Sie Abweichungen bei den Zahlen

1. Der Vergleichsprozentwert zur Vorperiode wirkt unintuitiv (die Nutzung ist unverändert, es wird dennoch eine große Veränderung angezeigt): Unvollständige Daten im vorherigen Zeitfenster verzerren den Ausgangswert. Prüfen Sie zuerst, ob das vorherige Zeitfenster vollständig ist.
2. Die Summe der Detailaufstellung ≠ die Gesamtkarten: Anfragen mit fehlendem Dimensionswert werden nur in der Gesamtsumme berücksichtigt (die Seite weist die nicht zugeordnete Menge aus).
3. Das Zeitfenster umfasst heute: Die heutigen Zahlen wachsen noch in Echtzeit, weshalb sich der Vergleich zur Vorperiode laufend ändert. Nach Tagesabschluss ist er genauer.

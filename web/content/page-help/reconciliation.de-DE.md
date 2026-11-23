# Datenbeschreibung der Abstimmungsberichtseite

Diese Seite fasst die vom Gateway erfasste Nutzung des Vortags aus Anbieter-, Mandanten-, Personen-, Modell- und weiteren Perspektiven zusammen. Sie dient zum Abgleich der Gateway-Aufzeichnungen; sie ist keine Lieferantenrechnung und weist keinen bestätigten Zahlbetrag aus.

## Wie Snapshots erstellt werden

- Der tägliche Aggregationsjob erstellt in den frühen Morgenstunden einen Snapshot des **Vortages**; für die Daten des aktuellen Tages gibt es erst in den frühen Morgenstunden des Folgetags einen Snapshot. Die Seite zeigt den Hinweis „Snapshot für heute wird in den frühen Morgenstunden des Folgetags erstellt“.
- Wenn für einen Tag kein Snapshot vorliegt (z. B. weil das System zu diesem Zeitpunkt nicht verfügbar war), wird „Nicht aggregiert“ angezeigt; über das „Enddatum des Wiederholungslaufs“ kann die Aggregation manuell nachgeholt werden. Bereits erstellte Snapshots können ebenfalls neu berechnet werden und sind nicht endgültig festgeschrieben.

## Zwei Betragskennzahlen (nicht zusammengeführt)

- **Betrag aus Anbietersicht**: Summe der vom Gateway erfassten Nutzungen mit terminalem Status, einschließlich keinem Anbieter zugeordneter und nicht geschäftlicher Nutzung. Der Betrag ist eine vom Gateway erfasste Schätzung, kein vom Lieferanten zurückgemeldeter Betrag und weder bestätigte Verbindlichkeit noch bestätigte Zahlung.
- **Interne Credits / interner Betrag**: Nur geschäftlicher Datenverkehr; Credits werden anhand des im Snapshot gespeicherten Preisfaktors in einen Betrag umgerechnet. Dies ist die interne Abrechnungsgrundlage der Plattform und kein Nachweis für Rechnungsstellung oder Zahlungseingang.
- Ein Vergleich ist nur bei übereinstimmendem Datenverkehrsumfang und übereinstimmender Währung sinnvoll. Die Differenz ist nicht automatisch die Bruttomarge. Die Werte werden getrennt ausgewiesen und nicht über Einheiten hinweg summiert.

## Die vier Token-Bestandteile

Dieser Bericht addiert derzeit Eingabe, Ausgabe sowie Cache-Lese- und Schreibwerte. Cache-Werte können sich mit der Eingabe überschneiden. Für Berichtsvergleiche gilt die Summe aus normalisierter Eingabe und Ausgabe. Die Cache-Werte werden separat ausgewiesen.

## Selbstprüfung bei abweichenden Zahlen

1. Ein Snapshot ist eine Aggregation der zu diesem Zeitpunkt verfügbaren Daten. Abweichungen vom Echtzeit-Dashboard sind normal — das Dashboard kann Daten des aktuellen Tages enthalten, und ein Snapshot lässt sich durch erneute Aggregation aktualisieren.
2. An als „nicht aggregiert“ markierten Tagen gibt es keine Zahlen: Starten Sie einen Wiederholungslauf, um die Aggregation nachzuholen, und gleichen Sie anschließend erneut ab.
3. Beim Vergleich der Anbietersicht mit einer Lieferantenrechnung sind neben der Zeitzone auch die Abgrenzungen zu beachten: Diese Seite summiert vom Gateway erfasste Nutzungen mit terminalem Status, einschließlich nicht zugeordneter und nicht geschäftlicher Nutzung; der Betrag ist eine Schätzung und nicht der Rechnungsbetrag des Lieferanten. Lieferantenrechnungen können außerdem lokale statt UTC-Tagesgrenzen verwenden.
4. Fehlgeschlagene Anfragen mit erfasster terminaler Nutzung oder Kosten fließen in die Gateway-Aggregation ein. Das bedeutet nicht, dass der Lieferant eine Gebühr bestätigt hat.

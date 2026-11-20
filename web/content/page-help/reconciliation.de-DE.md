# Datenbeschreibung der Abstimmungsberichtseite

Diese Seite ist der „Ledger-Abgleicher“: Jeden Morgen in den frühen Stunden werden die Buchungen des Vortages auf einer Abstimmungsseite zusammengefasst. Der Abgleich ist aus mehreren Perspektiven möglich: Anbieter, Mandant, Mitarbeitende und Modell.

## Wie Snapshots erstellt werden

- Der tägliche Aggregationsjob erstellt in den frühen Morgenstunden einen Snapshot des **Vortages**; für die Daten des aktuellen Tages gibt es erst in den frühen Morgenstunden des Folgetags einen Snapshot. Die Seite zeigt den Hinweis „Snapshot für heute wird in den frühen Morgenstunden des Folgetags erstellt“.
- Wenn für einen Tag kein Snapshot vorliegt (z. B. weil das System zu diesem Zeitpunkt nicht verfügbar war), wird „Nicht aggregiert“ angezeigt; über das „Enddatum des Wiederholungslaufs“ kann die Aggregation manuell nachgeholt werden.

## Zwei Betragskennzahlen (nicht zusammengeführt)

- **Anbieterkosten (USD)**: Die Beträge, die die Plattform an den vorgelagerten Anbieter zahlt.
- **Interne Credits / Interner Betrag**: Die Grundlage, nach der die Plattform ihre Mandanten abrechnet.
- Die Differenz zwischen beiden Werten ist die Bruttomarge der Plattform. Die Werte werden getrennt ausgewiesen und nicht über Einheiten hinweg summiert.

## Die vier Token-Bestandteile

Eingabe (die an das Modell gesendete Textmenge), Ausgabe (die vom Modell erzeugte Textmenge), Cache-Lesen (die Eingabe des vorherigen Durchlaufs wird wiederverwendet und entweder nicht erneut berechnet oder mit Rabatt berechnet) und Cache-Schreiben (die erstmals im Cache gespeicherte Eingabe). Die Summe der vier Bestandteile = Gesamt-Token.

## Selbstprüfung bei abweichenden Zahlen

1. Ein Snapshot ist die „Endabrechnung“: Abweichungen von den Zahlen im Echtzeit-Dashboard sind normal — der Snapshot wird in den frühen Morgenstunden des Folgetags eingefroren und ändert sich danach nicht mehr; das Echtzeit-Dashboard enthält außerdem den aktuellen Tag.
2. An als „nicht aggregiert“ markierten Tagen gibt es keine Zahlen: Starten Sie einen Wiederholungslauf, um die Aggregation nachzuholen, und gleichen Sie anschließend erneut ab.
3. Beim Abgleich der Anbieterperspektive mit den Rechnungen im Anbieter-Backend auf die Zeitzone achten: Dieses System trennt die Tage nach UTC, Anbieterrechnungen werden möglicherweise nach lokaler Zeitzone erstellt. Datenverkehr über den Tageswechsel hinaus wird daher unterschiedlichen Tagen zugeordnet.
4. Auch fehlgeschlagene Anfragen verursachen geringe Kosten (z. B. für eine teilweise generierte Antwort, die dem vorgelagerten Anbieter bereits in Rechnung gestellt wurde). An Tagen mit hoher Fehlerquote ist das Verhältnis von Kosten zu Anfragen höher.

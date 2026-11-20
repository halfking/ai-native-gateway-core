# Detailseite der Anfrage-Journey

Sehen Sie sich die „Journey“ einer Anfrage über mehrere Versuche an: Eingang, Ausgang und Ergebnis jedes Kandidatenversuchs werden zu einer vollständigen Kette verbunden.

## Berechnungsgrundlage

Journey = Hauptanfrage + alle Wiederholungsversuche; Gesamtdauer = Zeitdifferenz zwischen dem ersten und letzten Versuch, einschließlich aller Versuche.

## Selbstprüfung bei Zahlenabweichungen

- 1. Journey-Anzahl ≠ Anzahl der Kandidaten: Enthält interne Wiederholungen und Fallbacks; die Abrechnung richtet sich nach dem letztendlich erfolgreichen Versuch.
- Abgebrochene Versuche: Versuche mit einem Upstream-Timeout werden als „Unterbrechung“ gekennzeichnet; die Teilkosten werden nach der tatsächlichen Abrechnungsmethodik des Upstreams festgelegt.

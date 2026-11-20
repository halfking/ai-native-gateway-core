# Speicher-Migrationsstatusseite

Fortschritts-Dashboard für die Migration von Daten zwischen Speicherebenen.

## Definitionen und Berechnungsgrundlage

Migration = Replikation in Batches + Validierung + Umstellung; der Fortschritt wird nach Batch gezählt.

## Selbstprüfung bei Abweichungen in den Zahlen

- 1. Der Fortschritt hängt fest: Die Migration einer großen Tabelle in einem Batch dauert länger. Prüfen Sie die Batch-Details statt des Gesamtprozents.
- Validierung fehlgeschlagen: Ein fehlgeschlagenes Batch kann erneut ausgeführt werden, ohne dass bereits abgeschlossene Batches beeinträchtigt werden.

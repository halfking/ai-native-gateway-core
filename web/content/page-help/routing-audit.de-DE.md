# Auditseite für Routing-Rewrites

Auditprotokoll darüber, wer wann die Routing-Konfiguration geändert hat.

## Hinweise zur Datenauslegung

Jeder Eintrag enthält den Wert vor und nach der Änderung sowie die ausführende Person; Einträge werden nur ergänzt und nicht geändert.

## Selbstprüfung bei abweichenden Zahlen

- 1.Eine bestimmte Änderung fehlt: Wenn die Datenbank direkt und nicht über die Seite geändert wurde, wird kein Auditprotokoll erstellt; über die Seite vorgenommene Änderungen werden immer protokolliert.
- Nach der Änderung hat sich das Verhalten nicht geändert: Bestätigen Sie den Traffic-Bereich, den die Regel tatsächlich erfasst.

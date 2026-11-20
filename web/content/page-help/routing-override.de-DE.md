# Seite für Routing-Überschreibungen

Routing manuell überschreiben: Leitet den Datenverkehr eines bestimmten Modells bzw. Schlüssels an ein vorgegebenes Ziel.

## Hinweise zur Terminologie

Die Überschreibung hat Vorrang vor der automatischen Routenauswahl und wird sofort wirksam. Sie kann jederzeit zurückgenommen werden.

## Selbstprüfung, wenn die Zahlen nicht übereinstimmen

- 1.Die Überschreibung greift nicht: Prüfen Sie den Abdeckungsbereich der Überschreibungsbedingungen (Dimension Modell/Schlüssel/Mandant); zu eng gefasste Bedingungen erfassen keinen Datenverkehr.
- Nach der Rücknahme wird weiterhin das alte Ziel verwendet: Die Routing-Entscheidung wurde für einige Sekunden zwischengespeichert; nach einiger Zeit wird wieder die reguläre Route verwendet.

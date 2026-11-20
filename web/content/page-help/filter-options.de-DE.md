# Filteroptionen-Seite

Pflegt die verfügbaren Optionen in den Filtern der einzelnen Listenseiten (Inhalte der Dropdown-Felder für Modelle, Schlüssel, Mandanten usw.).

## Erläuterung der Methodik

Optionsquellen = tatsächlich aufgetretene Werte + manuell gepflegte Positivliste; beide werden zusammengeführt und dedupliziert.

## Selbstprüfung bei abweichenden Zahlen

- 1.Auf der Listenseite lässt sich ein certain Wert nicht filtern: Prüfen Sie zuerst hier, ob die Option erfasst ist; dynamische Werte erscheinen automatisch, sobald entsprechende Daten vorliegen.
- Eine gelöschte Option erscheint erneut: Der Wert wird durch tatsächlichen Datenverkehr erzeugt. Das Löschen wirkt sich nur auf den manuell gepflegten Teil aus.

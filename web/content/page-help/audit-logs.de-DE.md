# Datenerläuterungen zur Audit-Log-Seite

Diese Seite hält fest, „wer wann was geändert hat“ – ein chronologisches Verzeichnis der Verwaltungsvorgänge zur späteren Nachverfolgung.

## Angaben zu jedem Eintrag

Bediener, Zeitpunkt, Aktionstyp (z. B. Konfiguration ändern, Schlüssel deaktivieren, Parameter anpassen), Zielobjekt sowie Werte vor und nach der Änderung.

## Erfassungsumfang

- Es werden ausschließlich **Administrationsoperationen** erfasst (Schreiboperationen über die Backend-Verwaltungsoberfläche und Wartungsschnittstellen); Modellaufrufe normaler Benutzer sind hier nicht enthalten. Diese finden Sie auf der Anfrageprotokollseite.
- Leseoperationen (nur ansehen, nichts ändern) werden standardmäßig nicht erfasst.

## Abfragetipps

- Die Kombination aus „Bediener + Zeitraum“ ist am effektivsten: Ermitteln Sie zuerst die Person und grenzen Sie anschließend den Zeitraum ein.
- Die „Werte vor und nach der Änderung“ sind die maßgebliche Grundlage für die Prüfung, „wann wurde die Konfiguration von wem geändert“.

## Selbstprüfung bei abweichenden Zahlen

1. Die Konfiguration wurde geändert, es ist jedoch kein Logeintrag vorhanden: Vergewissern Sie sich, dass die Änderung über die Verwaltungsoberfläche beziehungsweise die Administrationsschnittstelle erfolgte (also über einen Kanal mit Audit-Protokollierung). Direkte Datenbankänderungen ohne Durchgang durch das Gateway hinterlassen keinen Eintrag.
2. Die Zeitangaben weichen um mehrere Stunden ab: Die Logzeiten werden in Koordinierter Weltzeit (UTC) angegeben. Lokale Zeiten müssen selbst umgerechnet werden (Peking-Zeit entspricht UTC+8).
3. Für dieselbe Aktion liegen zwei Einträge vor: Teiloperationen werden jeweils separat erfasst (z. B. kann „Konfiguration aktualisieren“ in „Alten Wert lesen“ und „Neuen Wert schreiben“ aufgeteilt werden). Maßgeblich ist der vollständigste Eintrag.

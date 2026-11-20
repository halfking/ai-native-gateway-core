# Datenhinweise zur Betriebsseite für AUTO-Routing

Diese Seite bildet den geschlossenen Workflow der „automatischen Parameteranpassung“ ab: Annotation → Statistik → Profilempfehlung → Freigabe von Parameteranpassungen. Das System lernt dadurch immer besser, das passende Modell auszuwählen, aber **jede Anpassung, die wirksam werden soll, muss den Freigabeprozess durchlaufen und manuell genehmigt werden**.

## Die vier Tabs bilden eine Prozesskette

- **Arbeitsbereich für Annotationen**: Historische Anfragen werden hier manuell als „gut“ oder „schlecht“ annotiert. Diese Annotationen sind die Grundlage für alle nachfolgenden Statistiken.
- **Annotationsstatistik**: Eine Zusammenfassung des Annotationsvolumens (heutige ausstehende Annotationen, Annotationen insgesamt, Anzahl der Beteiligten).
- **Aufgabenprofil**: Zeigt die Leistung der Modelle nach Aufgabentyp und erstellt Profilempfehlungen wie „Für diese Art von Aufgabe eignet sich dieses Modell besser“.
- **Freigabe von Parameteranpassungen**: Hier warten die vom System vorgeschlagenen Anpassungen der Parameter auf manuelle Genehmigung oder Ablehnung.

## Berechnungsgrundlagen

- „Heutige ausstehende Annotationen“ wird nach Kalendertag (UTC) berechnet; was gestern nicht abgeschlossen wurde, wird in die offene Liste unter „Annotationen insgesamt“ übernommen.
- Für Vorschläge zur Parameteranpassung gibt es eine Konfidenzschwelle: Das System leitet nur Empfehlungen mit ausreichender Konfidenz zur Freigabe weiter. Vorschläge mit niedriger Konfidenz werden automatisch zurückgestellt, sodass die Freigebenden nicht unnötig belastet werden.
- Vor jeder wirksam werdenden Parameteranpassung wird ein „Snapshot der alten Werte“ erstellt: Falls nach der Genehmigung auffällt, dass die Wirkung nicht wie erwartet ausfällt, kann die Anpassung mit einem Klick auf den Zustand davor zurückgesetzt werden.

## So prüfen Sie Abweichungen bei den Zahlen selbst

1. Die Anzahl der Annotationen stimmt nicht mit der Anzahl der Anfragen überein; das ist normal: Nicht jede Anfrage muss annotiert werden, und das System wählt nur eine repräsentative Stichprobe aus.
2. Ein abgelehnter Vorschlag wird nicht erneut als dieselbe Empfehlung vorgeschlagen: Gleichartige Empfehlungen berücksichtigen frühere Freigabeentscheidungen.
3. Die Kennzahlen ändern sich nicht, obwohl die Parameteranpassung wirksam geworden ist: Die Parameter werden mit einer Verzögerung aktiviert (Hot-Reload-Intervall), und ihre Wirkung zeigt sich erst im neuen Datenverkehr.
4. Ein Deep Link führt zum falschen Tab: Die vier alten Seiten wurden zu einer Seite mit vier Tabs zusammengeführt; alte Links leiten automatisch weiter.

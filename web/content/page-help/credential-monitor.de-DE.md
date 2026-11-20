# Datenhinweise zur Überwachungsseite für Zugangsdaten

Diese Seite überwacht die Verfügbarkeit der vorgeschalteten „Zugangsdaten“ (Kontoschlüssel zur Anbindung der einzelnen Modellanbieter) und hilft dabei, Probleme zu erkennen, bevor Benutzer eine Störung melden.

## Registerkarten

- **Liste**: Gesamtstatus der Zugangsdaten mit einer Zeile pro Zugangsdatensatz (aktueller Status, letzter Fehler, Deaktivierungsgrund).
- **Heatmap**: Zeitliche Darstellung der Fehler für jedes Zugangsdatum; je dunkler die Farbe, desto mehr Fehler.
- **Routing-Protokolle**: Ereignisprotokoll auf Ebene der Zugangsdaten (Aktivierung/Deaktivierung/Wechsel).
- **Fehlertrend**: Stündliche Fehlerkurve zur Unterscheidung zwischen kurzzeitigen Spitzen und anhaltenden Fehlern.
- **Prüfstatus** (nur für Superadmins): Ergebnisse der vom System aktiv vorgenommenen Testaufrufe für jedes Zugangsdatum.

## Bedeutung der Statusetiketten

- „Normal“ = In den jüngsten Einträgen wurden keine Fehler erfasst.
- „Manuell deaktiviert“ = Von einem Administrator manuell außer Betrieb genommen; automatische Prüfungen aktivieren es nicht wieder.
- „Automatisch gesteuert“ = Vom System aufgrund aufeinanderfolgender Fehler automatisch außer Betrieb genommen (beispielsweise bei mehrfach bestätigten Störungen); die Zugangsdaten können manuell außer Betrieb genommen werden oder sich automatisch wiederherstellen.
- Das Abzeichen für den Deaktivierungsgrund hat Vorrang vor dem angezeigten Zustand: Wenn für dieselben Zugangsdaten sowohl ein Fehler als auch ein Deaktivierungsgrund vorliegt, zeigt die Seite den Deaktivierungsgrund an.

## Health-Score und Fehleranzahl

- Die Fehleranzahl erfasst **abgeschlossene fehlgeschlagene Aufrufe** und summiert sie über den ausgewählten Zeitraum.
- Ein Fehlschlag derselben Anfrage wird nur den zum Zeitpunkt verwendeten Zugangsdaten zugerechnet; ein erfolgreicher Wiederholungsversuch entfernt den bereits erfassten Fehler nicht.

## Selbstprüfung bei abweichenden Zahlen

1. Abweichende Zeitfenster: Der Filterzeitraum dieser Seite und der Zeitraum im Dashboard müssen vor dem Vergleich angeglichen werden.
2. Der „Prüfungs“-Traffic umfasst Kontrollaufrufe, die das System selbst startet, und wird getrennt vom Benutzer-Traffic ausgewertet; die Verfügbarkeit der Zugangsdaten wird anhand von Testaufrufen und echtem Traffic gemeinsam beurteilt.
3. Bei gerade wiederhergestellten Zugangsdaten kann die Fehleranzahl kurzzeitig noch einen alten Wert anzeigen: Die Diagramme werden stündlich aggregiert, und die vollständigen Ergebnisse der aktuellen Stunde liegen erst in der nächsten Stunde vor.
4. Bei einer leeren Liste zunächst die Filter prüfen: Die Kombination aus Typ der Zugangsdaten, Mandant und Status kann sämtliche Ergebnisse herausfiltern.

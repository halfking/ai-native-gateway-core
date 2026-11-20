# Datenhinweise zur Anbieterlistenseite

Diese Seite verwaltet angebundene Upstream-„Anbieter“ (OpenAI, Claude usw.) sowie die Zugangsdaten und Modelle jedes Anbieters.

## Unterschied zwischen den beiden Statuswerten

- **Systemgesundheit**: Verfügbarkeit auf Ebene der Zugangsdaten (normal/warnung/nicht erreichbar/unbekannt), basierend auf Fehleraufzeichnungen aus Prüfungen und echtem Datenverkehr.
- **Routing-Fähigkeit**: Ob der Anbieter derzeit **am Routing teilnimmt** (verfügbar/nicht verfügbar/keine Modelle/manuell deaktiviert). Ein Anbieter kann gesund, aber manuell deaktiviert sein – dann empfängt er keinen Datenverkehr, während seine Gesundheitsdaten weiterhin aktualisiert werden.

## Beziehung zwischen Modellen und Zugangsdaten

- Ein Anbieter kann mit mehreren Zugangsdaten verknüpft sein und mehrere Modelle bereitstellen; Probleme mit einer der Zugangsdaten wirken sich nur auf die darüber verfügbaren Modelle aus.
- „Keine Modelle“ = Der Anbieter ist angebunden, hat aber noch keine verfügbaren Modelle konfiguriert und empfängt daher natürlich keinen Datenverkehr.

## Sortierung und Filterung

- Standardmäßig wird nach Gesundheitszustand sortiert. Der Verfügbarkeitsfilter (routingfähig/nicht routingfähig) ist ein unabhängiger Schalter. Beide beeinflussen sich nicht gegenseitig und bestimmen nur, welche Zeilen angezeigt werden.

## Selbstprüfung bei abweichenden Zahlen

1. Gesund, aber kein Datenverkehr: Prüfen Sie zuerst die „Routing-Fähigkeit“ – manuell deaktivierte Anbieter nehmen nicht am Routing teil.
2. Die Modellanzahl stimmt nicht mit den tatsächlich verfügbaren Optionen überein: Die Modellliste wird nach Verfügbarkeit der Zugangsdaten gefiltert. Sind alle Zugangsdaten offline, verschwinden die Modelle vorübergehend aus den verfügbaren Optionen.
3. Kürzlich gespeicherte Zugangsdaten zeigen „Prüfung läuft“: Das System startet nach dem Speichern automatisch eine Gesundheitsprüfung. Bis zum Ergebnis wird ein Übergangsstatus angezeigt.
4. Nach dem Aktualisieren bleibt die Filterung gespeichert: Die Seite behält die zuletzt verwendeten Filterbedingungen bei. Wenn die Seite von einer anderen Person genutzt wird, setzen Sie die Filter zuerst zurück.

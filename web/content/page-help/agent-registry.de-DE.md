# Registrierungsseite für Agenten

Liste der in der Verwaltungskonsole registrierten automatisierten Agenten (Skripte/Bots) einschließlich Status und Zuordnung.

## Statusdefinitionen

Gesund = kürzlich Heartbeat und erfolgreicher Aufruf; Offline = länger als ein Heartbeat-Zeitraum keine Nachricht.

## Selbstprüfung bei Zahlenabweichungen

- 1. Die Anzahl der Agenten ist unabhängig von der Anzahl der Benutzer: Agenten sind Servicekonten und belegen keinen Benutzerplatz.
- Zwei Einträge für denselben Agenten: Wird die Instanz neu erstellt, erhält sie eine neue ID; die historische Nutzung wird nach ID zugeordnet.
- Status „Gesund“, aber der Aufruf schlägt fehl: „Gesund“ berücksichtigt nur Heartbeat und Anmeldestatus; konkrete Fehler sind in den Anfrageprotokollen einzusehen.

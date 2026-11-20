# Timeline-Seite für Anfrageaktionen

Jede Aktion einer Anfrage im Gateway wird chronologisch angezeigt (feinere Aktionsgranularität als beim Phasenbaum).

## Begriffsklärung

Aktion = eine konkrete interne Operation (Cache lesen/schreiben, Regeln anwenden, Anfrage an den Upstream senden); mehrere Aktionen können gleichzeitig parallel ausgeführt werden.

## Selbstprüfung bei abweichenden Zahlen

- 1.Die Anzahl der Aktionen und Phasen unterscheidet sich: Eine Phase enthält mehrere Aktionen und hat eine andere Granularität.
- Der Abstand zwischen den Zeitstempeln beträgt 0: Aktionen mit einer Dauer von weniger als einer Millisekunde werden zusammengefasst angezeigt und gehen nicht verloren.

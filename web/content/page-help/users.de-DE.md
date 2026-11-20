# Datenerläuterung zur Benutzerlistenseite

Diese Seite verwaltet die Benutzerkonten der Personen, die sich bei dieser Plattform anmelden und sie verwenden können.

## Felder je Zeile

Benutzername, Rolle, Status (Aktiv/Inaktiv), zugehöriger Mandant, letzte Aktivitätszeit.

## Rolle und Sichtbarkeit

- Die Rolle bestimmt, welche Seiten eine Person nach der Anmeldung sehen kann: Administratoren sehen alle, Mandantenadministratoren nur die ihres eigenen Mandanten.
- „Inaktive“ Konten können sich nicht anmelden, ihre bisherige Nutzung und die Anfragen bleiben jedoch gespeichert und können weiterhin eingesehen werden.

## Definition der „letzten Aktivität“

- Es wird die neuere Zeit von **letzter Anmeldung oder letztem API-Aufruf** herangezogen.
- Die Anzeige „Vor langer Zeit“ bedeutet nicht, dass das Konto fehlerhaft ist: Möglicherweise wurde es in diesem Zeitraum lediglich nicht verwendet.

## Selbstprüfung bei abweichenden Zahlen

1. Die Benutzerzahl stimmt nicht mit der im Mandanten konfigurierten Anzahl der Lizenzplätze überein: Lizenzplätze beziehen sich auf die Berechtigung, die Liste auf tatsächliche Konten. Zusätzliche Konten können aus Testzeiträumen oder als Altbestand stammen.
2. Zwei Konten mit demselben Namen: Konten werden anhand ihrer eindeutigen ID unterschieden; gleiche Namen wirken sich weder auf die Abrechnung noch auf die Statistik aus. Eine Umbenennung führt nicht zu einer Zusammenführung der historischen Daten.
3. Ein gerade deaktiviertes Konto wird weiterhin als aktiv angezeigt: Der Status wird sofort wirksam, „Letzte Aktivität“ bleibt jedoch auf den Zeitpunkt vor der Deaktivierung stehen. Das ist normal.

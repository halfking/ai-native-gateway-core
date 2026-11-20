# Datenbeschreibung der Rundenliste

Diese Seite organisiert Modellaufrufe nach „Sitzung → Runde“: Eine Sitzung ist ein fortlaufender Dialog, eine Runde ist eine Frage-Antwort-Paarung.

## Hierarchische Beziehungen

- Projekt / Benutzer / Client / Schlüssel sind vier verschiedene Gruppierungsperspektiven; dieselben Sitzungen können aus unterschiedlichen Blickwinkeln betrachtet werden.
- Beim Erweitern einer Sitzung werden alle Runden angezeigt: Zusammenfassung des Frageinhalts, Zusammenfassung der Antwort, Dauer und Status.

## Zählung der Runden

- Eine Frage des Benutzers und eine Antwort des Modells zählen als eine Runde.
- Die Runden innerhalb einer Sitzung sind chronologisch sortiert.
- Eine Runde kann mehrere interne Modellaufrufe umfassen (Wiederholungen, Tool-Aufrufe). Diese zählen zu der jeweiligen Runde und werden nicht separat gezählt.

## Zusammenfassung und Status

- Die Sitzungszusammenfassung wird automatisch vom System erstellt; „Jetzt zusammenfassen“ löst manuell eine Neuerstellung aus. Während der Erstellung wird der laufende Vorgang angezeigt.
- Wenn die Aufzeichnung nicht verfügbar ist, fehlen in neuen Sitzungen möglicherweise vorübergehend die Details der Runden. Dies ist ein temporärer Zustand auf Erfassungsebene und kein Datenverlust.

## Selbstprüfung bei abweichenden Zahlen

1. Diese Seite aggregiert nach „Sitzung“, während die Seite mit den Anfrageprotokollen nach „einzelnem Aufruf“ aufgegliedert ist: Eine Anfrage kann in der Sitzungsansicht nur einen Teil einer Runde darstellen. Daher unterscheiden sich die Anzahl der Einträge auf beiden Seiten naturgemäß.
2. Je mehr Filterdimensionen (Projekt/Benutzer/Client/Schlüssel) ausgewählt werden, desto weniger Sitzungen werden angezeigt: Filter zurücksetzen und anschließend die Gesamtzahl vergleichen.
3. Für das Laden der Runden gibt es ein Limit (beim Erweitern werden die neuesten 100 Runden geladen): Bei sehr langen Sitzungen den Zeitraum eingrenzen oder die Seiten durchblättern.

# Datenbeschreibung der Betriebsdashboard-Seite

Diese Seite ist die Startseite für das Operations-Team: Sie konsolidiert verteilte Betriebskennzahlen (Nutzung, Freigaben, Hintergrundaufgaben) auf einem Bildschirm.

## Aufbau der Seite

- **Nutzungsübersichtskarte**: zentrale Kennzahlen wie Anzahl der Anfragen, Erfolgsquote und Kosten (aus derselben Quelle und mit derselben Berechnungslogik wie im Dashboard).
- **Hintergrundaufgaben**: Status und Zeitpunkt der jeweils letzten Ausführung periodischer Systemaufgaben (Aggregation, Statusprüfungen, Synchronisierung).
- **Selbstprüfungsübersicht**: kurzer Überblick über den Zustand der kritischen Komponenten.

## Hintergrundaufgaben richtig lesen

- Jede Aufgabe zeigt „letzte Ausführungszeit + Ergebnis“; „Fehlgeschlagen“ bedeutet nicht, dass der Betrieb ausfällt – die meisten Aufgaben werden im nächsten Zyklus automatisch erneut ausgeführt.
- Erst wenn eine Aufgabe über einen längeren Zeitraum nicht ausgeführt wurde (mehr als das Doppelte des Zyklus), ist Aufmerksamkeit erforderlich.

## Berechnungsgrundlagen

- Die Zahlen auf den Karten beziehen sich standardmäßig auf „Heute“ (Tageswechsel nach UTC); nach Auswahl eines anderen Zeitraums entsprechen sie denen im Dashboard.
- Auf dieser Seite werden keine Summen über unterschiedliche Einheiten hinweg angezeigt; Ausgabenkarten zeigen jeweils nur eine klar definierte Berechnungsgrundlage.

## Selbstprüfung bei abweichenden Zahlen

1. Die Zahlen auf den Karten weichen um wenige Einträge vom Dashboard ab: Bei Live-Traffic ist es normal, dass die Werte zwischen zwei Aktualisierungen weiter steigen; schließen Sie das Fenster „Heute“ oder warten Sie die nächste Aktualisierung ab.
2. Alle Hintergrundaufgaben werden als fehlgeschlagen angezeigt: Prüfen Sie zuerst, ob die Speicherebene (Datenbank) nicht verfügbar ist – die meisten Aufgaben hängen gemeinsam davon ab.
3. Eine gerade bereitgestellte Aufgabe wurde noch nicht ausgeführt: Sie wird als „Nicht ausgeführt“ angezeigt; warten Sie einen Zyklus ab.

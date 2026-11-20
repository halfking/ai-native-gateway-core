# Datenübersicht der Konversationsanalyse

Diese Seite beantwortet die Frage „Wie wird das Angebot genutzt?“: Wie viele Konversationen gibt es, wie lange dauern sie im Durchschnitt, welche Aufgaben werden bearbeitet und wie entwickeln sich die Zahlen?

## Kernkennzahlen

| Karte | Bedeutung | Berechnung |
| --- | --- | --- |
| Gesamtzahl der Konversationen | Anzahl der Dialoge im Zeitraum | De-Duplizierung: Eine Konversation wird nur einmal gezählt, unabhängig von der Anzahl ihrer Runden |
| Durchschnittliche Token-Zahl | Durchschnittlicher Textumfang pro Konversation | Gesamtzahl der Token aller Konversationen ÷ Gesamtzahl der Konversationen |
| Durchschnittliche Konversationsdauer | Durchschnittliche Dauer einer Konversation | Durchschnitt der Zeitdifferenz zwischen dem ersten und letzten Zeitpunkt jeder Konversation |
| Durchschnittliche Rundenzahl | Durchschnittliche Anzahl der Frage-und-Antwort-Runden pro Konversation | Gesamtzahl der Runden ÷ Gesamtzahl der Konversationen |
| Interne Kosten (USD) | Kosten in USD, die die Plattform an den vorgelagerten Anbieter zahlt | Nach den Preisen des vorgelagerten Anbieters je Vorgang addiert |
| Externe Kosten (Credits) | Credits, die den Mietern berechnet werden | Nach den Preisen der Plattform je Vorgang addiert |

**Interne und externe Kosten werden nicht zusammengefasst**: USD und Credits sind zwei unterschiedliche Einheiten; ihre Addition hat keine geschäftliche Bedeutung. Im Trenddiagramm oben auf der Seite werden sie daher getrennt in den beiden Einheiten dargestellt.

## Trends und Rankings

- Das Trenddiagramm wird nach Tagen aggregiert (Tageswechsel in Weltzeit UTC) und zeigt die tägliche Veränderung der Konversationszahl oder der Kosten.
- „Gegenüber dem vorherigen Zeitraum“ = prozentuale Veränderung des aktuellen Zeitfensters gegenüber dem gleich langen vorherigen Zeitfenster.
- Die Rangliste (Personen/Agenten/Modelle) wird nach der ausgewählten Kennzahl sortiert; die Spalte „Ausgaben“ addiert die Werte beider Messgrößen und **dient nur zum relativen Vergleich der Höhe und darf nicht als Betrag gelesen werden**.

## Datenaktualität

Diese Seite verwendet einen „aggregierten Snapshot“, der vom System in regelmäßigen Abständen zusammengefasst wird, und keine Details einzelner Vorgänge. Snapshots haben eine Aktualitätsfrist (ca. 90 Minuten); nach Ablauf zeigt die Seite den Banner „Aggregierte Daten werden aktualisiert“ an und gibt keine veralteten Zahlen zurück – dies ist eine Schutzmaßnahme und kein Fehler.

## Selbstprüfung bei abweichenden Zahlen

1. Zeitraum: Auf dieser Seite und auf der Seite mit den Anfrageprotokollen ist das Zeitfenster unterschiedlich; die Zahlen weichen daher zwangsläufig voneinander ab. Gleichen Sie zuerst den Zeitraum ab.
2. Snapshot-Verzögerung: Die neuesten Anfragen sind möglicherweise noch nicht im aggregierten Snapshot enthalten. Vergleichen Sie die Zahlen erneut, nachdem die Aktualisierung abgeschlossen ist.
3. Unterschiedliche Berechnungsgrundlagen: Diese Seite aggregiert nach „Konversation“, die Anfrageprotokollseite nach „einzelnem API-Aufruf“; die Anzahlen sind daher von Natur aus nicht gleich.
4. De-Duplizierungsregel: Die Anzahl der Konversationen wird pro Konversation dedupliziert, sodass mehrere Runden derselben Konversation nicht mehrfach gezählt werden; erst die Rundenzahl zählt jede Runde einzeln.

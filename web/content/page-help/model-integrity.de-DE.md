# Datenhinweise zur Überwachung der Modellintegrität

Diese Seite überwacht, „ob die vom Upstream gelieferten Antworten vollständig sind und unbemerkt ausgetauscht wurden“: Modellaustausch, Kürzungen, leere Antworten, Wiederholungen und Fingerabdruck-Drift gelten allesamt als Integritätsanomalien.

## Zwei Tabs

- **Integritätsereignisse**: Für jede Anomalie wird ein Datensatz erfasst (Typ, Schweregrad, betroffenes Modell/betroffener Anbieter, ob sie gelöst wurde).
- **Fingerabdruck-Drift**: Die zeitliche Veränderung des „Verhaltensfingerabdrucks“ eines Modells – eine starke Abweichung deutet darauf hin, dass der Upstream stillschweigend die Implementierung geändert hat.

## Definition der Kennzahlen

- Die Gesamtzahl der Ereignisse / nicht gelöste Ereignisse / kritische Ereignisse werden für das ausgewählte Zeitfenster gezählt; das Zeitfenster kann geändert werden.
- „Kritisch“ = Anomalien, die die Richtigkeit oder Sicherheit der Antworten beeinträchtigen (z. B. Modellaustausch); „Kürzung/leere Antwort“ wird in der Regel als mittel eingestuft.
- Fingerabdruck-Drift ist ein **Vergleich relativ zur Baseline**: Nach einer Aktualisierung der Baseline werden die historischen Drift-Ergebnisse neu berechnet.

## Selbstprüfung bei abweichenden Zahlen

1. Die Ereignisanzahl weicht von der auf der Seite zu Formatanomalien ab: Die beiden Seiten betrachten unterschiedliche Anomalietypen (diese Seite ist für „Antwortintegrität“ zuständig, die andere für „Antwortformat“); es gibt Überschneidungen, aber keine vollständige Übereinstimmung.
2. Die Anzahl der „nicht gelösten“ Ereignisse sinkt nicht: Die Behebung muss manuell bestätigt werden; wie auf der Seite zu Formatanomalien wird ein Fall nach einer automatischen Wiederherstellung nicht automatisch geschlossen.
3. Bei allen Modellen tritt plötzlich eine Fingerabdruck-Drift auf: Prüfen Sie zuerst, ob die Baseline gerade aktualisiert oder das Zeitfenster gewechselt wurde, bevor Sie vermuten, dass der Upstream seine Implementierung für alle Modelle geändert hat.

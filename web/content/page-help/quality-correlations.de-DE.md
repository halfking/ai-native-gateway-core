# Datenbeschreibung der Seite zur Qualitätskorrelationsanalyse

Diese Seite beantwortet die Frage „Welche Faktoren beeinflussen die Antwortqualität?“: Sie analysiert den statistischen Zusammenhang zwischen den Merkmalen eines Requests (Prompt-Länge, mit Bild, mit Code, Anzahl der Tool-Aufrufe) und dem Qualitätsscore der Antwort.

## Kernbereiche

- **Prädiktoren für die Qualität** (Korrelationskoeffizienten-Tabelle): Die Stärke des Zusammenhangs zwischen jedem Merkmal und der Qualität (je größer |r|, desto stärker die Vorhersagekraft).
- **Vergleich nach Buckets**: Die Requests werden anhand des jeweiligen Merkmalswerts in Buckets gruppiert (z. B. fünf Buckets für die Prompt-Länge); pro Bucket werden der durchschnittliche Qualitätsscore, die Erfolgsquote und die Latenz angezeigt.

## Berechnungsgrundlagen

- Der Qualitätsscore stammt aus einer automatischen Bewertung der Antwort durch das System (Zusammensetzung aus mehreren Dimensionen), nicht aus Benutzerbewertungen.
- Der Korrelationskoeffizient (Pearson r) liegt zwischen -1 ~ +1: Ein positiver Wert bedeutet, dass ein größeres Merkmal mit höherer Qualität einhergeht; ein negativer Wert bedeutet, dass ein größeres Merkmal mit niedrigerer Qualität einhergeht; ein Wert nahe 0 bedeutet, dass kein Zusammenhang besteht.
- Buckets enthalten nur Requests mit Qualitätsscore; Requests, die von der Bewertung nicht erfasst wurden, fließen nicht in den Nenner ein.

## Selbstprüfung bei Abweichungen in den Zahlen

1. Starker Zusammenhang, aber fachlich nicht plausibel: Es kann eine gemeinsame Ursache geben (z. B. „Requests mit Bild stammen ausgerechnet alle von einem bestimmten Anbieter“); beziehen Sie die Bucket-Details ein.
2. Gesamtzahl der Requests in den Buckets < Gesamtzahl der Requests auf der Seite: Nicht bewertete Requests werden in den Buckets nicht berücksichtigt; die Differenz entspricht dem Anteil „nicht bewertet“.
3. Bei zwei Betrachtungen ist r für dasselbe Merkmal unterschiedlich: Die Zeitfenster sind verschieden; r reagiert empfindlich auf das Zeitfenster. Halten Sie beim Vergleich das Zeitfenster fest.

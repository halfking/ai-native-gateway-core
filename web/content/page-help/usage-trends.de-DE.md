# Datenhinweise zur Seite „Nutzungstrends“

Diese Seite beantwortet mit einem großen Diagramm die Frage „Wie verändert sich die Nutzung im Zeitverlauf?“: Die Nutzung kann nach Modell/Schlüssel/Mandant multidimensional aufgeschlüsselt werden, um Zu- und Abnahmen zu erkennen und Anomalien aufzudecken.

## Berechnungsgrundlagen des Diagramms

- Der Zeitraster kann „stündlich/täglich“ gewählt werden; bei „täglich“ wird die Mitternachtsgrenze anhand der Weltzeit (UTC) festgelegt.
- Bei einer flachen Linie (Nulllinie) sind zwei Bedeutungen zu unterscheiden: **tatsächlich null** (es gab tatsächlich keine Nutzung) und **nicht berechnet** (die Datenquelle war noch nicht bereit). Die Seite weist deutlich darauf hin, wenn die Datenquelle noch nicht bereit ist; in diesem Fall bedeutet die flache Linie nicht null.
- Bei Mehrfachauswahl von Modellen werden die Kurven **einzeln angezeigt** und nicht gestapelt; zur Berechnung der Summe addieren Sie die Werte der einzelnen Tage.

## Filter sind durch „UND“ verknüpft

Wenn Sie Zeit + Modell + Schlüssel auswählen und anschließend weitere Filter anwenden, erfüllen die Ergebnisse gleichzeitig alle Bedingungen.

## Abweichungen bei den Zahlen selbst prüfen

1. Die Zahl weicht um einen Eintrag von den Dashboard-Zahlen ab: Die Trendseite verwendet Daten aus der aggregierten Ansicht. Anfragen der letzten Minuten sind noch nicht in die Aggregation eingeflossen; maßgeblich sind die aktuellen Zahlen im Dashboard.
2. Nahe null fehlt der Kurve „ein Raster“: Bei einer Tagesaggregation über die UTC-Mitternachtsgrenze hinweg entspricht die Tageszeit in Pekinger Zeit dem Nachmittag des vorherigen Tages in UTC. Unterschiedliche Startzeitpunkte des Zeitfensters sind eine Abweichung der Berechnungsgrundlage.
3. Nach der Aufteilung nach Dimensionen entspricht die Summe nicht der Gesamtkurve: Bei einigen Anfragen fehlen Dimensionswerte (z. B. weil das vorgelagerte System keinen Modellnamen zurückgegeben hat). Diese fließen nur in den Gesamtwert ein, nicht in die Aufteilung – die Seite weist den Umfang des nicht zugeordneten Anteils aus.

# Dispatch-Wasserfalldiagramm

Jede Anfrage wird als Wasserfall dargestellt: Die Kandidaten werden nacheinander ausprobiert, und es ist sofort erkennbar, welcher Auftrag angenommen wurde und welcher fehlgeschlagen ist.

## Erläuterung der Messwerte

Die Länge des Balkens entspricht der benötigten Zeit des jeweiligen Kandidaten; Erfolgs- bzw. Fehlermeldungen stehen am Balkenende. Für jede Anfrage gibt es nur einen endgültig erfolgreichen Eintrag.

## Selbstprüfung bei abweichenden Zahlen

- 1. Die Anzahl der Wasserfallbalken ist größer als die Anzahl der Kandidaten: Interne Wiederholungen sind enthalten. Richten Sie sich nach der Zeile mit der Angabe „Endgültig erfolgreich“.
- Die Gesamtzeit unterscheidet sich von den Angaben in den Anfragedetails: Das Wasserfalldiagramm umfasst nur die Dispatch-Phase, nicht die Zeit für die Modellantwort.

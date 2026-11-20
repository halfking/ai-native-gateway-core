# Routing-Entscheidungsseite

Protokoll des endgültigen Routing-Ergebnisses je Anfrage (wer ausgewählt wurde und warum).

## Definitionen

Eine Zeile = eine Entscheidung; einschließlich der zutreffenden Regelkette und des endgültigen Kandidaten.

## Selbstprüfung bei abweichenden Zahlen

- 1. Entscheidungsanzahl ≠ Anfrageanzahl: Vorab blockierte Anfragen werden nicht weitergeleitet und erzeugen keinen Entscheidungseintrag.
- Zwei Entscheidungen für dieselbe Anfrage: Der Hauptaufruf und der Wiederholungsversuch erzeugen jeweils eine Entscheidung.

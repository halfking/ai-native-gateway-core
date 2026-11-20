# Seite der Routing-Strategien

Routing-Parameter: Reihenfolge der Ebenen, Obergrenze für Kandidaten, Fallback-Regeln.

## Klarstellung der Begriffe

Die Strategie legt fest, „wie ausgewählt wird“; die Kandidatendaten legen fest, „was ausgewählt wird“.

## Selbstprüfung bei abweichenden Zahlen

- 1. Nach einer Änderung der Strategie bleibt der Traffic unverändert: Die Strategie wirkt sich nur auf neue Anfragen aus; laufende Anfragen werden nach der alten Strategie abgeschlossen.
- Nach dem Vertauschen der Ebenenreihenfolge tritt ein unerwartetes Verhalten auf: Zwischen den Ebenen bestehen Abhängigkeiten; prüfen Sie nach der Anpassung zunächst auf der Entscheidungsseite die Berechnungsergebnisse.

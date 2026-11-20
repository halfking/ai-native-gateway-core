# Detailseite zum Probe-Status

Detailliertes Profil für ein einzelnes Probe-Objekt: historische Erfolgsquote der Probes, Latenzverteilung und Fehlerdetails.

## Methodik

Probe = ein vom System aktiv ausgeführter Testaufruf; die Erfolgsquote wird anhand der Anzahl der Probes berechnet und getrennt vom Geschäftsverkehr ausgewiesen.

## Selbstprüfung bei abweichenden Zahlen

- 1. Alle Probes sind erfolgreich, bei Geschäftsanfragen treten jedoch Fehler auf: Der Probe-Pfad kann vom tatsächlichen Pfad abweichen (z. B. durch eine vereinfachte Anfrage); der tatsächliche Traffic ist maßgeblich.
- Die Latenz stimmt nicht mit den Anfrageprotokollen überein: Probes verwenden eine feste kleine Anfrage; die Latenz ist daher naturgemäß niedrig.

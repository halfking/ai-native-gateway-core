# Verbindungsregister-Seite

Erfassen Sie für jede Upstream-Anmeldedaten die „Verbindungsidentität“: Knoten, Wiederherstellungszeitleiste und Verlauf der Statusänderungen.

## Hinweise zur Abgrenzung

Eine Anmeldung pro Zeile; die Wiederherstellungszeitleiste erfasst jeden Schritt von der Störung bis zur Wiederherstellung.

## Selbstprüfung bei abweichenden Zahlen

- 1.Die Anmeldedaten sind auf der Überwachungsseite für Anmeldedaten normal, hier jedoch auffällig: Die beiden Seiten bieten unterschiedliche Perspektiven (die Überwachung zeigt den aktuellen Zustand, das Register das historische Archiv). Verfolgen Sie den Vorgang anhand der Zeitleiste.
- Lücke in der Wiederherstellungszeitleiste: Während des Neustarts des Erfassungsdienstes werden fehlende Einträge nicht nachträglich ergänzt; solche Lücken sind ein bekanntes Muster.

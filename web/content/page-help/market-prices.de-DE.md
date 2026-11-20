# Datenhinweise zur Marktpreisseite

Diese Seite verwaltet „externe Marktdaten“: Marktdaten zu Modellen werden von verschiedenen Marktplattformen abgerufen, um Preise zu vergleichen und als Referenz für die Preisgestaltung zu dienen. Diese Seite dient dem **schreibgeschützten Abgleich und der Metadatenverwaltung**; Preise werden hier nicht direkt festgelegt.

## Die beiden Bereiche

- **Plattformregister**: Erfasst, von welchen Marktplattformen Daten abgerufen werden. Als „Eigen“ oder „Deaktiviert“ gekennzeichnete Plattformen werden weder an der Marktdatensynchronisierung noch an der Berechnung des Markt-Mindestpreises beteiligt.
- **Synchronisierungsprotokoll**: Zeigt die Ausführungen der letzten 10 Abrufe (Zeitpunkt, Anzahl abgerufener Datensätze, Anzahl abgelehnter Datensätze).

## Abgleichsformel

Bei jeder Synchronisierung gilt: **Anzahl der Quelldatensätze = Anzahl der übernommenen Datensätze + Anzahl der abgelehnten Datensätze**. Alle drei Zahlen sind im Protokoll sichtbar. Bei einer Differenz öffnen Sie die Details der abgelehnten Datensätze, um den Grund zu prüfen (häufige Gründe: Währung und Region passen nicht zusammen, doppelte Einträge).

## Zuordnungsregeln für Regionen und Währungen

Plattformen im Ausland ↔ USD, Plattformen im Inland ↔ CNY; beide müssen paarweise konfiguriert sein. Datensätze mit einer falschen Zuordnung werden bei der Synchronisierung abgelehnt.

## Selbstprüfung bei Zahlenabweichungen

1. Im Protokoll steht in der Spalte „Abgelehnt“ eine rote Zahl: Das ist Teil des vorgesehenen Abgleichsmechanismus. Öffnen Sie die Details der betreffenden Synchronisierung, um die konkreten Ablehnungsgründe zu prüfen.
2. Der in den Marktdaten angezeigte Preis unterscheidet sich von dem Marktpreis, den Sie selbst sehen: Die Marktdaten werden in einem Abrufzyklus aktualisiert und sind nicht in Echtzeit verfügbar. Prüfen Sie beim Vergleich das „Synchronisierungsdatum“.
3. Für eine Plattform sind noch keine Daten vorhanden: Prüfen Sie im Register, ob sie deaktiviert wurde, oder ob die Synchronisierung für sie noch nie ausgeführt wurde.

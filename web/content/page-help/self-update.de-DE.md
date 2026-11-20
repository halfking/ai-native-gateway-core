# Datenhinweise zur Selbstaktualisierung

Diese Seite verwaltet die Versionsupgrades des Gateways selbst: Neueste Version prüfen → Erinnerung → Installation nach Bestätigung durch die Administration.

## Drei Status

- **Keine Versionsquelle konfiguriert**: Auf diesem Knoten ist nicht angegeben, „wo nach neuen Versionen gesucht werden soll“. Die automatische Prüfung ist nicht aktiviert.
- **Versionsstatus unbekannt**: Die Prüfung wurde durchgeführt, es konnte jedoch kein Ergebnis ermittelt werden (Netzwerk oder Quelle nicht erreichbar).
- **Normal**: Die aktuelle und die neueste Version sind bekannt und können miteinander verglichen werden.

## Upgrade-Prozess

1. „Sofort auf Updates prüfen“ fragt bei der konfigurierten Versionsquelle die neueste Versionsnummer ab.
2. Ist eine neue Version verfügbar, wird eine Erinnerung erzeugt; **es erfolgt keine automatische Installation**.
3. Nachdem die Administration die Installation bestätigt hat, wird der Upgrade-Prozess gestartet. Nach Abschluss des Upgrades wird der Dienst automatisch mit der neuen Version neu gestartet.

## Hinweise zu den verwendeten Begriffen

- Die „Aktuelle Version“ ist die Version, **die auf diesem Knoten ausgeführt wird**. Wenn das System mit mehreren Knoten betrieben wird, können diese unterschiedliche Versionen ausführen. Diese Seite betrachtet nur diesen Knoten.
- Während des Upgrades wird der Dienst kurzzeitig neu gestartet: In diesem Zeitraum werden Anfragen von anderen Knoten übernommen (bei mehreren Knoten) oder die Verfügbarkeit ist kurzzeitig unterbrochen (bei einem einzelnen Knoten).

## So prüfen Sie Abweichungen bei den Versionsnummern selbst

1. Nach einem Upgrade wird weiterhin die alte Version angezeigt: Die auf der Seite zwischengespeicherten Versionsinformationen müssen einmal aktualisiert werden, damit die Anzeige auf den neuesten Stand gebracht wird.
2. Die „Neueste Version“ weicht von der offiziellen Veröffentlichungsseite ab: Diese Seite liest die **konfigurierte Versionsquelle**. Wenn diese nicht mit der offiziellen Quelle synchronisiert ist, hat die konfigurierte Quelle Vorrang. Prüfen Sie zunächst die Adresse der Versionsquelle.
3. Die Update-Prüfung bleibt dauerhaft auf „Unbekannt“: Die Adresse der Versionsquelle ist nicht erreichbar. Testen Sie die Erreichbarkeit dieser Adresse direkt auf dem Bereitstellungsrechner.

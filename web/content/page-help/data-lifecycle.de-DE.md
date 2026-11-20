# Datenhinweise zur Seite „Datenslebenszyklus“

Diese Seite verwaltet das „Speichern, Verschieben und Aufräumen“ von Daten: Welche Daten in der Datenbank Platz belegen, wann sie archiviert und wie sie zurückgewonnen werden.

## Seitenreiter im Überblick

- **Speicherüberblick**: So viel Platz belegen die einzelnen Tabellen und wie schnell wachsen sie.
- **Migration von Hot-Tabellen & Partitionsbereinigung**: Große Tabellen mit häufigen Schreibvorgängen werden monatlich in getrennten Ablagen gespeichert; ältere Monate werden vollständig archiviert.
- **Fallback-Wiederherstellung & Bereinigung**: Aggregierte Daten bei Beschädigung aus Einzeldaten neu aufbauen.
- **Speicherkonfiguration**: Einstellungen für Datenbankverbindung und Speicherorte.
- **Protokollverwaltung**: Aufbewahrung und Bereinigung von Betriebsprotokollen.
- **Sitzungsanhänge / Große Felder / Dateisystem**: Platzbelegung und Wartung von Dateidaten.

## Wichtige Kennzahlen und Begriffe

- **Aufbewahrungszeit** = Wie lange Daten in der Datenbank aufbewahrt werden; abgelaufene Daten werden als „bereinigbar“ eingestuft, und vor der Bereinigung kann die Anzahl der betroffenen Zeilen in der Vorschau angezeigt werden.
- „Bereinigbar“ bedeutet nicht „gelöscht“: Die Bereinigung muss zunächst in der Vorschau angezeigt und anschließend manuell ausgeführt werden; wenn zwischen Vorschau und tatsächlicher Ausführung neue Daten geschrieben werden, kann die tatsächliche Zeilenzahl leicht abweichen.
- Die Platzbelegung eines Tabellenbereichs steigt zunächst an und sinkt anschließend: Während einer Bereinigung (Platzfreigabe) verdoppelt sich die Plattenbelegung zunächst (alte Datei + neue Datei), und der Platz wird erst nach Abschluss der Bereinigung freigegeben.

## Sicherheitsmechanismen

- Für alle Bereinigungsaktionen ist zunächst eine „Vorschau“ und danach eine „Ausführung“ erforderlich; die beiden Schritte sind getrennt.
- Bei der Archivierung werden Daten in langsamen Speicher verschoben, nicht gelöscht; sie können weiterhin zur Wiederherstellung abgerufen werden.

## Selbstprüfung bei abweichenden Zahlen

1. Die Belegung wird aus Datenbankstatistiken ermittelt und weist daher eine feste Abweichung gegenüber der Dateigröße auf, die das Betriebssystem anzeigt (die Datenbank enthält internen Redundanzspeicher). Eine gleiche Größenordnung genügt.
2. Eine im Zeitverlauf steigende Anzahl „bereinigbarer“ Zeilen ist normal (Daten altern) und kein anomaler Anstieg.
3. Schwankende Zeilenzahlen während eines Rebuilds oder einer Migration sind ein Zwischenstand; maßgeblich ist der Endstatus nach Abschluss des Vorgangs.
4. Prüfen Sie zunächst den Seitenreiter: Für dieselbe Tabelle gelten je nach Seitenreiter andere Kennzahlen (im Überblick die Belegung, auf der Partitionenseite die Verteilung der Zeilenzahlen).

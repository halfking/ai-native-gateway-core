# Datenerläuterung zur Seite „Angebotsarbeitsplatz“

Diese Seite ist ein interner Arbeitsbereich zur Angebotserstellung für Kunden: Zuerst erfassen Sie in „Was wir anbieten können“, danach in „Was der Kunde benötigt“; das System erstellt daraus ein Angebot.

## Vier Tabs

- **Angebotspool**: Aktuell auf der Plattform verfügbare Modellliste (einschließlich Kostenpreis, Währung und Konfidenz; alle Werte werden serverseitig berechnet und können nicht manuell geändert werden).
- **Nachfragepool**: Vom Kunden gewünschte Modelle und Nutzungsmengen.
- **Angebot**: Formales Angebot nach dem Abgleich von Angebot und Nachfrage (mit Versionsnummer; kann freigegeben und geteilt werden).
- **Dashboard** (nur für Superadmins): Übersicht über den Angebotsfunnel und Kostenabweichungen.

## Wichtige Berechnungsgrundlagen

- **Kostenbasis**: Die Kosten einer Angebotsposition stammen aus der Kostenpreistabelle auf der Seite „Preisverwaltung“; die im Angebot ausgewiesenen Kosten sind eine Momentaufnahme zum Zeitpunkt der Erstellung. Spätere Kostenänderungen wirken sich nicht rückwirkend auf bereits erstellte Angebote aus.
- **Version**: Jede Überarbeitung desselben Angebots erzeugt eine neue Version; historische Versionen sind schreibgeschützt und dienen der Nachverfolgung.
- **Kostenabweichungs-Badge**: Wenn die Abweichung zwischen den dem Angebot zugrunde liegenden Kostenpreisen und den aktuellen Kostenpreisen den Schwellenwert überschreitet, erscheint eine Warnung. So wird vermieden, dass Angebote auf veralteter Kostenbasis erstellt werden.

## Selbstprüfung, wenn Zahlen nicht übereinstimmen

1. Ein Modell ist im Angebotspool nicht vorhanden: Prüfen Sie zunächst, ob sein Kostenpreis bereits auf der Seite „Preisverwaltung“ erfasst wurde. Modelle ohne Kostenpreis können nicht angeboten werden.
2. Die Kosten im Angebot stimmen nicht mit den aktuellen Kostenpreisen überein: Im Angebot sind die zum Erstellungszeitpunkt geltenden Kosten fixiert. Prüfen Sie am Badge „Kostenabweichung“, ob das Angebot neu erstellt werden muss.
3. Ein Eintrag im Nachfragepool ist verschwunden: Nachfragen, die zu einem Angebot überführt wurden, werden aus dem Nachfragepool archiviert und sind auf der Angebotsseite sichtbar.

# Datenübersicht der Seite „Anfragedetails“

Diese Seite zeigt **den vollständigen Datensatz einer Anfrage**: den gesamten Ablauf vom Eingang im Gateway bis zur Ausgabe der Antwort sowie alle zugehörigen Zahlen.

## Die drei Bereiche

- **Grundinformationen**: Zeitpunkt, Schlüssel, Modell, Status, Gesamtbearbeitungszeit.
- **Prozessdetails**: Dauer und Ergebnis der einzelnen Schritte (Authentifizierung, Routing, Upstream-Aufruf, Maskierung und Rückschreibung, Verbrauchserfassung).
- **Inhalte und Nutzung**: Zusammenfassung der tatsächlich gesendeten/empfangenen Inhalte, Umfang des eingegebenen/ausgegebenen Textes, Kosten dieser Anfrage.

## Wichtige Berechnungsgrundlagen

- **Gesamtbearbeitungszeit** = von der Annahme der Anfrage bis zur Rückgabe der vollständigen Antwort; sie umfasst sowohl die Gateway-Verarbeitungszeit als auch die Generierungszeit des Upstream-Anbieters. In den Prozessdetails werden beide getrennt ausgewiesen.
- **Kosten** werden anhand der zum Anfragezeitpunkt gültigen Preisliste berechnet; spätere Preisanpassungen ändern die Kosten dieser historischen Anfrage nicht.
- Die Inhaltszusammenfassung wird möglicherweise gekürzt angezeigt (aus Leistungsgründen); den vollständigen Inhalt können Sie unter „Volltext anzeigen“ einsehen.

## Selbstprüfung bei Zahlenabweichungen

1. Die Kosten dieser Anfrage weichen vom Katalogpreis des Modells ab: Prüfen Sie zuerst den Zeitpunkt der Anfrage und den Zeitpunkt der Preisanpassung – der Preis wird zum **Anfragezeitpunkt** eingefroren.
2. Der Umfang des eingegebenen Textes ist größer als Ihre eigene Zählung: Die Systemzählung umfasst die Verpackung der Nachrichten (System-Prompt, bisherige Gesprächsverläufe) und nicht nur den von Ihnen eingegebenen Satz.
3. Der Status ist „Erfolgreich“, der Nutzer meldet jedoch, nichts erhalten zu haben: Ein Erfolg aus Sicht des Gateways bedeutet, dass der Upstream-Anbieter die Antwort vollständig zurückgegeben hat; bricht die Netzwerkverbindung des Clients danach ab, liegt das Problem im nachgelagerten Abschnitt der Übertragung. Prüfen Sie in der Aktionszeitleiste der Anfrage den Schritt, in dem die Antwort zurückgesendet wurde.
4. Hinweise auf Wiederholungen: Bei einer Anfrage können intern mehrere Kandidaten getestet werden; im endgültigen Status wird nur der letzte Versuch ausgewiesen. In den Prozessdetails ist die vollständige Kette der Versuche sichtbar.

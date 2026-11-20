# Erläuterung zu den Daten der Betriebsübersichtsseite

Diese Seite ist ein „globales Kommandobrett“ für mehrere Bereitstellungsorte: Der Zentraldienst, die Regionen, die Bereitstellungsknoten und der Zustand der Datenebene sind auf einen Blick sichtbar.

## Die vier Bereiche

- **Zentrale Instanz**: Betriebsstatus und Version des Zentraldiensts.
- **Regionale Verteilung**: Verteilung und Status der Bereitstellungsorte nach Region.
- **Bereitstellungsknoten**: Letzte Bereitstellungsprotokolle (Zeitpunkt, Version, Ergebnis).
- **Datenebene**: Größe und Status der zentralen Datentabellen.

## Statusdefinition

- Der Status der Knoten wird von den jeweiligen Knoten regelmäßig gemeldet; „Offline“ = seit mehr als einem Meldeintervall keine Nachricht. Dies kann entweder daran liegen, dass der Knoten nicht mehr läuft, oder dass die Netzwerkverbindung unterbrochen ist.
- Die Versionsnummer richtet sich nach der Meldung des jeweiligen Knotens; treten in derselben Region mehrere Versionen auf, handelt es sich um einen Zwischenstatus während eines Rolling Upgrades.

## Aktualisierungsintervall

- Die Daten auf dieser Seite werden in einem festen Intervall aktualisiert (nicht sekundenweise in Echtzeit); ein gerade abgeschlossenes Deployment ist erst nach der nächsten Aktualisierung sichtbar.
- „Letzte N Einträge“: N ist die maximale Anzahl der auf der Seite angezeigten Einträge, nicht der gesamte Verlauf; den vollständigen Verlauf finden Sie in der Deployment-Verwaltung.

## Selbstprüfung, wenn Zahlen nicht übereinstimmen

1. Prüfen Sie zuerst den Zeitpunkt der Abfrage: Zwischen dem Status auf dieser Seite und dem lokal auf dem Knoten angezeigten Status liegt ein Aktualisierungsintervall.
2. Die Summe für eine Region entspricht nicht der Summe der einzelnen Knoten: Knoten, die nicht gemeldet haben, werden als „Unbekannt“ behandelt und nicht in die regionale Summe einbezogen.
3. Abweichende Versionen weisen nicht zwangsläufig auf einen Fehler hin: Während eines Rolling Upgrades ist es erwartbar, dass alte und neue Versionen parallel vorhanden sind. Nach Abschluss des Upgrades sollten sie zu einer einzigen Version konsolidiert sein.

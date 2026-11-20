# Datenhinweise zur Seite „Proxy-Verwaltung“

Diese Seite verwaltet den „Proxy-Ausgang“, über den das Gateway auf den Upstream zugreift: Abonnementquellen, Knotenpool und derzeit verwendeter Ausgang.

## Die drei Tabs

- **Statusübersicht**: Übersichtskarten (Anzahl der Abonnements, Gesamtzahl der Knoten, Anzahl der dialfähigen/gesunden Knoten) + aktuell ausgewählter Ausgang + Status der automatischen Umschaltung.
- **Abonnementverwaltung**: Proxy-Abonnementquellen (Links zum Importieren einer Reihe von Knoten); mit einer „Regional-Blacklist“ können Ausgänge gefiltert werden.
- **Knotenverwaltung**: Manuelle Registrierung und Wartung einzelner Knoten.

## Wichtige Definitionen

- **Dialfähig** = Der Knoten kann derzeit eine Verbindung herstellen; **gesund** = Der Knoten ist bei aufeinanderfolgenden Prüfungen erfolgreich. Ein dialfähiger Knoten, der wiederholt fehlschlägt, gilt nicht als gesund.
- **Automatische Umschaltung**: Wenn der aktuelle Ausgang eine Schwelle aufeinanderfolgender Fehler erreicht, wird automatisch der nächste Knoten verwendet; die „Anzahl aufeinanderfolgender Fehler“ wird in Echtzeit hochgezählt und nach einem Erfolg auf null zurückgesetzt.
- „Letzte aktive Prüfung“ ist der Zeitpunkt der vom System selbst eingeleiteten Konnektivitätsprüfung, nicht der Zeitpunkt einer geschäftlichen Anfrage.

## So prüfen Sie Abweichungen bei den Zahlen selbst

1. Gesamtzahl der Knoten ≠ Anzahl der dialfähigen Knoten: Das ist normal – die Gesamtzahl ist der Bestand an registrierten Knoten, die dialfähige Anzahl ist die derzeit verbindbare Anzahl.
2. Nach der Umschaltung scheitert der Geschäftsverkehr weiterhin: Die Umschaltung folgt einem Prüfzyklus und ist innerhalb weniger Sekunden abgeschlossen; bei anhaltenden Fehlern prüfen Sie, ob die „Anzahl aufeinanderfolgender Fehler“ weiterhin steigt.
3. Nach einer Abonnementaktualisierung bleibt die Knotenanzahl unverändert: Möglicherweise ist der Inhalt der Abonnementquelle identisch; vergleichen Sie die Fingerprints der Knoten vor und nach der Aktualisierung, nicht die Anzahl.
4. Die Regional-Blacklist hat keine Wirkung: Regionalcodes müssen exakt mit dem von den Knoten gemeldeten Regionswert übereinstimmen (Groß-/Kleinschreibung beachtet); eine fehlerhafte Konfiguration führt zu keiner Fehlermeldung, bewirkt aber auch keine Filterung.

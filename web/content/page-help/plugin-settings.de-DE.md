# Datenhinweise zur Plugin-Konfigurationsseite

Auf dieser Seite wird für jeden „Plugin-Slot“ der Plattform ausgewählt, welche Implementierung verwendet wird (zum Beispiel für die Anmeldung: die integrierte Komponente oder ein externer Identitätsdienst).

## Verwendung

- Für jeden Plugin-Slot gibt es eine Karte: die aktuell wirksame Implementierung + eine Dropdown-Liste der verfügbaren Optionen.
- **Nach dem Speichern ist ein Neustart des Dienstes erforderlich**: Auf der Seite werden die Konfigurationen gespeichert; der Wechsel erfolgt erst beim nächsten Start – eine erfolgreiche Speicherung bedeutet nicht, dass bereits gewechselt wurde.

## Konfigurationsquelle

- Oben auf der Seite wird die „Konfigurationsquelle“ angezeigt: Sie gibt an, ob die aktuellen Werte aus der Datenbank oder aus Umgebungsvariablen stammen.
- Bei Werten aus Umgebungsvariablen überschreiben diese auch Änderungen auf der Seite – für eine dauerhafte Umstellung muss die Bereitstellungsumgebung geändert werden.

## So prüfen Sie Abweichungen bei Zahlen

1. Das Anmeldeverhalten hat sich nach der Umstellung nicht geändert: Prüfen Sie zunächst, ob der Dienst mit der neuen Konfiguration neu gestartet wurde.
2. Der auf der Seite angezeigte Status stimmt nicht mit der Realität überein: Richten Sie sich nach der „Konfigurationsquelle“ – Umgebungsvariablen haben Vorrang vor den auf der Seite gespeicherten Werten.
3. Für einen Plugin-Slot sind keine Optionen verfügbar: Die entsprechenden Implementierungen wurden noch nicht bereitgestellt; installieren Sie zuerst die zugehörige Komponente.

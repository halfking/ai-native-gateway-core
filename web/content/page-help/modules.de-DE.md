# Modulverwaltung

Aktivierungs- und Deaktivierungspanel für die Funktionsmodule der Plattform: Jedes Modul kann unabhängig aktiviert oder deaktiviert werden.

## Begriffsklärung

Modulstatus = Aktiviert/Deaktiviert; Bei deaktivierten Modulen wird der Eintrag ausgeblendet, die Konfiguration bleibt erhalten.

## Selbstprüfung bei abweichenden Zahlen

- 1.Nach der Deaktivierung bleibt der Menüpunkt sichtbar: Das Ausblenden des Eintrags ist durch die Cache-Verzögerung verzögert und verschwindet nach dem Neuladen.
- Das Modul ist aktiviert, die Funktion jedoch nicht verfügbar: Möglicherweise ist die zugrunde liegende Dienste, auf die das Modul angewiesen ist, noch nicht bereit. Prüfen Sie den Abhängigkeitsstatus in den Moduldetails.

# Datenhinweise zur Schlüsselliste

Diese Seite verwaltet die „Schlüssel“ für Aufrufe über das Gateway: Jeder Schlüssel steht für eine Identität, die einen Modellaufruf starten kann.

## Felder in jeder Zeile

- Name und Präfix: die gut lesbare Kennung eines Schlüssels; der vollständige Schlüssel wird nur bei der Ausstellung einmal angezeigt.
- Status: siehe die vier Statusangaben unten.
- Nutzung und Kontingent: der bereits verbrauchte Anteil am Kontingent.

## Wie die vier Status bestimmt werden

| Status | Bedingung |
| --- | --- |
| Aktivierung ausstehend | Der Schlüssel wurde ausgestellt, die erste Verifizierung ist jedoch noch nicht abgeschlossen. |
| Aktiv | Status „Normal“ + nicht abgelaufen + nicht deaktiviert; alle drei Bedingungen müssen gleichzeitig erfüllt sein |
| Abgelaufen | Ein Ablaufzeitpunkt ist festgelegt und dieser ist bereits überschritten |
| Widerrufen | Der Schlüssel wurde manuell deaktiviert |

Hinweis: „Aktiv“ setzt voraus, dass alle drei Bedingungen gleichzeitig erfüllt sind – sobald der Schlüssel abgelaufen oder deaktiviert wurde, zeigt die Seite auch dann „Abgelaufen“ oder „Widerrufen“, wenn das Statusfeld im Backend weiterhin „Normal“ lautet.

## Berechnungsgrundlage der Nutzungsstatistik

- Die Nutzungszahl zählt **abgeschlossene Aufrufe**; laufende Anfragen werden nicht einbezogen.
- Nutzungsdaten können kurzfristig verzögert sein: Ein gerade gestarteter Aufruf wird möglicherweise erst nach kurzer Zeit in der Nutzung angezeigt.

## Selbstprüfung bei abweichenden Zahlen

1. Prüfen Sie, welche Kategorie im Tab des Statusfilters ausgewählt ist: Oben auf der Seite gibt es Tabs nach Status; je nach Auswahl werden unterschiedliche Listeninhalte angezeigt.
2. Für die Bestimmung des Ablaufs ist die Serverzeit maßgeblich; eine Abweichung der Uhrzeit des lokalen Computers hat keinen Einfluss.
3. Systemeigene Schlüssel (die die Plattform selbst verwendet) erscheinen dauerhaft in der Liste und werden nicht in den regulären Statistiken berücksichtigt.
4. Kontingent und Nutzung stammen aus zwei verschiedenen Datenquellen und werden unterschiedlich häufig aktualisiert: Die Nutzung wird nahezu in Echtzeit aktualisiert, Änderungen am Kontingent wirken sofort. Wenn die Werte nicht übereinstimmen, laden Sie zuerst die Seite neu.

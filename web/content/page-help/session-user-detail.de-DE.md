# Datenbeschreibung zur Detailseite für Sitzungsnutzer

Diese Seite zeigt das vollständige Nutzungsprofil **einer Person**: deren Sitzungen, Projekte, Aufgaben, häufig genutzten Modelle und Kostenverlauf.

## Inhaltsaufbau

- **Übersichtskarte**: Anzahl der Sitzungen, Token, Kosten, Erfolgsquote, Cache-Trefferquote, durchschnittliche Dauer.
- **Trendbereich**: Tageskurven für Sitzungen/Kosten im vom Benutzer ausgewählten Zeitfenster.
- **Detailtabelle**: Zeilen mit Nutzungsdaten, aufgeschlüsselt nach Projekt / Aufgabe / Agent / Modell.

## Berechnungsgrundlagen

- Alle Zahlen erfassen ausschließlich Sitzungen, die **vom betreffenden Benutzer gestartet** wurden; eine Mitwirkung bei der Zusammenarbeit (sein Auftreten in den Sitzungen anderer) zählt nicht mit.
- Die Kennzahl „Kosten“ wird nach zwei Berechnungsweisen parallel angezeigt; die „Ranking“-Karte zeigt die Position des Benutzers in der Gesamtrangliste und die Veränderung gegenüber dem vorherigen Zeitraum.

## Selbstprüfung bei abweichenden Zahlen

1. Die Summe auf der Detailseite ≠ der Wert in der entsprechenden Zeile der Listenseite: Gleichen Sie zunächst die Zeitbereiche beider Seiten ab (oben auf der Detailseite gibt es eine eigene Zeitbereichsauswahl).
2. Die Summe der Projekt-/Aufgabendetails ≠ die Gesamtmenge in der Übersicht: Sitzungen, die weder einem Projekt noch einer Aufgabe zugeordnet sind, fließen nur in die Gesamtmenge ein. Ist am unteren Rand der Detailtabelle eine Zeile „Nicht zugeordnet“ vorhanden, dient diese zur Erklärung der Differenz.
3. Die Richtung der „Rangveränderung“ ist umgekehrt: Die Veränderung bemisst sich nach der **Rangposition** (steigend = besserer Platz), nicht nach der Zu- oder Abnahme des Zahlenwerts.

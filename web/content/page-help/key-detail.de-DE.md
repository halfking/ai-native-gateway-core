# Detailsseite für Schlüssel

Vollständiges Profil eines einzelnen Schlüssels: Status, Limit, Nutzungsverlauf und letzte Aufrufe.

## Erläuterung der Berechnungsweise

Der Nutzungsverlauf wird nach dem ausgewählten Zeitfenster aggregiert; die vier Statuswerte entsprechen denen auf der Listenseite.

## Selbstprüfung bei abweichenden Zahlen

- 1. Detailnutzung ≠ Abrechnung: Die Abrechnung erfolgt nach Kalendertagen (UTC), während das Detailfenster frei angepasst werden kann. Richten Sie zunächst die Zeitfenster aneinander ab.
- Keine letzten Aufrufe vorhanden, die Nutzung ist jedoch ungleich null: Unter den letzten Aufrufen werden nur Detailstichproben angezeigt, nicht alle einzelnen Vorgänge.

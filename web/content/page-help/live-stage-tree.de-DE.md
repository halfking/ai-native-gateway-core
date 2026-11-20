# Erläuterung der Daten auf der Seite „Anfragephasenbaum“

Auf dieser Seite wird der „Lebenszyklus“ einer Anfrage in zehn Phasen aufgeteilt und entlang einer Zeitachse angeordnet. Bei Problemen ist sofort erkennbar, an welchem Schritt die Anfrage hängen bleibt.

## Zehn Phasen (in Reihenfolge des Auftretens)

Authentifizierung/Ratenbegrenzung → Datenmaskierung → Freigabe → Injektionserkennung → Routing → Komprimierung → Anbieter/Token-Pool → Erstes Byte → Rückschreiben der Maskierung → Abrechnung/Antwort.

- Die Sendespur (links→rechts) zeigt, wie die Anfrage nach außen geht; die Empfangsspur (rechts→links) zeigt, wie die Antwort zurückkommt.
- Eine Anfrage durchläuft möglicherweise nicht alle Phasen: Wird keine Freigabe ausgelöst, wird in der Phase „Freigabe“ der Status „Nicht zutreffend“ angezeigt.

## Sieben Phasenstatus

Nicht erreicht / In Bearbeitung / Abgeschlossen / Nicht zutreffend / Fehlgeschlagen / Warnung / Nicht gemessen.

- „Fehlgeschlagen“ = Die Anfrage wurde in diesem Schritt blockiert, beispielsweise bei einer positiven Injektionserkennung oder einer abgelehnten Freigabe.
- „Warnung“ = Die Anfrage wurde durchgelassen, verdächtige Punkte wurden jedoch protokolliert.
- „Nicht gemessen“ = Für diese Phase liegen keine Zeitmessdaten vor. Das bedeutet nicht, dass sie fehlgeschlagen ist.

## Phasendauer

- Die Länge des Balkens gibt die Dauer der jeweiligen Phase an; die Zeit vom „Ersten Byte“ bis zur „Antwort“ ist die Generierungszeit des Modells und normalerweise am längsten.
- Gesamtzeit ≠ einfache Summe der Phasen: Einige Phasen laufen parallel.

## Selbstprüfung, wenn Zahlen nicht übereinstimmen

1. Bei einer Anfrage mit dem Status „In Bearbeitung“ steigt die Phasendauer noch an und wird erst nach Abschluss festgeschrieben.
2. Wenn eine Anfrage nicht gefunden wird, prüfen Sie zuerst die Filter: Die Seite zeigt nur Anfragen, die den aktuellen Filterkriterien entsprechen und im ausgewählten Zeitfenster liegen.
3. Viele Phasen mit dem Status „Nicht gemessen“ deuten darauf hin, dass die Anfrage im alten Ablaufprotokollformat erfasst wurde (frühere Daten). Dies ist eine historische Datenform und kein Systemfehler.
4. Bei fehlgeschlagenen Anfragen richtet sich die Diagnose nach der „Fehlgeschlagenen Phase“: Sehen Sie zuerst auf die erste Phase mit dem Status „Fehlgeschlagen“. Die Phasen danach werden normalerweise nicht ausgeführt.

# Datenbeschreibung der Mandantenliste

Ein Mandant ist die „Kundeneinheit“ der Plattform: Jeder Mandant hat eigene Schlüssel, Benutzer, Kontingente und Nutzungen, die voneinander isoliert sind.

## Felder jeder Zeile

Mandantenname, Mandantencode (eindeutiger Kurzcode innerhalb des Systems), Status und Kontingentverbrauch.

## Kontingentsystem

- **Abonnementkontingent**: Das vorrangig genutzte Kontingent aus dem Monatspaket, das jeden Monat zurückgesetzt wird.
- **Wallet/Barguthaben**: Die Quelle für die nutzungsabhängige Abrechnung, nachdem das Abonnement aufgebraucht wurde.
- Verrechnungsreihenfolge: Zuerst das Abonnementkontingent, dann das Wallet. In der Detailansicht der Seite werden sie getrennt ausgewiesen und nicht zu einem einzelnen „Guthaben“ zusammengefasst.

## Bedeutung der Mandantentrennung

- Die Schlüssel von Mandant A können nicht auf die Konfiguration von Mandant B zugreifen; Statistiken, Protokolle und Kontingente werden vollständig je Mandant getrennt verbucht.
- Der „Standardmandant“ ist der interne Betriebsbereich der Plattform und wird parallel zu den kostenpflichtigen Kundenmandanten verwaltet.

## Selbstprüfung bei Abweichungen

1. Das verbleibende Kontingent stimmt nicht überein: Prüfen Sie zuerst den Zeitpunkt der Messung – das Kontingent wird bei jedem Aufruf in Echtzeit abgezogen, während die auf der Seite angezeigten Werte auf Sekundenbasis zwischengespeichert sind.
2. Der Verbrauch in der Mandantendetailansicht stimmt nicht mit dem Übersichts-Dashboard überein: Das Dashboard enthält auch den internen Verbrauch des Standardmandanten. Für die Abrechnung eines bestimmten Mandanten sind die Angaben in der Mandantendetailansicht maßgeblich.
3. Ein neuer Mandant zeigt sofort Daten von „gestern“: Das ist nicht möglich. Falls dies angezeigt wird, wurde mit hoher Wahrscheinlichkeit ein historischer Mandantencode wiederverwendet; prüfen Sie zuerst die Eindeutigkeit des Codes.

# Datenerläuterung zur Konversationsnutzerliste

Diese Seite zeigt die Nutzung nach Person: Jede Person wird in einer Zeile aufgeführt, mit einer Zusammenfassung ihrer Konversationen, Token, Kosten und am häufigsten verwendeten Modelle.

## Felder je Zeile

Benutzer (Anzeigename/Konto), Anzahl der Konversationen, Token-Anzahl, Kosten, Erfolgsquote, Top-Liste der am häufigsten verwendeten Modelle (Zeile erweitern, um Details anzuzeigen).

## Verhältnis zur Konversationsanalyse

- Die Übersichtsseite enthält die Gesamtzusammenfassung; auf dieser Seite werden dieselben Daten **nach Person aufgeschlüsselt**. Bei identischem Zeitfenster stimmen die Werte überein.
- Die Ranking-Seite ist eine sortierte Ansicht dieser Seite (Top N); auf dieser Seite wird die vollständige Liste angezeigt, die durchsucht werden kann.

## Hinweise zur Berechnungsgrundlage

- In der Spalte „Kosten“ werden zwei Abrechnungsarten parallel angezeigt und nicht miteinander umgerechnet.
- Konversationen werden der startenden Person zugeordnet: Bei mehreren Teilnehmern an derselben Konversation wird die Nutzung der startenden Person zugerechnet.

## So prüfen Sie Abweichungen bei den Zahlen

1. Die Benutzerzahl ist höher als die Anzahl der Systembenutzer: Auf dieser Seite werden Identitäten gezählt, die Konversationsaktivität hatten (einschließlich System-/Servicekonten); die Liste der Personen finden Sie auf der Benutzerlistenseite.
2. Die Summe der Modellnutzung in der erweiterten Zeile ist ≠ der Gesamt-Token-Anzahl der Zeile: In der erweiterten Liste sind nur die Top-Modelle aufgeführt; selten verwendete Modelle werden unter „Sonstige“ zusammengefasst.
3. Für dieselbe Person gibt es zwei Zeilen: Änderungen am Konto oder Anzeigenamen können zwei historische Identitäten erzeugen. Das Verhalten wird nach Konto zugeordnet; verwenden Sie für den Abgleich die Kontospalte.

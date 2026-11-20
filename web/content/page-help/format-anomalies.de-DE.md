# Datenhinweise zur Seite „Formatabweichungen“

Diese Seite überwacht zwei Arten von „Auffälligkeiten“: Entweder hat sich das **Format** der Upstream-Antwort plötzlich geändert, oder die **Anfrage ist fehlgeschlagen**. So sollen Änderungen auf Anbieterseite erkannt werden, bevor Nutzer sie bemerken.

## Zwei Datenquellen (zwei Tabs)

- **Abweichung im Antwortformat**: Die Struktur der Upstream-Antwort entspricht nicht den Erwartungen (fehlende Felder, geänderter Typ, fehlgeschlagene Token-Extraktion usw.).
- **Anfragefehler**: Die Anfrage selbst ist fehlgeschlagen (Zeitüberschreitung, Ablehnung, fehlerhafte Parameter usw.).

Die Zahl im Badge oben entspricht der **Summe** der „Nicht gelöst“-Einträge aus beiden Datenquellen und verwendet dieselbe Zählweise wie der Filter „Nicht gelöst“ auf der Listenseite.

## Wie „Nicht gelöst / Gelöst“ berechnet wird

- Neu erkannte Abweichungen sind standardmäßig „Nicht gelöst“; nach manueller Bestätigung und Bearbeitung werden sie als „Gelöst“ markiert.
- „Gelöst“ bedeutet nicht, dass der Eintrag automatisch verschwindet, sondern dass er manuell bestätigt wurde – so wird zwischen „gesehen“ und „nicht gesehen“ unterschieden.

## So prüfen Sie selbst nach, wenn die Zahlen nicht übereinstimmen

1. Die Zahl im Badge stimmt nicht mit der Zahl in der Liste überein: Prüfen Sie zuerst die Filter (Ist „Nur nicht gelöst“ ausgewählt? Welcher Zeitraum ist eingestellt?).
2. Eine Änderung beim Anbieter kann dazu führen, dass viele gleichartige Abweichungen gleichzeitig auftreten: Gruppieren Sie nach „Abweichungstyp“. Die Einträge eines Typs haben in der Regel eine gemeinsame Ursache.
3. Sowohl Test- als auch echter Traffic kann Einträge auslösen: Bestimmen Sie vor der Bewertung der Auswirkungen zuerst, aus welcher Art von Traffic die Abweichung stammt.
4. Wird eine Abweichung automatisch behoben (der Anbieter nimmt die Änderung zurück), wird sie nicht automatisch als „Gelöst“ markiert: Eine manuelle Bestätigung ist weiterhin erforderlich. Dies ist absichtlich so gestaltet, damit stille Änderungen nicht übersehen werden.

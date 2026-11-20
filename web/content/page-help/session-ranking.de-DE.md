# Datenhinweise zur Konversationsrangliste

Diese Seite gliedert die Konversationsanalyse in drei Ranglisten: **Wer sie nutzt** (Personen), **Wer die Arbeit erledigt** (Agenten), **Was am häufigsten genutzt wird** (Modelle).

## Spalten der Rangliste

- Anzahl der Konversationen / Anzahl der Anfragen / Token-Anzahl: Jeder Wert wird unabhängig gezählt (Konversationen werden dedupliziert, Anfragen einzeln gezählt, Token kumuliert).
- Kosten: **Gesamtsumme** aus internen Kosten und externen Credits; sie dient nur dem relativen Vergleich und darf nicht als Geldbetrag interpretiert werden (zwei Einheiten).
- Erfolgsquote / Cache-Trefferquote / Durchschnittsdauer: Effizienzkennzahlen des jeweiligen Eintrags.

## Sortierung und Zeitfenster

- Wählen Sie oben auf der Seite den Zeitraum aus; die Rangliste wird nach der ausgewählten Kennzahl sortiert, und ein Klick auf die Spaltenüberschrift ändert den Sortierschlüssel.
- „Rangänderung“ = Platzierungsänderung gegenüber dem vorherigen gleich langen Zeitfenster; bei neuen Einträgen wird „Neu“ angezeigt.

## Selbstprüfung bei Zahlenabweichungen

1. Die Konversationsanzahl derselben Person ist an zwei Stellen unterschiedlich: Die Ranglistenseite aggregiert den ausgewählten Zeitraum; auf der Detailseite ist möglicherweise standardmäßig ein anderer Zeitraum ausgewählt. Gleichen Sie zuerst den Zeitraum ab.
2. Die Spalte „Kosten“ stimmt nicht mit der Rechnung überein: Sie enthält die Gesamtsumme aus beiden Berechnungsgrundlagen und dient nur dem Vergleich; verwenden Sie für den Abgleich unbedingt die Seite mit dem Abstimmungsbericht.
3. Abweichung zwischen anonymem Namen und Anzeigename: Der festgelegte Anzeigename hat Vorrang; ist keiner vorhanden, wird der Systemkontoname angezeigt; nach einer Umbenennung werden historische Daten weiterhin unter derselben Identität aggregiert und nicht auf zwei Personen aufgeteilt.
4. Summe der Rangliste ≠ Gesamtzahl auf der Übersichtsseite: Die Rangliste zeigt die Top-Einträge; Objekte mit geringem Nutzungsvolumen aus dem Long Tail werden nicht aufgeführt, während die Übersichtsseite alle umfasst.

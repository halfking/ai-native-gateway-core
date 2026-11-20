# Datenhinweise zur Modellglaubwürdigkeit

Diese Seite erkennt, ob ein Upstream-Anbieter ein Modell mit niedrigerer Konfiguration so ausgibt, als hätte es eine höhere – umgangssprachlich: „abgewässert“/„heruntergestuft“. Die Bewertung stützt sich auf vier miteinander abgeglichene Maßstäbe. Wenn die Prüfung keinen belastbaren Schluss ergibt, wird ehrlich „Evidenz unzureichend“ ausgewiesen; dies ist nicht als „kein Problem“ zu verstehen.

## Vier Ergebnisarten

| Ergebnis | Bedeutung |
| --- | --- |
| Verifiziert | Mehrere Belege stimmen überein; die zugesagte Modellstufe wird tatsächlich geliefert |
| Fragwürdig | Es gibt verdächtige Signale, die jedoch für eine abschließende Beurteilung nicht ausreichen; eine erneute Prüfung wird empfohlen |
| Abgewässert | Es gibt starke Belege dafür, dass die tatsächlich bereitgestellte Modellstufe unter der angegebenen liegt |
| Evidenz unzureichend | Zu wenige Belege, um eine Beurteilung vorzunehmen (**bedeutet nicht, dass alles in Ordnung ist**) |

## Vier Maßstäbe (Entscheidungsgrundlage)

Selbstauskunft des Upstream-Anbieters über seine Identität, Fähigkeitsvergleich desselben Modells, Prüfung auf Abweichungen bei der Anfragesignatur und Kalibrierung anhand externer Baseline-Werte.

## Hinweise zur Methodik

- Als „entschieden“ ausgewiesene Knoten werden je Kombination aus Modell × Anbieter gezählt; dasselbe Modell wird bei unterschiedlichen Anbietern jeweils separat bewertet.
- Maßgeblich ist das Ergebnis der letzten Prüfung; eine „vollständige Prüfung“ führt sämtliche Prüfungen erneut aus und überschreibt die bisherigen Ergebnisse.

## Selbstprüfung bei abweichenden Zahlen

1. Viele „fragwürdige“ Ergebnisse, obwohl im Geschäftsbetrieb keine Auffälligkeiten bestehen: „Fragwürdig“ ist kein Schuldspruch, sondern ein Hinweis auf eine erneute Prüfung; berücksichtigen Sie dabei auch Fingerabdruckabweichungen auf der Seite zur Integritätsüberwachung.
2. Unmittelbar nach der Prüfung lautet das Ergebnis „Evidenz unzureichend“: Für diese Kombination gab es zuletzt zu wenig Traffic, sodass die Stichprobe nicht ausreichte; prüfen Sie erneut, sobald tatsächlicher Traffic vorhanden ist.
3. Das Ergebnis passt nicht zu den im Backend des Anbieters angezeigten Fähigkeiten: Der „Fähigkeitsvergleich“ innerhalb der vier Maßstäbe stützt sich auf den auf dieser Plattform gemessenen Traffic; bei selten verwendeten Modellen liegen von Natur aus weniger Belege vor.

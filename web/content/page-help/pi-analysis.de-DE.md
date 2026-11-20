# Datenhinweise zur Seite „Intelligente Fehleranalyse“

Diese Seite verwendet KI, um zu analysieren, „warum Anfragen fehlschlagen“: Fehler aus der Warteschlange werden einzeln (oder gesammelt) zur Analyse an einen intelligenten Agenten übergeben. Dabei werden Ursachenbewertungen und Korrekturvorschläge erstellt.

## Zwei Tabs

- **Fehlerwarteschlange**: Liste der zu analysierenden Fehlerdatensätze (jede Zeile entspricht einer fehlgeschlagenen Anfrage).
- **Analyseergebnisse**: Liste der von der KI erzeugten Schlussfolgerungen (Ursachenkategorien, Grundlagen, Empfehlungen).

## Statistische Bereiche der sechs Karten oben

- Gesamt / Zu analysieren / Analysiert / Fehlgeschlagen: Statistik aus der **Fehlerwarteschlange** (Warteschlangenbereich).
- Fehlalarmquote / Durchschnittliche Dauer: Statistik aus den **Analyseergebnissen** (Ergebnisbereich).
- Die drei Karten und die übrigen drei Karten haben unterschiedliche statistische Bereiche; eine Summe wäre nicht sinnvoll – die Seite gruppiert die Werte nach Bereich.

## Umsetzung der Korrekturvorschläge

- Von der KI abgegebene Empfehlungen (Regel ändern/Schweregrad anpassen/Abgleichsmuster ändern) werden erst nach **manueller Genehmigung** wirksam. Für die Genehmigung gilt ein Konfidenzschwellenwert; Empfehlungen mit geringer Konfidenz werden blockiert und müssen überprüft werden.
- Bei jeder Umsetzung wird ein „Snapshot des alten Werts“ gespeichert, sodass die Änderung mit einem Klick zurückgerollt werden kann.

## Selbstprüfung bei abweichenden Zahlen

1. Die Anzahl in der Warteschlange unterscheidet sich von der Fehleranzahl auf der Seite mit den Anfrageprotokollen: Die Warteschlange enthält nur Fehlerarten, die **in den Analyseumfang einbezogen** sind; vorab blockierte Anfragen (z. B. Authentifizierungsfehler) werden nicht aufgenommen.
2. „Analysiert“, aber auf der Ergebnisseite nicht wiederzufinden: Die Analyseergebnisse werden mit kurzer Verzögerung gespeichert. Aktualisieren Sie die Seite und prüfen Sie sie erneut.
3. Derselbe Fehler tritt wiederholt auf: Fehler mit demselben Muster werden zusammengefasst analysiert; in der Warteschlange wird ein repräsentatives Beispiel angezeigt.

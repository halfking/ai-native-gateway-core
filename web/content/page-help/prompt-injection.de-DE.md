# Datenhinweise zur Seite „Prompt-Injection-Schutz“

Diese Seite konfiguriert den „Schutz vor unbefugten Anweisungen“: Sie erkennt in Benutzereingaben eingebettete Angriffsversuche wie „Regeln ignorieren“ oder „als System auftreten“.

## Drei Tabs

- **Strategie**: Hauptschalter und Betriebsmodus.
- **LLM-Engine**: Konfiguration der Engine für eine zweite Beurteilung mithilfe eines Modells.
- **Erkennungsregeln**: Liste der konkreten Regeln für Angriffsmerkmale.

## Zwei Betriebsmodi (wichtiger Unterschied)

- **Beobachtungsmodus (nur Protokollierung)**: Die Erkennungsergebnisse werden lediglich protokolliert und **keine Anfrage wird blockiert** – so lässt sich zunächst die Falsch-positiv-Rate prüfen, bevor entschieden wird, wie streng die Regeln sein sollen.
- **Erzwungener Modus**: Gemäß der „Schweregradmatrix“ werden Aktionen ausgeführt (durchlassen / manuelle Genehmigung erforderlich / direkt blockieren); ein auf 0 gesetztes Genehmigungs-Timeout bedeutet, dass unbegrenzt gewartet wird.

## Hinweise zur Auswertung

- Die Anzahl der Regeltreffer wird als „erkannt“ gezählt; Treffer im Beobachtungsmodus **lösen keine** Blockierung aus und werden in der Statistik getrennt vom erzwungenen Modus ausgewiesen.
- Die Schweregradmatrix bestimmt, „wie derselbe Treffer in den einzelnen Modi jeweils behandelt wird“ – wenn Sie die Matrix ändern, müssen Sie beide Spalten gemeinsam prüfen.

## Selbstprüfung bei abweichenden Zahlen

1. Sie sehen einen Treffer, die Anfrage wird jedoch nicht blockiert: Meist ist der Beobachtungsmodus aktiv (so vorgesehen); prüfen Sie vor dem Wechsel in den erzwungenen Modus die Falsch-positiven dieser Regel.
2. Hohe Zahl von Falsch-positiven: Prüfen Sie die Trefferbeispiele Regel für Regel; häufige Falsch-positive sind Formulierungen wie „Ignoriere die obigen Anweisungen“ in normalen Geschäftsvorgängen. Passen Sie die betreffende Regel an, statt den Hauptschalter zu deaktivieren.
3. Im erzwungenen Modus wird alles zur Genehmigung weitergeleitet: In der Schweregradmatrix sind alle hohen Schweregrade auf „Genehmigung“ gesetzt. Wenn die genehmigenden Personen nicht verfügbar sind, wirkt dies, als würde „alles hängen bleiben“; passen Sie die Matrix oder das Genehmigungs-Timeout an.

# Seite mit der Gesundheitszeitleiste für Knoten

Eine Zeitleiste mit dem Gesundheitsverlauf eines Anmeldedatensatzes: wann er funktioniert, wann er ausfällt und wann er wiederhergestellt ist.

## Hinweise zur Interpretation

X-Achse = Zeit, Farbblöcke = Status; die Zeitleiste wird ausschließlich fortgeschrieben und nie geändert und ist die primäre Aufzeichnung, um die Qualität der Anmeldedaten rückwirkend zu prüfen.

## Selbstprüfung bei abweichenden Zahlen

- 1. Lücke in der Zeitleiste: Erfassungslücken werden nicht nachträglich ergänzt (Neustartfenster des Dienstes); Lücke ≠ Normalzustand.
- Der Status auf der Überwachungsseite weicht ab: Richten Sie sich nach dem jüngsten Farbblock in der Zeitleiste; die Überwachungsseite kann zwischengespeicherte Werte anzeigen.

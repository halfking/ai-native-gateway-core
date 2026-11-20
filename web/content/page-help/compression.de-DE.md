# Datenübersicht der Komprimierungsverwaltung

Diese Seite überwacht die „Schlankung übergroßer Konversationen“: wie das System eine Konversation komprimiert, wenn sie zu lang wird, um in die Modellkapazität zu passen.

## Kernkennzahlen

- **Anzahl komprimierter Konversationen**: Anzahl der Konversationen, bei denen eine Komprimierung ausgelöst wurde.
- **Komprimierungsrate**: Anteil der Inhalte, die durch die Komprimierung eingespart wurden.
- **Verteilung der Strategien**: Anzahl der Verwendungen je Komprimierungsstrategie (vorausschauend/reaktiv/Fenster-/inkrementell usw.).
- **Trend der Komprimierungsrate**: Veränderung der täglichen durchschnittlichen Komprimierungsrate.

## Unterschied zwischen den zwei Auslösern

- **Vorausschauende Komprimierung** (vorläufig): Komprimierung erfolgt vorzeitig, wenn absehbar ist, dass die Kapazität nahezu erschöpft ist; für die Nutzerinnen und Nutzer bleibt dies unbemerkt.
- **Erzwungene Komprimierung**: Komprimierung und erneuter Versuch erfolgen erst, nachdem der vorgelagerte Dienst die Anfrage wegen unzureichender Kapazität abgelehnt hat. Dadurch entstehen zusätzliche Kosten durch einen fehlgeschlagenen Aufruf, die Komprimierung wird jedoch nur bei Bedarf durchgeführt.

## Erläuterung der Berechnungsgrundlage

- Die Komprimierung erfolgt, „bevor die Anfrage an das Modell gesendet wird“; die ursprüngliche Konversation bleibt unverändert. Erfasst wird der Anteil der „an den vorgelagerten Dienst gesendeten Daten“, der reduziert wurde.
- Eine Konversation kann mehrfach eine Komprimierung auslösen (bei zunehmender Länge wird sie wiederholt geschlankt); jeder Vorgang wird separat erfasst.

## Selbstprüfung bei Abweichungen bei den Zahlen

1. Eine ungewöhnlich hohe Komprimierungsrate um 100 %: Prüfen, ob der Komprimierungsschwellenwert falsch konfiguriert wurde und dadurch auch sehr kleine Anfragen komprimiert werden.
2. Dass die Anzahl der „komprimierten“ Konversationen deutlich höher ist als die Anzahl der „erzwungenen Komprimierungen“, ist normal: Die meisten werden vorausschauend komprimiert; nur die übrigen werden erzwungen komprimiert.
3. Die Konfiguration zeigt „Werksstandard“: Oben auf der Seite wird ein Fehler beim Lesen der Konfiguration angezeigt. Die Angaben unterhalb der Meldung entsprechen dann nicht der aktuellen Konfiguration. Beheben Sie zunächst den Lesefehler, bevor Sie die Parameter anpassen.

# Erläuterungen zu den Daten im Anfrageprotokoll

Diese Seite ist das „Journal“ aller Modellaufrufe: Jede Anfrage wird in einer Zeile angezeigt, nach den gewünschten Kriterien gefiltert und anschließend einzeln eingesehen.

## Was jede Zeile darstellt

Eine Zeile = ein Modellaufruf. Enthalten sind: Aufrufzeitpunkt, verwendeter API-Schlüssel und Modell, Status (Erfolg/Fehler), Dauer, Umfang der Eingabe-/Ausgabetexte sowie die Kosten dieses Aufrufs.

## Regeln für das Zeitfenster

- Auswählbare Schnellbereiche (letzte 1 Stunde bis laufender Monat) oder benutzerdefinierte Start- und Endzeit.
- **Nicht-Standard-Mandanten sehen maximal die letzten 3 Tage**: Bei Auswahl eines größeren Zeitraums verkürzt das System ihn automatisch auf 3 Tage. Im Zeitwähler ist zu erkennen, dass der Zeitpunkt auf einen früheren Wert zurückgesetzt wurde.
- Standardmandanten haben alle Zeitbereiche zur Verfügung, einschließlich der letzten 7 Tage, der laufenden Woche, des laufenden Monats und des laufenden Jahres.

## Mehrere Filter sind durch „und“ miteinander verknüpft

Wenn mehrere Filterkriterien gleichzeitig festgelegt werden (Zeit + Schlüssel + Modell + Status + Anbieter), müssen die Ergebnisse alle Bedingungen erfüllen. Je mehr Bedingungen festgelegt sind, desto kleiner wird die Ergebnismenge.

## Bedeutung der Statusangaben

- „Erfolg“ = Das Modell hat normalerweise eine vollständige Antwort zurückgegeben.
- Fehlerstatus werden nach Grund unterteilt (Fehler beim vorgelagerten Dienst, gesperrter Inhalt, Zeitüberschreitung usw.). Jede Anfrage wird nur einer Kategorie zugeordnet.

## Selbstprüfung bei abweichenden Zahlen

1. Ein abweichendes Zeitfenster ist die häufigste Ursache: Auf dieser Seite und auf der Statistikseite muss derselbe Zeitbereich ausgewählt sein.
2. Nach dem Anwenden von Filtern wird nur eine „Teilmenge“ angezeigt: Entfernen Sie alle Filter, bevor Sie die Gesamtzahl vergleichen.
3. Die Mandantenidentität beeinflusst den sichtbaren Bereich: Nicht-Standard-Mandanten sehen nur die Anfragen ihres eigenen Mandanten und maximal die letzten 3 Tage.
4. Diese Seite enthält Einzeldetails (die einzeln abgeglichen werden können), während die Statistikseite eine Zusammenfassung (bereits dedupliziert und aggregiert) darstellt: Bei Stichproben ist diese Seite maßgeblich, beim Vergleich mit der Gesamtzahl die Statistikseite.

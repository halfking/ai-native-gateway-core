# Datenerläuterung zur Detailseite der Konversation

Diese Seite zeigt das Gesamtbild **einer vollständigen Konversation**: was besprochen wurde, wie viele Gesprächsrunden sie umfasst, welche Fragen in den einzelnen Runden gestellt und welche Antworten gegeben wurden. So können Administratoren den gesamten Ablauf eines einzelnen Servicevorgangs nachvollziehen.

## Was Sie auf dieser Seite sehen

- **Konversationsübersichtsleiste**: Zusammenfassung des Themas, beteiligte Parteien, Anzahl der Gesprächsrunden und Status, ob die Konversation noch läuft.
- **Zeitachse der Gesprächsrunden**: Die einzelnen Frage-Antwort-Runden in chronologischer Reihenfolge, die jeweils zur vollständigen Anzeige aufgeklappt werden können.
- **Kernpunkte der Gesprächsrunden**: Verdichtete Zusammenfassungen wichtiger Runden, mit denen sich entscheidende Abschnitte schnell finden lassen.

## Hinweise zu Definitionen und Berechnungsgrundlagen

- **Eine Konversation** bezeichnet einen fortlaufenden Dialog; alle Zahlen auf der Seite beziehen sich ausschließlich auf **diese eine Konversation** und werden nicht über mehrere Konversationen hinweg summiert.
- **Die Anzahl der Gesprächsrunden** wird anhand der Abwechselnde von Frage und Antwort gezählt: Jede vom Benutzer gestellte Frage zählt als eine Runde. Unabhängig von der Länge der Antwort gehört sie weiterhin zu derselben Runde.
- **Die Zusammenfassung** wird vom System automatisch aus dem gesamten Dialog erstellt. Läuft die Konversation noch, kann die Zusammenfassung hinter den neuesten Runden zurückliegen; maßgeblich ist dann die „Generierungszeit“.
- **Die Zeitangabe** basiert auf der auf dem Server protokollierten Zeit. Bei der Anzeige in unterschiedlichen Zeitzonen gilt die einheitliche Systemzeit (UTC-Tageswechsel); eine Umrechnung in die lokale Zeitzone erfolgt nicht.

## Selbstprüfung bei abweichenden Zahlen

- **Die Anzahl der Gesprächsrunden stimmt nicht mit der Listenseite überein**: Möglicherweise wirken auf der Listenseite Filterkriterien. Entfernen Sie diese zunächst vollständig und vergleichen Sie die Werte anschließend erneut.
- **Die Zusammenfassung scheint nicht zur neuesten Konversation zu passen**: Während eine Konversation noch läuft, wird die Zusammenfassung in regelmäßigen Abständen erstellt. Warten Sie einige Minuten, laden Sie die Seite neu und prüfen Sie sie erneut. Liegt die „Generierungszeit“ vor der letzten Gesprächsrunde, ist das normal.
- **Der Inhalt einer Gesprächsrunde lässt sich nicht öffnen**: Möglicherweise wurde die Runde nach Ablauf der Aufbewahrungsfrist gelöscht. Sie ist weiterhin in der Liste vorhanden, ihr vollständiger Inhalt jedoch nicht mehr. Dies entspricht der vorgesehenen Aufbewahrungsrichtlinie.
- **Auf beiden Seiten gibt es eine Konversation mit demselben Titel**: Ob es sich um dieselbe Konversation handelt, richtet sich nach der Konversationsnummer. Ähnliche Titel bedeuten nicht, dass es sich um dieselbe Konversation handelt.

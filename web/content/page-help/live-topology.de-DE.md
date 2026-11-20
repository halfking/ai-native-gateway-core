# Datenhinweise zur Echtzeit-Topologie

Diese Seite ist das „Live-Verkehrsdiagramm“ des Gateways: Welche Knoten sind online, und wohin fließen Anfragen derzeit von wo?

## Elemente der Grafik

- **Knoten** = ein Servicepunkt, der Anfragen annehmen kann (Zugangsknoten, Bereitstellungsknoten).
- **Verbindung** = eine Linie zwischen zwei Knoten; die Dicke bzw. Anzahl der Linien zeigt das Verkehrsvolumen an.
- Eine Anfrage tritt auf der „Benutzerseite“ ein, wird vom Gateway geroutet und über eine „Bereitstellungsverbindung“ nach außen gesendet, um die Antwort zu erhalten.

## Knotenstatus

- Der Online-/Offline-Status basiert auf den Echtzeit-Heartbeats der Knoten; bei einer Störung von Redis wird der Gesundheitsstatus als „Unbekannt“ zurückgestuft – dies betrifft den Erfassungskanal und bedeutet nicht, dass der Knoten tatsächlich nicht verfügbar ist.
- Das Abzeichen „Interne Direktverbindung“ = dieser Bereitstellungsknoten wird direkt von der Plattform betrieben und nicht über einen Proxy eines Drittanbieters bereitgestellt.

## Echtzeithinweise

- Die Seite empfängt Updates über eine langlebige Verbindung und wird unter normalen Bedingungen im Sekundenbereich aktualisiert.
- Bei einer kurzzeitigen Verbindungsunterbrechung verbindet sich die Seite automatisch erneut; während der Unterbrechung gehen keine Anfragen verloren, die Ansicht wird lediglich mit Verzögerung vervollständigt.
- „N Anfragen über ungültige Knoten ausgeblendet“ = Anfragen, die als über ungültige Knoten gelaufen eingestuft wurden, werden standardmäßig nicht auf den Verbindungen angezeigt. Bei Bedarf kann die Anzeigeoption aktiviert werden.

## Selbstprüfung bei abweichenden Zahlen

1. Die Echtzeitanzeige ist eine Momentaufnahme des aktuellen Moments und nicht direkt mit den kumulierten Werten auf der Statistikseite für einen vergangenen Zeitraum vergleichbar; für den Abgleich verwenden Sie die Seite mit den Anfragenprotokollen.
2. Die Zahlen auf den Verbindungen geben die Anzahl der Anfragen im gleitenden Zeitfenster an. Da sich das Zeitfenster kontinuierlich verschiebt, nehmen die Zahlen natürlicherweise ab.
3. Bleibt eine Anfrage auf einer Verbindung stehen, prüfen Sie zuerst ihr Statuslabel: „In Bearbeitung“/„Abgeschlossen“/„Fehlgeschlagen“ stehen für verschiedene Phasen.
4. Wenn bei allen Knoten „Unbekannt“ angezeigt wird, liegt meistens eine Störung des Nachrichtenkanals vor. Aktualisieren Sie die Seite oder warten Sie kurz auf die erneute Verbindung.

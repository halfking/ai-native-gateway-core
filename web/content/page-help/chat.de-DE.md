# Datenhinweise zur Chatseite

Diese Seite ist das integrierte „Testchat-Fenster“: Sie können direkt mit dem über das Gateway angebundenen Modell chatten. So lässt sich am schnellsten prüfen, ob ein Schlüssel funktioniert.

## Voraussetzungen

Sie benötigen einen **zu Ihnen gehörenden und aktivierten** API-Schlüssel. Ohne Schlüssel führt Sie die Seite zunächst zur Beantragung; ist der Schlüssel deaktiviert oder abgelaufen, schlägt der Chat fehl und der Grund wird angezeigt.

## Modellauswahl

- Wählen Sie „auto“, wählt das Gateway automatisch ein geeignetes Modell (gemäß der Routing-Richtlinie).
- Bei manueller Modellauswahl wird Ihre Auswahl verwendet; ist das Modell nicht verfügbar, schlägt die aktuelle Runde fehl. Die nächste Runde ist davon nicht betroffen.
- **Änderungen an Parametern werden in der nächsten Runde wirksam**: Wenn Sie die Temperatur oder das Modell ändern, werden die neuen Werte ab der nächsten Nachricht verwendet; historische Nachrichten bleiben unverändert.

## Zuordnung zu den Statistiken

- Jede Chatrunde auf dieser Seite ist ein tatsächlicher Aufruf und **wird bei Nutzung und Kosten berücksichtigt** — auch Tests verursachen Kosten.
- Die Chatverläufe auf dieser Seite und die Einträge auf der Seite „Anfrageprotokoll“ gehören eins zu eins zusammen: Für jede Runde lassen sich dort die Details nachschlagen.

## So prüfen Sie Abweichungen bei den Zahlen selbst

1. Die Kosten zweier Runden in demselben Chat weichen stark voneinander ab: Im Modus „auto“ wird für jede Runde unabhängig ein Modell ausgewählt. Dabei kann es zu einem teureren oder günstigeren Modell gewechselt worden sein.
2. Eine Antwort wurde abgeschnitten: Prüfen Sie zuerst die Einstellung „Maximale Ausgabelänge“ und anschließend die Kapazitätsgrenze des ausgewählten Modells.
3. Der Chat ist fehlgeschlagen, obwohl die API-Schlüssel-Seite normal angezeigt wird: Stellen Sie sicher, dass das Kontingent Ihres Schlüssels nicht erschöpft ist und das Modell mit Ihrem Schlüssel verwendet werden kann.

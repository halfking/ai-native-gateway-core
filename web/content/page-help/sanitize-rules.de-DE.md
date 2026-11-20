# Datenhinweise zur Seite „Maskierungsregeln“

Diese Seite verwaltet die Regelbibliothek für den „Abfangfilter für sensible Informationen“: Sie legt fest, welche Muster in an das Modell gesendeten Inhalten ersetzt oder maskiert werden (Mobiltelefonnummern, Ausweisnummern, Schlüsselzeichenfolgen usw.).

## Zwei Tabs

- **Regeln**: Liste der Regeln (jede Regel definiert, wie ein sensitives Muster erkannt und ersetzt wird).
- **Treffer und Überprüfung**: Tatsächliche Abfangprotokolle – welche Regel bei welcher Anfrage an welcher Stelle ausgelöst und womit der Treffer ersetzt wurde.

## Wirkungslogik der Regeln

- Jede Regel kann je nach „Ausführungsmodus“ einzeln ein- oder ausgeschaltet werden; die globale Einstellung „Aktiviert“ ist der Hauptschalter – wenn dieser aus ist, wird keine Regel angewendet.
- Änderungen an Regeln werden **sofort wirksam** für nachfolgende Anfragen; bereits gesendete historische Anfragen werden nicht rückwirkend berücksichtigt.

## Zählweise der Trefferprotokolle

- Eine Anfrage kann mehrere Regeln auslösen; für jeden Treffer wird ein eigener Eintrag erfasst.
- Die „Überprüfung“ ist ein manueller Bestätigungsschritt: Dabei wird bestätigt, ob eine Ersetzung angemessen ist oder ein Fehlalarm vorliegt. Sie dient der laufenden Anpassung der Regeln und wirkt sich nicht auf bereits abgeschlossene Anfragen aus.

## Selbstprüfung bei Abweichungen bei den Zahlen

1. Obwohl sensible Informationen vorhanden sind, wurden sie nicht abgefangen: Prüfen Sie zuerst den Hauptschalter und den Regelschalter für den jeweiligen Modus und danach, ob der Modus der Regel und die tatsächliche Übereinstimmung zusammenpassen (einige Regeln erkennen nur bestimmte Formate).
2. Die Anzahl der Treffer ist höher als erwartet: Mehrere Übereinstimmungen an verschiedenen Stellen eines Inhalts werden einzeln gezählt; filtern Sie nach der Dimension „Anfrage“, um die Anzahl der eindeutigen Anfragen zu sehen.
3. Es gibt viele Fehlalarme: Aggregieren Sie in „Treffer und Überprüfung“ die Fehlalarme nach Regeln, um zu erkennen, bei welcher Regel sie sich konzentrieren, und passen Sie die Übereinstimmungsbedingungen dieser Regel an, statt den Hauptschalter global auszuschalten.

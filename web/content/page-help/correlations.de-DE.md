# Datenbeschreibung für die Korrelationsanalyse

Diese Seite beantwortet die Frage „Womit hängen Fehler zusammen“: Die verschiedenen Anfragemerkmale (Modell, Länge, Anbieter) werden mit den Fehlerausgaben kreuztabelliert, um Kombinationen zu finden, in denen sich die Fehler konzentrieren.

## Grafik richtig lesen

- Jede Zelle der Kreuztabelle zeigt die Anzahl der Anfragen und die Fehlerrate dieser Kombination; je dunkler die Zelle, desto stärker konzentrieren sich die Fehler.
- „Korrelation“ bedeutet nicht „Kausalität“: Die Seite zeigt statistische Zusammenhänge. Wenn A und B gemeinsam auftreten, bedeutet das nicht, dass A B verursacht.

## Erläuterung der Berechnungsgrundlagen

- Fehlerrate = Anzahl der fehlgeschlagenen Anfragen in der Kombination ÷ Gesamtzahl der Anfragen in der Kombination; Kombinationen mit einer zu kleinen Stichprobe (unterhalb des Schwellenwerts) werden gekennzeichnet, um irreführende Schlüsse zu vermeiden.
- Das Zeitfenster und die Filter bestimmen den statistischen Umfang. Prüfen Sie vor dem Vergleich zweier Zahlengruppen, ob das Zeitfenster identisch ist.

## Selbstprüfung bei abweichenden Zahlen

1. In einer Zelle beträgt die Fehlerrate 100%, es gibt aber nur 2 Anfragen: Die Werte schwanken bei einer kleinen Stichprobe; die statistische Signifikanz ist entsprechend dem Hinweis auf der Seite nicht zu berücksichtigen.
2. Abweichung von der Fehlerzahl auf der Seite zur Überwachung der Zugangsdaten: Diese Seite aggregiert nach „Kombination der Anfragemerkmale“, die Zugangsdaten-Seite nach „Zugangsdaten“; die Gruppierungsdimensionen unterscheiden sich.
3. Nach dem Filtern ist eine Zelle leer: Für die Kombination gibt es im aktuellen Filter keine Stichprobe. Das bedeutet „keine Daten“ und nicht „null Fehler“.

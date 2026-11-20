# Datenhinweise zur Routing-Übersicht

Diese Seite beantwortet die Frage »Warum wurde eine Anfrage einem bestimmten Modell zugeordnet«: Der Routing-Ablauf wird hier in einzelne Schritte zerlegt.

## Kernkonzepte

- **Kandidat** = Zugangsdaten, die nach den Kriterien als Empfänger für diese Anfrage infrage kommen.
- **Routingfähig** = der Teil der Kandidaten, der derzeit tatsächlich verwendet werden kann (gesund, nicht deaktiviert, nicht in der Abkühlphase).
- **Entscheidungsprotokoll** = der Auswahlverlauf für jede tatsächliche Anfrage.

## Statistische Abgrenzung

- Die »Anzahl der routingfähigen Kandidaten« wird zum aktuellen Zeitpunkt ermittelt: Ein Kandidat kann vor einer Minute verfügbar gewesen sein und eine Minute später in die Abkühlphase geraten; die Zahl ändert sich entsprechend.
- Die Entscheidungsdetails zeigen die Reihenfolge der Kandidaten nach »Ebene«: Kandidaten weiter vorne nehmen die Anfrage zuerst an; wenn ein vorderer Kandidat fehlschlägt, wird auf den nächsten in der Reihenfolge zurückgegriffen.
- »Fallback« = Nachdem der bevorzugte Kandidat fehlschlagen ist, übernimmt der nächste Kandidat; pro Anfrage gibt es höchstens einen endgültigen Erfolgseintrag.

## Erläuterung der Tabs

- Live-Entscheidungen: Starten Sie einen Testlauf, um zu sehen, »wie eine Anfrage derzeit ausgewählt würde«.
- Überwachung der Zugangsdaten / Verfügbarkeitsprüfung: Details zur Verfügbarkeit der Upstream-Zugangsdaten.
- Automatisierter Betrieb: Das System passt Parameter automatisch an und stellt Analysetools bereit.

## Selbstprüfung bei Zahlenabweichungen

1. Testlauf und tatsächliche Protokolle stimmen nicht überein: Der Testlauf ist eine »Projektion des aktuellen Zustands«, die tatsächliche Entscheidung liegt jedoch in der Vergangenheit; Abkühlstatus und vorübergehende Deaktivierung können zu Abweichungen führen.
2. Die Anzahl der Entscheidungen stimmt nicht mit der Anfrageseite überein: Die Entscheidungsseite erfasst nur Routing-Ereignisse; Anfragen, die nicht in das Routing gelangen (z. B. bereits vor dem Routing abgefangen wurden), erscheinen hier nicht.
3. »Routingfähig = 0«, die Anfrage war dennoch erfolgreich: Das System hat einen anbieterübergreifenden Fallback oder einen Reservekanal genutzt; die Fallback-Kette ist in den Entscheidungsdetails sichtbar.
4. Zeitversatz: Die Freigabe aus der Abkühlphase und die Wiederherstellung von Zugangsdaten erfolgen im Minutentakt; wenn die beiden Seiten wenige Minuten auseinander abgefragt werden, können die Ergebnisse abweichen.

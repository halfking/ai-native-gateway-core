## Berichtsübergreifende Kennzahlen und Abstimmung

Gleichen Sie vor dem Vergleich von Berichten den Zeitraum und die Zeitzone, den Mandanten, den Datenverkehrsumfang, die Zähleinheit und die Aktualität der Daten ab. Standardzeiträume schließen den Anfang ein und das Ende aus. Die Hinweise oberhalb der Seite beschreiben den aktuellen Bericht. Einige Berichte verwenden weiterhin andere Zeitfenster oder Datenquellen.

| Kennzahl | Standardberechnung |
| --- | --- |
| Clientanfragen | Zählen Sie jede am Gateway erfasste Clientanfrage genau einmal. Abgeschlossene Anfragen umfassen Erfolge, Fehler, Ratenbegrenzungen und endgültige Abbrüche. Anfragen in Bearbeitung werden separat erfasst. Wiederholungen und Anbieterwechsel ändern die ursprüngliche Anfragezahl nicht. |
| Anfragen an Anbieter | Zählen Sie jeden an einen Anbieter gesendeten Aufruf, einschließlich Wiederholungen, paralleler Versuche und automatisierter Plattformaufrufe. Eine Ablehnung durch das Gateway vor dem Senden zählt nur als Clientanfrage. Aktuelle Tages-Snapshots fassen endgültige Gateway-Datensätze zusammen. Die vollständige Erfassung jedes Versuchs steht noch aus. |
| Aktive und neue Sitzungen | Zählen Sie unterschiedliche Sitzungen mit Clientanfragen im Zeitraum als aktiv. Eine Sitzung gilt als neu, wenn ihre erste Anfrage in den Zeitraum fällt. Anfragen ohne Sitzung bleiben in den Anfragegesamtzahlen enthalten. |
| Erfolgsrate | Erfolge geteilt durch abgeschlossene Ereignisse, getrennt berechnet für Clientanfragen und Anbieteraufrufe. Bei einem Nenner von null liegen keine Daten vor. |
| Eingabe-, Ausgabe- und Gesamttoken | Summieren Sie normalisierte Eingabe- und Ausgabetoken getrennt. Die Gesamttoken entsprechen der Summe aus Eingabe und Ausgabe. Cache- und Reasoning-Details werden separat ausgewiesen, damit sie nicht doppelt gezählt werden. |
| Cache-Tokens und Cache-Trefferraten | Summieren Sie Cache-Lese- und Schreibvorgänge getrennt. Die Token-Trefferrate teilt die Cache-Lese-Tokens durch alle Eingabe-Tokens, einschließlich gecachter Eingaben, sofern vergleichbare Cache-Daten vorliegen. Die Anfragen-Trefferrate teilt die Treffer durch die Anfragen mit vergleichbaren Cache-Daten. Nenner und Abdeckung sind anzugeben. |
| Geschätzte Anbieterkosten | Berechnen Sie jeden Aufruf anhand der abrechenbaren Nutzung, der zum jeweiligen Zeitpunkt geltenden Anbieter-Kostensätze und ihrer Währung. Fehlende Kostensätze oder Nutzungsdaten werden separat erfasst. |
| Vom Anbieter gemeldeter Betrag | Erfassen Sie den Originalbetrag und die Währung, wenn die Antwort diese ausdrücklich enthält. Stimmen Sie den endgültig zu zahlenden Betrag anhand der Anbieterrechnung ab. |
| Interne Credits und Betrag | Summieren Sie gebuchte Belastungen, Rückbuchungen und Anpassungen. Einheit und geltende Umrechnungsregeln bleiben erhalten. Credits und Währung werden getrennt angezeigt. |
| Plattformautomatisierung | Ordnen Sie Prüfaufrufe und Selbsttests einem dedizierten Client zu. Berücksichtigen Sie sie bei Anbieterkosten und Plattformnutzung, schließen Sie sie jedoch von Mandantenbelastungen aus. Die Seitenhinweise zeigen den aktuellen Umfang des Datenverkehrs. |

Null steht für einen bekannten Wert. Fehlende Nutzungs- oder Preisdaten, nicht zugeordnete Einträge, nicht gebuchte Belastungen und verzögerte Daten sind zu kennzeichnen. Vergleichen Sie Beträge mit derselben Währung und Genauigkeit.

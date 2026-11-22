# Berechnungsregeln für Board und Sitzungsstatistik

Die Tabs „Board" und „Sitzungsstatistik" fassen denselben Strom von Modellaufrufen unter **zwei verschiedenen Berechnungsregeln** zusammen. Diese Seite beschreibt, wie jede Zahl der beiden Tabs berechnet wird, woher sie stammt und welches Zeitfenster sie abdeckt — samt Gegenüberstellung, warum sich die Zahlen unterscheiden. Zweck: Daten abgleichen und Abweichungen selbst prüfen.

> Messgrundlage: 2026-11-23 02:27 (Asia/Shanghai) im Produktivbetrieb gemessen und gegen die Produktionsdatenbank (nur lesend) abgeglichen.

## Wie die beiden Tabs zusammenhängen

| | Board | Sitzungsstatistik |
| --- | --- | --- |
| Zähleinheit | Ein Modellaufruf zählt als eine Zeile (**auf Anfrage-Ebene**) | Eine Sitzung zählt als eine Zeile mit den **kumulierten Werten** dieser Sitzung (**auf Sitzungs-Ebene**) |
| Quelle der Wahrheit | Anfrage-Faktentabelle `usage_facts` (Redis-Cache-Baseline + Live-Differenz) | Sitzungsübersicht `session_summaries` (Live-Abfrage) + materialisierte Sichten (Cache-Fläche) |
| Zeitfenster | Kalendertage in der Systemzeitzone: Mitternacht von heute-(N-1) bis jetzt | Panel = rollierende N Tage (erste Anfrage in den letzten N Tagen); Abgleichzeile = ganze Shanghai-Kalendertage |
| Probe-Verkehr | Ausgeschlossen über die Verkehrsklasse der Anfrage-Faktentabelle | Ausgeschlossen über die Sitzungs-Regeln der „Sitzungsuniversum"-Seite |

**Fazit vorab**: Die Zahlen der beiden Tabs **sollen nicht gleich sein** — andere Zähleinheit (Anfrage vs. Sitzung), anderes Zeitfenster, andere Ausschlussregeln. Ein tabellenübergreifender Vergleich muss diese drei Punkte zuerst angleichen; einfaches Subtrahieren führt in die Irre.

## Board: wie jede Zahl entsteht

| Karte / Modul | Berechnung | Hinweis |
| --- | --- | --- |
| Gesamt-Anfragen | Anzahl der Endzeilen (erfolgreich / fehlgeschlagen / ratenbegrenzt) im Fenster | Nur Endstatus-Zeilen; laufende Streams sind nicht enthalten |
| Erfolgsquote | erfolgreiche Zeilen ÷ Gesamtzeilen | Anteil der Anfragen mit Antwort |
| Token gesamt | Eingabe- und Ausgabe-Token je Anfrage summiert | Cache-Treffer zählen nach der jeweiligen Anbieter-Definition |
| Kosten gesamt | Kosten je Anfrage summiert (US-Dollar) | US-Dollar sind die Kosten gegenüber dem Upstream-Anbieter |
| Credits gesamt | Credits, die die Plattform den Mandanten berechnet | ★ siehe „Die Credit-Karte" unten |
| Ø Latenz | Summe der Latenzen ÷ Gesamtzeilen | Millisekunden |
| Aktive Modelle / Anbieter | Anzahl der in der Zeit tatsächlich aktiven Modelle/Anbieter | Nicht die „insgesamt verfügbaren" |
| Aktive Schlüssel | Anzahl der derzeit aktivierten Schlüssel | **Konfigurationsbasiert, unabhängig vom Zeitfenster** |
| Fehlerzahl | Summe je Fehlerklasse aus der Fehlerverteilung; ohne diese Schätzung über „Gesamt-Anfragen × (1 − Erfolgsquote)" | Die Schätzung kann um ein paar Zeilen abweichen |
| Kreisdiagramme (Modell / Anbieter / Mandant / Client…) | Anfragen, Token, Kosten je Dimension gruppiert | Aus der Minutentabelle — nicht dieselbe Quelle wie die Karten oben |
| Nutzungstrend | Anfragen, Token, Kosten, Credits je Zeitbucket | Ebenfalls aus der Minutentabelle |

### Wie das Zeitfenster berechnet wird

- „Heute" = Mitternacht heute in der Systemzeitzone (Standard Asia/Shanghai) bis jetzt; „letzte 7 Tage" = Mitternacht vor 6 Tagen bis jetzt; „letzte 30 Tage" ebenso. **Der Start ist eine Kalendertages-Mitternacht, das Ende ist der aktuelle Moment** — kein ganzer Tag.
- Ein freier Bereich aggregiert die beiden gewählten Tage einschließlich des vollen letzten Tages.
- Die Seite aktualisiert sich alle 10 Sekunden; bei aktivem Live-Kanal werden neu abgeschlossene Anfragen sofort addiert.

### Die Credit-Karte (besondere Regel — bitte beachten)

Die Credit-Karte zeigt **nicht** die im gewählten Fenster verbrauchten Credits: Die Credit-Spalte der Anfrage-Faktentabelle ist derzeit immer 0 (gemessen: alle 298.037 Zeilen sind 0), angezeigt wird die **Live-Differenz nach dem Cache-Stand**. Daher gilt:

- Der Wechsel zwischen „Heute / letzte 7 Tage / letzte 30 Tage" lässt die Credit-Karte **unverändert** (gemessen: alle drei 86.925).
- Der Nutzungstrend derselben Seite führt eine eigene Credit-Summe (Minutentabelle, 7 Tage 1.767.183), die nicht der Zahl der Karte entspricht.

Für ein belastbares Credit-Konto nutzen Sie die Seiten „Nutzung & Kosten" und „Abgleich"; die Credit-Karte des Boards taugt nicht für Zeitreihenvergleiche.

## Sitzungsstatistik: wie jede Zahl entsteht

| Modul | Berechnung | Hinweis |
| --- | --- | --- |
| Sitzungen gesamt | Sitzungen, deren erste Anfrage im Fenster liegt | Zählung auf Sitzungs-Ebene, keine Anfragenzahl |
| Aktive / neue / geschlossene Sitzungen | Klassifiziert danach, ob letzte bzw. erste Anfrage in den letzten 24 Stunden liegt | 24-Stunden-Regel, unabhängig vom Hauptfenster |
| Kosten gesamt (USD) | Summe der kumulierten Kosten je Sitzung | Die **gesamten** Kosten einer Sitzung stehen in der Sitzungszeile |
| Anfragen gesamt | Summe der kumulierten Anfragenzahl je Sitzung | Ebenfalls der kumulierte Sitzungswert, nicht die im Fenster angefallenen Anfragen |
| Eingangs-/Ausgangskosten | Summe der Eingangs-/Ausgangskosten je Sitzung | Beide zusammen ergeben die Gesamtkosten |
| Ø Kosten je Sitzung / je Anfrage | Gesamtkosten ÷ Sitzungen (÷ Anfragen) | Der Nenner ist der kumulierte Sitzungswert |
| Erfolgs- / Cache-Quote (Abgleichzeile) | Erfolgreiche Zeilen ÷ Gesamtanfragen der Anfrage-Ebene | Nur auf Anfrage-Ebene vorhanden |
| Abgleichzeile der Sitzungsanalyse | Materialisierte Sitzungssicht | Aktualisierung alle 15 Minuten; Fenster = ganze Shanghai-Kalendertage |
| Top 5 Nutzer / Top 5 Anbieter | Ranking-Endpunkte der Sitzungsanalyse (Nutzer / Modelle / Anbieter / Aufgaben) | Der Anbieter wird per SQL über `model_usage_aggregates.provider` aggregiert (nicht aus dem Modellnamenspräfix abgeleitet); **Kosten sind enthalten** und eine Sortierung nach Kosten ist möglich; die vier Top-5-Dimensionen erscheinen unter Sitzungsanalyse → Nutzer |
| Turn-Abgleich | Anfragenzahl der Sitzungen vs. Turnzahl | Bei mehr als 10 % Abweichung zeigt die Seite einen Hinweis |

### Das Sitzungsuniversum (welche Sitzungen gezählt werden)

Eine Sitzung zählt nur, wenn alle drei Bedingungen zutreffen:

1. Sie ist in der Sitzungstabelle registriert;
2. Sie hat einen Besitzer (Benutzerschlüssel oder API-Key-Besitzerschlüssel; sind beide leer, zählt sie nicht);
3. Sie ist keine Probe-Sitzung: weder manuell als Probe markiert noch per Muster „Besitzer / Client-Typ / Titel" als Self-Check, Deployment-Gate, Test oder Verifikation erkannt.

### Warum die Kosten hier kleiner sind als im Board

Regel 3 des Sitzungsuniversums wirft **echte Geschäftssitzungen** weg, die als Probe eingestuft werden. Messung in der Produktionsdatenbank (gleiches 7-Tage-Fenster, 2026-11-23):

- Die 15.898 vom Sitzungs-Teil ausgeschlossenen Sitzungen haben ihre Hauptanfrage in der Anfrage-Faktentabelle zu **100 % als Geschäftsverkehr** eingestuft, zusammen $6.452,73;
- im selben Zeitfenster summiert sich der in der Anfrage-Faktentabelle tatsächlich als Probe klassifizierte Verkehr auf nur **$1,81**.

Beide Tabs behaupten also, Probe-Verkehr auszuschließen, **schließen aber nicht dieselben Anfragen aus**. Das ist der Hauptgrund, warum die Kosten der Sitzungsstatistik deutlich unter denen des Boards liegen.

## Gegenüberstellung der Zahlen (gemessen 2026-11-23, 7-Tage-Fenster)

| Kennzahl | Board | Sitzungsstatistik (Panel) | Sitzungsstatistik (Abgleichzeile) |
| --- | --- | --- | --- |
| Zähleinheit | Anfragen | Sitzungen + kumulierte Sitzungsanfragen | Sitzungen / Anfragen |
| Anfragen | 65.774 | 72.098 | — |
| Kosten (USD) | 8.013,93 | 2.717,62 | 2.468,73 (interne Kosten) |
| Credits | 86.925 (fensterunabhängig) | — | 0 |
| Token | 3.245.804.009 | — | — |
| Erfolgsquote | 95,31 % | — | 94,64 % |
| Sitzungen | — | 19.235 | 17.390 |

Zuordnung der Abweichungen:

- **Anfragen 72.098 > 65.774**: Die Anfragenzahl der Sitzungsseite ist die kumulierte Anfragenzahl der gewählten Sitzungen; eine Sitzung, die das Fenster überschreitet, bringt ihre Anfragen nach dem Fensterende vollständig mit. Zusätzlich enthält die Sitzungstabelle doppelte Zeilen (gemessen: bis zu 30 gleiche Schlüssel), die mit aggregiert werden.
- **Kosten 2.717,62 < 8.013,93**: Das Sitzungsuniversum entfernt die oben genannten $6.452,73 Geschäftsverkehr. Hinzu kommt, dass das Board auch Aufrufe **ohne Sitzungsbezug** zählt (direkte API-Aufrufe, Skriptaufrufe) — gemessen rund ein Fünftel der Board-Kosten.
- **Credits 0 vs. 86.925**: Weder die Sitzungsseite noch die Anfrage-Faktentabelle halten derzeit einen belastbaren Credit-Bestand; der angezeigte Wert stammt aus der Live-Differenz.
- **Erfolgsquote 95,31 % vs. 94,64 %**: beides Anfrage-Ebene, aber einmal aus `usage_facts`, einmal aus der materialisierten Anfrage-Log-Sicht — und mit unterschiedlichen Fenstern.

## Was tun, wenn die Zahlen nicht zusammenpassen

1. **Zuerst das Fenster angleichen**: Board = ab Kalendertages-Mitternacht; Sitzungsstatistik-Panel = rollierende N Tage; Abgleichzeile = ganze Shanghai-Kalendertage. Auch bei gleicher Voreinstellung sind das drei verschiedene Fenster.
2. **Dann die Zähleinheit angleichen**: Anfragen mit Anfragen, Kosten mit Kosten vergleichen; Sitzungszahl nicht gegen Anfragenzahl stellen.
3. **Auf Degradierungsmarkierungen achten**: Zeigt eine Karte „degradiert / nicht vertrauenswürdig / nicht berechenbar", bedeuten diese Nullen nicht „kein Verkehr".
4. **Den Probe-Schalter prüfen**: „Probes ausschließen / einschließen" im Board wirkt nur auf die Kopfsumme; Verteilungs- und Trenddiagramm bleiben immer auf Geschäftsgrundlage.
5. **„Heute" konnte fehlschlagen (behoben am 2026-11-23)**: Die Voreinstellung „Heute" der Sitzungsstatistik lief reproduzierbar in ein 30-Sekunden-Timeout, weil auf der Datenbankseite der Join-Index fehlte (Index am 2026-11-23 ergänzt: Abfragezeit von 121,8 s auf 0,19 s). Zeigt die Seite den Fehler wieder an, ist der Index erneut verschwunden — dann bitte den Bereitschaftsdienst prüfen lassen, ob `idx_session_dim_session_key` vorhanden ist.

## Bekannte Einschränkungen (gemessen 2026-11-23)

1. **Kosten und Anfragenzahl der Sitzungsseite sind zu hoch (Doppelzählung, am 2026-11-23 auf `main` behoben)**: Kosten und Anfragenzahl der Sitzungsstatistik stammen aus der Aggregation der Sitzungssummen; `sessions` ist jedoch eine **nach Tagen partitionierte Sitzungshistorie** (dieselbe Session bekommt jeden Tag eine neue Zeile), und der Join der Statistik dedupliziert nicht nach Sitzung ⇒ dieselbe Session wird mehrfach gezählt. Messung auf der Produktionsdatenbank vom 2026-11-23 (gleiches 7-Tage-Fenster, **Werte vor der Behebung**): Die Kosten der Sitzungsseite liegen rund **61 %** und die Anfragenzahl rund **36 %** über den nach Sitzung deduplizierten Werten (die teuerste Session wurde dreimal gezählt). **Diese Doppelzählung ist behoben**: der Join der Sitzungsuniversum dedupliziert nun nach `(session_id, tenant_id)` (`DISTINCT ON`), die drei materialisierten Sichten wurden mit Migration 994 nachgezogen ⇒ die **61 %／36 %** oben sind **Messwerte vor der Behebung**. **Die weiterhin bestehende bekannte Einschränkung ist D1 (zu aggressive Probe-Ausschließung, orthogonal zu diesem Punkt)**; **die Board-Zahlen (auf Anfrage-Ebene) waren nie betroffen** ⇒ beim Kostenvergleich das Board heranziehen.
2. Die Credit-Karte des Boards ändert sich nicht mit dem Zeitfenster (siehe oben).
3. Trenddiagramm (Minutentabelle) und Karten (Anfrage-Faktentabelle) des Boards unterscheiden sich im 30-Tage-Fenster um rund 13,8 % (160.451 vs. 140.969 Anfragen).
4. Der Probe-Ausschluss entfernt auf beiden Seiten nicht denselben Verkehr (siehe oben).

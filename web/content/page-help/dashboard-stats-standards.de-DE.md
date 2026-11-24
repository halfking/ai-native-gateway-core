# Berechnungsregeln für Board und Sitzungsstatistik

Die Tabs „Board", „Sitzungsstatistik" und „Anbieter-Abgleich" fassen denselben Strom von Modellaufrufen unter **drei verschiedenen Berechnungsregeln** zusammen. Diese Seite beschreibt, wie jede Zahl berechnet wird, woher die Daten stammen und welches Zeitfenster sie abdeckt — samt **Aufschlüsselung nach Positionen** darüber, warum sich die Zahlen der drei Module nicht decken. Zweck: Daten abgleichen und Abweichungen selbst prüfen.

> Messgrundlage: 2026-11-23 21:05 (Asia/Shanghai) im Produktivbetrieb gemessen. Alle Zahlen stammen aus **demselben Zeitpunkt** (innerhalb einer einzigen SQL-Abfrage), Fenster = `NOW()-7 Tage`; die Seite „Anbieter-Abgleich" wurde zusätzlich über den geschlossenen Kalendertagsbereich `2026-11-17 ~ 2026-11-23` gemessen und taggenau gegen die Faktentabelle abgeglichen.
>
> ★ Korrektur (Runde „Datumsbereich angleichen", 2026-11-23): Der Tab „Sitzungsstatistik" verwendet jetzt **denselben** Zeitbereich wie der Tab „Board" (gleiche Steuerung, gleicher Kalendertagsbereich) ⇒ die unten aufgeführten Werte der Sitzungsstatistik stammen noch aus dem **nicht angeglichenen** (rollierenden) Fenster und müssen im neuen Fenster neu gemessen werden; die Werte von Board und Anbieter-Abgleich sind nicht betroffen.
> ★★ Korrektur (Runde „Wahrheitsquelle vereinheitlichen", 2026-11-24): ★★ Die Wahrheitsquelle des Boards wurde von der Anfrage-Faktentabelle `usage_facts` **vollständig auf die Anfrage-Logs `request_logs_with_current_month` umgestellt** — Hero-Karten, Nutzungstrend und Pie-Charts lesen nun **dieselbe View, dieselbe Endstatus-Menge und dieselbe Probe-Bedingung** ⇒ ein Universum für die ganze Seite, und lange Fenster (letzte 30 Tage) zerlegen die Seite nicht mehr in zwei Zahlen (Karte und Pie-Charts unterschieden sich um das Doppelte). ⇒ **die Algorithmen- und Quellenbeschreibungen auf dieser Seite sind auf das neue Kaliber korrigiert**; die **gemessenen Board-Zahlen unten stammen aber weiterhin aus der Messung vom 2026-11-23 (Kaliber vor der Umstellung)** und müssen im neuen Kaliber neu gemessen werden (als offene Aufgabe vermerkt). Die Seite „Anbieter-Abgleich" liest `report_snapshots` / `usage_facts` und ist daher **nicht mehr garantiert ziffernidentisch mit dem Board** — siehe Einschränkung 7 am Ende dieser Seite.

## Wie die drei Module zusammenhängen

| | Board | Sitzungsstatistik | Anbieter-Abgleich |
| --- | --- | --- | --- |
| Zähleinheit | Ein Modellaufruf zählt als eine Zeile (**auf Anfrage-Ebene**) | Eine Sitzung zählt als eine Zeile mit den **kumulierten Werten** dieser Sitzung (**auf Sitzungs-Ebene**) | Ein Modellaufruf zählt als eine Zeile (**auf Anfrage-Ebene**, je Anbieter aggregiert) |
| Quelle der Wahrheit | Anfrage-Logs `request_logs_with_current_month` | Sitzungsübersicht `session_summaries` + Sitzungsuniversum | Tagessnapshot-Tabelle `report_snapshots` |
| Zeitfenster | Kalendertage in der Systemzeitzone: Mitternacht von heute-(N-1) bis jetzt | **Derselbe Bereich wie das Board** (Kalendertage in der Systemzeitzone: Mitternacht von heute-(N-1) bis Ende des letzten Tages; freier Start/Ende wählbar) | **Geschlossener Kalendertagsbereich** (ganzer Tag, inklusive des vollen letzten Tages) |
| Probe-Verkehr | Ausgeschlossen über die Probe-Marker in den Anfrage-Logs (umschaltbar) | Ausgeschlossen über die Regeln des **Sitzungsuniversums** | **Nicht ausgeschlossen** (gesamter Verkehr, einschließlich „Nicht zugeordnet") |
| Reifegrad | Echtzeit | Echtzeit | **T+1-Snapshot** (Tage, die noch nicht abgeschlossen sind, hinken hinterher) |

**Fazit vorab**: Die Zahlen der drei Module **sollen nicht gleich sein** — andere Zähleinheit, anderes Zeitfenster, andere Probe-Ausschlussregeln, anderer Reifegrad. Ein modulübergreifender Vergleich muss diese vier Punkte zuerst angleichen; einfaches Subtrahieren führt in die Irre.

### Das Wichtigste: Probe-Verkehr ist praktisch kostenlos

Probe-Verkehr (Health-Check / Selbsttest) steht in der Faktentabelle separat als `traffic_class='probe'`:

| Berechnungsgrundlage (gleiches Fenster) | Anfragen | Kosten |
| --- | --- | --- |
| Ohne Probes | 81.265 | $8.854,70 |
| Mit Probes | 189.534 | $8.856,87 |
| **Probe-Anteil** | **108.271** | **$2,17** |

⇒ **Die Anfragenzahlen unterscheiden sich um den Faktor 2,3, die Kosten aber nur um $2,17.** Wer „viel mehr Anfragen, aber ähnlich hohe Kosten" sieht, sollte zuerst die Probe-Berechnungsgrundlage prüfen — nicht die Datenrechnung.

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
| Kreisdiagramme (Modell / Anbieter / Mandant / Client…) | Anfragen, Token, Kosten je Dimension gruppiert | Aus dem Anfrage-Log — dieselbe Quelle wie die Karten oben (gleicher Probe-Umfang) |
| Nutzungstrend | Anfragen, Token, Kosten, Credits je Zeitbucket | **Dieselbe Quelle wie die Karten oben**: dieselben Anfrage-Logs `request_logs_with_current_month`, dieselbe Probe-Bedingung und dieselbe Endstatus-Menge ⇒ die Buckets summieren sich zur Karte |

### Wie das Zeitfenster berechnet wird

- „Heute" = Mitternacht heute in der Systemzeitzone (Standard Asia/Shanghai) bis jetzt; „letzte 7 Tage" = Mitternacht vor 6 Tagen bis jetzt; „letzte 30 Tage" ebenso. **Der Start ist eine Kalendertages-Mitternacht, das Ende ist der aktuelle Moment** — kein ganzer Tag.
- Ein freier Bereich aggregiert die beiden gewählten Tage einschließlich des vollen letzten Tages.
- Die Seite aktualisiert sich alle 10 Sekunden; bei aktivem Live-Kanal werden neu abgeschlossene Anfragen sofort addiert.

### Die Credit-Karte (besondere Regel — bitte beachten)

Die Credit-Karte zeigt **nicht** die im gewählten Fenster verbrauchten Credits: Die Credit-Spalte der Anfrage-Faktentabelle ist derzeit immer 0 (gemessen: alle 298.037 Zeilen sind 0), angezeigt wird die **Live-Differenz nach dem Cache-Stand**. Daher gilt:

- Der Wechsel zwischen „Heute / letzte 7 Tage / letzte 30 Tage" lässt die Credit-Karte **unverändert** (gemessen: alle drei 86.925).
- Nutzungstrend und Credit-Karte derselben Seite stammen jetzt **aus derselben Quelle** (beide lesen die Anfrage-Logs `request_logs_with_current_month`) ⇒ die Credits der Buckets summieren sich zur Karte.

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

## Vollständige Aufschlüsselung der Kostendifferenz (gemessen 2026-11-23, gleicher Zeitpunkt)

Die 81.265 Anfragen / $8.854,70 des Boards (ohne Probes), **aufgeschlüsselt in jeden Block der Sitzungsstatistik** (die sechs Positionen unten ergeben zusammen die Board-Summe, die Kosten schließen auf den Cent genau):

| # | Bestandteil | Anfragen | Kosten (USD) | Zuordnung |
| --- | --- | --- | --- | --- |
| C | Im Sitzungsuniversum **beibehaltene** Sitzungen | 58.180 | 1.551,41 | Sitzungsstatistik (das ist der Block, den die Seite zeigt) |
| D1 | Ausgeschlossen · **manuell markierte Probes** | 12.364 | 5.145,16 | Lücke der Sitzungsstatistik |
| D2 | Ausgeschlossen · **per Muster erkannte Probes** | 5.946 | 1.876,67 | Lücke der Sitzungsstatistik |
| D3 | Ausgeschlossen · **ohne Besitzer** | 1.202 | 0,08 | Lücke der Sitzungsstatistik |
| E | **Sitzungen über das Fenster hinaus** (erste Anfrage außerhalb, im Fenster noch aktiv) | 3.191 | 281,38 | Lücke der Sitzungsstatistik |
| F | **Ohne Sitzungsbezug** (direkte API-Aufrufe, Skriptaufrufe) | 381 | 0,00 | Nur Board |
| | **Summe** | **81.263** | **8.854,70** | |

⇒ **Die Differenz zwischen Sitzungsstatistik und Board liegt nicht primär an den „Sitzungen über das Fenster hinaus", sondern an den Probe-Ausschlussregeln des Sitzungsuniversums (D1+D2).**

### Schlüsselmessung: Alles Ausgeschlossene ist echter Geschäftsverkehr

Die Anfragen der ausgeschlossenen D1/D2/D3-Sitzungen im Fenster, klassifiziert nach `origin_stage` im Anfrageprotokoll:

| Ausschlussgrund | Anfragen | Kosten (USD) | davon `origin_stage='business'` | Anteil |
| --- | --- | --- | --- | --- |
| Manuell markierte Probes | 12.364 | 5.145,16 | 12.364 | **100 %** |
| Per Muster erkannte Probes | 5.946 | 1.876,67 | 5.946 | **100 %** |
| Ohne Besitzer | 1.202 | 0,08 | 1.202 | **100 %** |

⇒ Die auf der Sitzungsseite ausgeschlossenen 19.512 Anfragen bzw. $7.021,91 Kosten sind im Anfrageprotokoll **zu 100 % Geschäftsverkehr (`origin_stage='business'`)** — keine einzige ist eine Probe. **Das ist der Hauptgrund, warum die Sitzungsstatistik unter dem Board liegt, und zugleich die derzeit größte Einzelabweichung.**

## Gegenüberstellung der Zahlen der drei Module (gemessen 2026-11-23)

| Kennzahl | Board (ohne Probes) | Sitzungsstatistik (Panel) | Anbieter-Abgleich |
| --- | --- | --- | --- |
| Zähleinheit | Anfragen | Sitzungen + kumulierte Sitzungsanfragen | Anfragen (je Anbieter aggregiert) |
| Anfragen | 81.265 | 58.180 (im Universum) / 77.687 (Gesamtmenge außerhalb des Universums) | 178.841 (einschließlich „Nicht zugeordnet") |
| Kosten (USD) | 8.854,70 | 1.551,41 (im Universum) | 8.612,07 |
| Probe-Verkehr | bereits ausgeschlossen | nach den Sitzungsregeln ausgeschlossen | **nicht ausgeschlossen** |
| Reifegrad | Echtzeit | Echtzeit | **T+1-Snapshot** |

### Board vs. Anbieter-Abgleich: taggenauer Abgleich

Die Abgleich-Seite liest `daily_total` aus `report_snapshots` und gleicht es taggenau gegen die Faktentabelle ab (geschlossener Kalendertagsbereich 11/17 ~ 11/23):

| Datum | Anfragen in der Faktentabelle | Anfragen im Abgleich-Snapshot | Differenz | Kosten-Differenz |
| --- | --- | --- | --- | --- |
| 11/17 | 27.446 | 27.446 | 0 | 0,0000 |
| 11/18 | 22.905 | 22.905 | 0 | 0,0000 |
| 11/19 | 35.008 | 35.008 | 0 | 0,0000 |
| 11/20 | 23.947 | 23.947 | 0 | 0,0000 |
| 11/21 | 21.341 | 21.341 | 0 | 0,0000 |
| 11/22 | 19.693 | 19.693 | 0 | 0,0000 |
| 11/23 | 36.019 | 28.501 | 7.518 | 42,38 |

⇒ **Die ersten 6 Tage stimmen anfrage- und centweise exakt überein**; die Differenz am 11/23 stammt zu 100 % vom Abschlusszeitpunkt des Tages-Snapshots (Snapshot `updated_at` = 17:38, Messzeitpunkt 21:05) — **kein Rechenfehler, sondern die normale Verzögerung eines noch nicht abgeschlossenen Tages**.

### Innerhalb des Abgleich-Moduls: keine Doppelzählung

Die Anbieterzeilen der Abgleich-Seite (einschließlich „Nicht zugeordnet") **summieren sich exakt auf die Gesamtsumme der Seite**:

- Summe der Anbieterzeilen = 178.841 Anfragen = die Gesamt-Anfragen der Seite ✓
- Summe der Kosten der Anbieterzeilen = $8.612,07 = die Gesamtkosten der Seite ✓
- Davon „Nicht zugeordnet" (`provider_id IS NULL`) = 79.801 Anfragen / 100 % Fehlschläge / $0,00 — das ist **Probe-Verkehr ohne Anbieterzuordnung** (Probes gehen nicht an den Upstream-Anbieter), keine fehlende Rechnung.

## Was tun, wenn die Zahlen nicht zusammenpassen

1. **Zuerst das Fenster angleichen**: Board und Sitzungsstatistik-Panel = **derselbe Kalendertagsbereich** (Mitternacht von heute-(N-1); das Board zählt bis jetzt, das Panel bis Ende des letzten Tages ⇒ beide decken dieselben Daten ab und nutzen dieselbe Zeitbereichs-Steuerung); Abgleichzeile = ganze Shanghai-Kalendertage; Anbieter-Abgleich = geschlossener Kalendertagsbereich. Die letzten beiden sind weiterhin nicht dasselbe Fenster wie die ersten beiden.
2. **Dann die Zähleinheit angleichen**: Anfragen mit Anfragen, Kosten mit Kosten vergleichen; Sitzungszahl nicht gegen Anfragenzahl stellen.
3. **Dann die Probe-Berechnungsgrundlage angleichen**: Das Board lässt sich auf „Probes einschließen" umstellen; die Sitzungsstatistik schließt nach den Regeln des Sitzungsuniversums aus; **der Abgleich enthält immer die Probes**. Weichen die Anfragenzahlen stark ab, während die Kosten ähnlich bleiben ⇒ zuerst die Probes prüfen.
4. **Dann den Reifegrad angleichen**: Der Abgleich ist ein T+1-Snapshot, **die Daten des laufenden Tages hinken zwingend hinterher**; vor dem Vergleich prüfen, bis zu welchem Tag der Snapshot reicht.
5. **Auf Degradierungsmarkierungen achten**: Zeigt eine Karte „degradiert / nicht vertrauenswürdig / nicht berechenbar", bedeuten diese Nullen nicht „kein Verkehr".
6. **Trenddiagramm und Karten des Boards stammen jetzt aus derselben Quelle**: beide lesen die Anfrage-Logs `request_logs_with_current_month` mit derselben Probe-Bedingung ⇒ die Buckets sollten sich zur Karte summieren. Wenn nicht: zuerst das `degraded`-Flag in der Nutzlast prüfen (bei Rückfall auf die voraggregierten Tabellen weicht der Probe-Umfang ab), dann die Zeitfenster abgleichen.
7. **„Heute" konnte fehlschlagen (behoben am 2026-11-23)**: Die Voreinstellung „Heute" der Sitzungsstatistik lief reproduzierbar in ein 30-Sekunden-Timeout, weil auf der Datenbankseite der Join-Index fehlte (Index am 2026-11-23 ergänzt: Abfragezeit von 121,8 s auf 0,19 s). Zeigt die Seite den Fehler wieder an, ist der Index erneut verschwunden — dann bitte den Bereitschaftsdienst prüfen lassen, ob `idx_session_dim_session_key` vorhanden ist.

## Bekannte Einschränkungen (gemessen 2026-11-23)

1. **Das Sitzungsuniversum schließt Probes zu aggressiv aus (D1/D2, am 2026-11-23 messtechnisch bestätigt)**: Die von der Sitzungsstatistik ausgeschlossenen 19.512 Anfragen bzw. $7.021,91 Kosten sind im Anfrageprotokoll **zu 100 % Geschäftsverkehr**. Die Sitzungsstatistik liegt damit rund **$7.021,91 (79 %)** unter dem Board (Berechnungsgrundlage ohne Probes). **Die Board-Zahlen (auf Anfrage-Ebene) waren nie betroffen** ⇒ für einen exakten Kostenabgleich das Board heranziehen oder die Abgleich-Seite vorher auf die Geschäftsgrundlage umstellen.
2. **Sitzungen über das Fenster hinaus (E)**: Bei 18 Sitzungen liegt die erste Anfrage vor dem Fenster, sie sind im Fenster aber noch aktiv und werden derzeit vollständig ausgeschlossen — das verschluckt 3.191 Anfragen / $281,38 (3,2 % der Board-Kosten).
3. **Aufrufe ohne Sitzungsbezug (F)**: 381 direkte API- / Skriptaufrufe haben keine Sitzung; das Board zählt sie, die Sitzungsstatistik nicht.
4. **Verzögerung des Abgleich-Snapshots am selben Tag**: Der Abgleich ist T+1, **die Daten des heutigen Tages sind unvollständig**; direkt von den Echtzeit-Zahlen des Boards abgezogen entsteht daher immer eine Differenz.
5. Die Credit-Karte des Boards ändert sich nicht mit dem Zeitfenster (siehe oben).
6. **Probe-Umfang im degradierten Zustand**: Wenn Pie-Charts und Trend des Boards nach einem Fehler des Hauptpfads auf die voraggregierten Tabellen zurückfallen, sind diese immer „ohne Sonden“ ⇒ im Modus „Sonden enthalten“ weichen sie vom Hero-Summary ab (die Nutzlast trägt dann ein `degraded`-Flag — es ist kein stiller Degradationsfall).
7. **Board und Anbieter-Abgleich sind nicht mehr garantiert ziffernidentisch (2026-11-24, Runde 2)**: Die Board-Wahrheitsquelle ist auf die Anfrage-Logs umgestellt, der Abgleich liest weiterhin `report_snapshots` / `usage_facts`. Für Zeiträume mit intakter Faktentabelle sind beide gleich; sobald das Fenster **vor 2026-11-10 13:38** zurückreicht, **unterschätzt** die Abgleichseite — die Faktentabelle verlor in diesem Moment ≈352.000 echte Geschäftsfakten (2026-10-12 ~ 2026-11-10; Beweiskette in der Audit-Hauptdatei §C), während die Anfrage-Logs diesen Verkehr weiterhin enthalten. ⇒ **Die Differenz zwischen Abgleich und Board ist über den unterschiedlichen historischen Startpunkt der Quellen zu erklären, nicht als Rechenfehler**; für buchhaltungsgenau Zahlen gilt die Abgleichseite (inklusive ihrer bekannten Tagesverzögerung).

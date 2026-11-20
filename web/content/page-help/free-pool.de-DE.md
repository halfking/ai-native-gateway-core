# Datenhinweise zur Seite „Kostenloser Ressourcenpool“

Diese Seite verwaltet den „Modellpool ohne Kosten“: Modelle aus den offiziellen oder Community-basierten kostenlosen Stufen der einzelnen Anbieter werden einheitlich in einem Pool gebündelt und als Fallback für die Routing-Abwicklung bereitgestellt.

## Drei Bereiche

- **Kostenlose Modelle im Pool**: Aktuelle Liste der tatsächlich verfügbaren kostenlosen Modelle (ein Modell gilt nur dann als „im Pool“, wenn nutzbare Zugangsdaten vorhanden sind).
- **Vorlagenverzeichnis**: Bekannte kostenlose Anbieter und die von ihnen erwarteten Modelle – „Verbunden“ bedeutet, dass aktive Zugangsdaten für den Anbieter im Pool vorhanden sind; nicht verbundene Anbieter sind lediglich als bekannt erfasst.
- **Zugangsdatenverwaltung**: Erfassung der Zugangsdaten, die ausschließlich für den kostenlosen Pool verwendet werden (verschlüsselte Speicherung), sowie deren Status.

## Routing-Priorität

- Bereits im offiziellen Verzeichnis registrierte Modellrouten haben **immer Vorrang** vor dem kostenlosen Pool; der kostenlose Pool dient ausschließlich als Fallback, „wenn alle offiziellen Kanäle nicht verfügbar sind“.
- Für Modelle im Pool bestehen keine SLA-Zusagen: Die kostenlose Stufe kann jederzeit vom Upstream-Anbieter gedrosselt oder eingestellt werden.

## Selbstprüfung bei abweichenden Zahlen

1. Ein Modell ist in der Vorlage vorhanden, fehlt aber in der „Pool-Liste“: Die Vorlage ist eine „Erwartungsliste“; für die tatsächliche Aufnahme ist der Status der Zugangsdaten entscheidend. Erst nach Ergänzung der Zugangsdaten wird das Modell in den Pool aufgenommen.
2. Aufrufe von Modellen im Pool schlagen weiterhin fehl: Kostenlose Stufen werden häufig gedrosselt; Fehler zählen als Zugangsdatenfehler, und nach aufeinanderfolgenden Fehlversuchen wird das Modell automatisch deaktiviert.
3. Unter „Kostenlos“ sind dennoch Kosten entstanden: Möglicherweise wurde durch das Routing-Fallback tatsächlich ein offizieller Kanal genutzt (der kostenlose Pool wurde nicht getroffen). Maßgeblich ist der tatsächliche Routingpfad in den Anfragedetails.

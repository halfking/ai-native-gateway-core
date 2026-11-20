# Datenerläuterung zur Seite „Systemeinstellungen“

Diese Seite ist die „zentrale Schalttafel“ der gesamten Plattform: Globale Konfigurationen wie Oberflächenmodus, Komprimierungsverhalten und Sicherheitsschalter werden hier verwaltet.

## Wie Konfigurationen wirksam werden

- Die meisten Einstellungen werden **beim Speichern sofort wirksam** (bei der nächsten Anfrage wird der neue Wert verwendet).
- Bei Elementen mit der Kennzeichnung „Neustart erforderlich“ muss der Dienst neu gestartet werden; die Seite weist bei solchen Elementen gesondert darauf hin.

## Einfach-/Expertenmodus

- „Einfach“: Auf der Startseite wird die Liste der Anbindungen angezeigt, und alle Funktionen sind in der Gruppe „Erweitert“ zusammengefasst – für Personen, die sie selten verwenden.
- „Experten“: Vollständige Navigation und alle Seiten.
- Dies ist lediglich ein Wechsel der **Darstellungsweise in der Benutzeroberfläche**: Beide Modi zeigen dieselben Daten und verwenden dieselbe Statistikbasis; die Zahlen unterscheiden sich nicht je nach Modus.

## Komprimierungsmodus (Umgang mit sehr langen Dialogen)

- Modus 1 (vorausschauend): Wenn das Kapazitätslimit des Modells annähernd erreicht ist, wird proaktiv komprimiert.
- Modus 2 (reaktiv, empfohlen): Nach dem Empfang des Fehlers „zu lang“ wird komprimiert und automatisch erneut versucht – eine Komprimierung erfolgt nur bei Bedarf, um Ressourcen zu sparen.
- Die Komprimierung wirkt sich nur auf die **Organisation der an das Modell gesendeten Inhalte** aus und löscht nicht den Originaltext des Dialogs; Statistik und Abrechnung beruhen auf den tatsächlich ausgeführten Aufrufen.

## Abweichungen bei Zahlen selbst prüfen

1. Sie haben Einstellungen geändert, das Verhalten hat sich aber nicht geändert: Prüfen Sie zuerst, ob das Element mit „Neustart erforderlich“ gekennzeichnet ist, und vergewissern Sie sich anschließend, ob nach dem Speichern eine Erfolgsmeldung angezeigt wurde.
2. In den beiden Modi sind unterschiedliche Zahlen zu sehen: Das ist nicht möglich – wenn Sie dies beobachten, unterscheiden sich das Zeitfenster oder die Filter. Gleichen Sie zunächst das Zeitfenster ab.
3. Ein Schalter ist aktiviert, aber einige Anfragen verwenden ihn dennoch nicht: Manche Schalter wirken je Mandant bzw. Schlüssel; globale Schalter dienen nur als Rückfallebene. Für eine einzelne Anfrage ist die Konfiguration auf der feinsten Ebene maßgeblich.

# Datenhinweise zur Preisseite für Standardmodelle

Diese Seite definiert den „Verkaufspreis“ – also, was ein Nutzer pro Nutzung eines Standardmodells bezahlt. Sie gehört zur Seite „Einkaufspreis“: Dort wird der Einkaufspreis dargestellt, hier der Verkaufspreis.

## Preisregeln (drei)

- **Eine Zeile pro Standardmodell**: Nutzer interessieren sich nur dafür, was die Nutzung eines Modells kostet, nicht für den zugrunde liegenden Anbieter.
- **Globaler Basispreis × Rabattfaktor**: Für Dimensionen ohne separate Preisdefinition (Eingabe/Ausgabe/Cache) wird der globale Basispreis automatisch mit dem Rabatt multipliziert.
- **Manuelle Preise haben Vorrang**: Als „Manuell“ markierte Felder sind unverändert und werden von globalen Preisanpassungen nicht beeinflusst – so lassen sich für besondere Modelle Preise sichern.

## Multimodale Abrechnung

Der Textumfang bei Bild-, Audio- und Videomodellen wird anhand des **dedizierten Tarifs** separat abgerechnet und nicht mit gewöhnlichem Text zusammengerechnet; Modellzeilen mit multimodaler Preisdefinition sind mit „Enthält multimodale Preise“ gekennzeichnet.

## Konsistenz mit anderen Seiten

- Änderungen auf dieser Seite gelten **sofort** für die Abrechnung nachfolgender Aufrufe; Tagesverläufe und Rechnungen werden erst nach dem nächsten Snapshot aktualisiert.
- Die auf der Benutzerseite angezeigten Preise unter „Pakete & Aufladungen“ stammen von dieser Seite. Bei Abweichungen gilt diese Seite als maßgebliche Quelle; außerdem sollte der Synchronisierungsjob geprüft werden.

## Selbstprüfung bei abweichenden Zahlen

1. Nach einer Preisanpassung hat sich der auf der Benutzerseite angezeigte Preis nicht geändert: Prüfen, ob der Cache dort nicht aktualisiert wurde oder ob für das betreffende Modell das Feld „Manuell“ ausgewählt ist.
2. Ein Modell hat zwei Verkaufspreise: Möglicherweise gilt für einen der globale Rabatt und für den anderen der manuelle Preis – dazu die Kennzeichnung „Manuell“ prüfen.
3. Der Rechnungsbetrag stimmt nicht mit den Aufrufdaten überein: Die Reihenfolge von Abrechnungszeitpunkt und Preisanpassung prüfen; Rechnungen, die eine Preisanpassung überspannen, werden abschnittsweise abgerechnet.

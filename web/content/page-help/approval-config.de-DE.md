# Konfigurationsseite für Genehmigungen

Legt fest, welche Vorgänge eine manuelle Genehmigung erfordern: Auslösebedingungen, Genehmiger und Richtlinie für Zeitüberschreitungen.

## Erläuterung der Begriffe

Richtlinie = Bedingung + Person + Zeitüberschreitung (drei Elemente); Änderungen an der Konfiguration wirken sich nur auf Genehmigungen aus, die erst danach neu ausgelöst werden.

## So prüfen Sie selbst, wenn Zahlen nicht übereinstimmen

- 1. Eingerichtet, aber es wurde keine Genehmigung erstellt: Überprüfen Sie den Geltungsbereich der Auslösebedingungen (Mandant-/Schlüsselgranularität) und den Schalterstatus.
- Die automatische Genehmigung/Ablehnung bei Zeitüberschreitung wird nicht wirksam: Ein Zeitüberschreitungswert von 0 bedeutet unbegrenztes Warten; der Vorgang wird nicht automatisch abgeschlossen.
- Der Genehmiger erhält keine Benachrichtigung: Prüfen Sie, ob das Konto des Genehmigers weiterhin aktiviert ist.

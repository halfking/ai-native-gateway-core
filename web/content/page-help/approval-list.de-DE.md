# Datenerläuterung zur Freigabeliste

Diese Seite bündelt Vorgänge, für die eine manuelle Entscheidung erforderlich ist: Bestimmte sensible Aktionen oder Anfragen, die das Budget übersteigen, werden zunächst angehalten, bis ein Administrator hier die Entscheidung trifft.

## Statusübergänge

Zur Freigabe ausstehend → (Freigegeben | Abgelehnt) → Archiviert.

- Vorgänge im Status „Zur Freigabe ausstehend“ bleiben bis zu ihrer Bearbeitung weiterhin angehalten (z. B. bleibt eine Anfrage während der Freigabeprüfung im Wartezustand).
- Für Freigaben gilt eine Frist: Vorgänge, die nach Ablauf der Frist nicht bearbeitet wurden, werden entsprechend der Konfiguration automatisch freigegeben oder abgelehnt. Auf der Detailseite ist angegeben, welche Regel für den jeweiligen Vorgang gilt.

## Beziehung zwischen Konfigurationsseite und Liste

- Die „Freigabekonfiguration“ legt fest, **in welchen Fällen** eine Freigabe erforderlich ist, wer sie erteilt und wie lange die Freigabefrist läuft.
- Die „Freigabeliste“ enthält die gemäß der Konfiguration **tatsächlich erzeugten** Aufgaben.
- Änderungen an der Konfiguration wirken sich nur auf Freigaben aus, die danach neu erzeugt werden, nicht auf bereits in der Liste enthaltene Freigaben.

## Selbstprüfung bei abweichenden Zahlen

1. Die Anzahl der Listeneinträge stimmt nicht mit der Anzahl der Benachrichtigungen überein: Benachrichtigungen können Vorgänge enthalten, die bereits von anderen Personen bearbeitet wurden. Als maßgeblich gilt der Tab „Zur Freigabe ausstehend“ in der Liste.
2. Für denselben Vorgang sind zwei Freigabe-Einträge vorhanden: Zwei unterschiedliche Regeln haben ihn jeweils einmal ausgelöst. Auf der Detailseite ist die auslösende Regel angegeben.
3. Die Freigabe wurde erteilt, die Funktion wird jedoch nicht wirksam: Die Aktivierung ist mit einer kurzen Verzögerung möglich. Wenn die Funktion nach mehr als 1 Minute noch nicht wirksam ist, ist der Vorgang auf der entsprechenden Funktionsseite zu prüfen.

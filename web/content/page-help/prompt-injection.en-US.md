# Prompt Injection Protection Page Data Notes

This page configures “Unauthorized Instruction Protection” to identify attack content embedded in user input, such as “ignore rules/act as the system.”

## Three Tabs

- **Policy**: Master switch and operating mode.
- **LLM Engine**: Engine configuration for using a model to make a secondary determination.
- **Detection Rules**: List of specific attack characteristic rules.

## Two Operating Modes (Key Difference)

- **Observation Mode (Log Only)**: Detection results are only logged; they **do not block** any requests—use this mode to review the false-positive rate before tightening the settings.
- **Enforcement Mode**: Actions are performed according to the “Severity Matrix” (allow/require manual approval/block directly); an approval timeout set to 0 means waiting indefinitely.

## Metric Definitions

- Rule hits are counted as “Detected”; hits in Observation Mode **do not result in** blocks and are reported separately from Enforcement Mode.
- The Severity Matrix determines “how the same hit is handled in each mode”—when changing the matrix, check both columns together.

## How to Self-Check When the Numbers Do Not Match

1. If you see a hit but the request was not blocked: it is most likely in Observation Mode (by design); review false positives for that rule before switching to Enforcement Mode.
2. If the false-positive rate is high: review hit samples rule by rule; common false positives occur when legitimate business content includes wording such as “ignore the above instructions”; adjust the rule rather than turning off the master switch.
3. If everything is sent for approval in Enforcement Mode: the Severity Matrix has all high-severity levels set to “Approval,” which appears as “everything is stuck” when approvers are unavailable; adjust the matrix or the approval timeout.

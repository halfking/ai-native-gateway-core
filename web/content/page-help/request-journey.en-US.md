# Request Journey Details Page

View the “journey” of a single request across multiple attempts: the entry, exit, and result of each candidate attempt are connected into a complete chain.

## Definitions

Journey = primary request + all retry attempts; total elapsed time = difference between the first and last timestamps, including all attempts.

## When the Numbers Do Not Match

- 1. Number of journey entries ≠ number of candidates: includes internal retries and fallbacks; billing is based on the final successful attempt.
- Attempts interrupted midway: attempts that timed out upstream are labeled “Interrupted”; their partial charges follow the upstream provider’s actual billing basis.

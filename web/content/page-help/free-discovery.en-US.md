# Free Resource Discovery Page Data Notes

This page is the “free model detector”: it scans each free provider according to templates to find newly available models.

## Three Sections

- **Template Management**: Defines which providers to scan and the conditions for identifying a model as free.
- **Discovery Tasks**: Records each triggered scan (when it ran and how many models were found).
- **Discovery Results**: Lists the candidate models found by each task. Manual review is required before deciding whether to add them to the pool.

## Process and Definitions

- Trigger discovery → the system probes according to the templates → results are **reviewed before being added to the pool**: discovered ≠ available. Manual confirmation is required before a model can enter the free resource pool.
- “Discovery Count” is based on candidate models. Repeated discoveries of the same model are merged and will not be added to the pool more than once.
- Deleting a template does not affect existing task or result records.

## How to Self-Check When the Numbers Do Not Match

1. A task shows 3 models found, but the results page has only 2 entries: duplicate candidates have been merged, so use the merged count.
2. A discovered model becomes unavailable after being added to the pool: free-tier availability fluctuates significantly, and the credentials used to add it may have already been rate-limited upstream. Check the credential status on the Free Resource Pool page.
3. A task remains “In Progress”: slow upstream responses can extend the probing time. If it has not finished after more than half an hour, investigate the network and template configuration.

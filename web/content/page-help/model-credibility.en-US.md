# Model Credibility Page Data Guide

This page checks whether an upstream provider is passing off a lower-tier model as a higher-tier one—often called “watering down” or “downgrading.” It cross-checks results using four checks. When no conclusion can be reached, it honestly labels the result “Insufficient Evidence” rather than treating it as “no problem.”

## Four Conclusions

| Conclusion | Meaning |
| --- | --- |
| Verified | Multiple pieces of evidence agree; the model is genuinely what it claims to be |
| Suspicious | There are warning signs, but not enough to reach a conclusion; review recommended |
| Diluted | Strong evidence shows that what is actually delivered falls below the claimed configuration |
| Insufficient Evidence | Too little evidence to determine a result (**this does not mean there is no problem**) |

## Four Checks (Decision Criteria)

The upstream provider’s self-reported identity, capability comparisons for the same model, whether request signatures have drifted, and calibration against external baselines.

## Measurement Notes

- A “determined node” is counted for each “model × provider” combination; the same model is assessed separately for each provider.
- The conclusion is based on the **most recent test**; a “full test” reruns all checks and overwrites the previous conclusion.

## How to Self-Check When the Numbers Do Not Reconcile

1. Many “Suspicious” results, but no business anomalies: “Suspicious” is not a finding of wrongdoing; it is a reminder to review. Check it alongside fingerprint drift on the Integrity Monitoring page.
2. It changes to “Insufficient Evidence” immediately after testing: too little traffic has recently flowed through that combination, resulting in an insufficient sample; test again once real traffic is available.
3. The result does not match the capabilities shown in the provider’s backend: the “Capability Comparison” among the four checks relies on traffic measured on this platform, so niche models naturally have less evidence.

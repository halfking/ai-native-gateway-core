# Data Notes for the Quality Correlation Analysis Page

This page answers the question, “What factors affect response quality?” It analyzes the statistical relationship between request characteristics (Prompt length, whether an image or code is included, and the number of tool calls) and the response quality score.

## Key Sections

- **Quality prediction factors** (correlation coefficient table): Shows the strength of the correlation between each feature and quality (the larger |r| is, the stronger its predictive power).
- **Bucket comparisons**: Groups requests into bands based on feature values (such as five bands for Prompt length) and shows the average quality, success rate, and latency for each band.

## Metric Definitions

- The quality score comes from the system’s automatic evaluation of responses (a composite of multiple dimensions); it is not a user rating.
- The correlation coefficient (Pearson r) ranges from -1 to +1: a positive number means quality increases as the feature increases; a negative number means quality decreases as it increases; a value close to 0 means there is no relationship.
- Buckets include only requests with a quality score; requests not covered by the evaluation are not included in the denominator.

## How to Self-Check When the Numbers Do Not Match

1. The correlation is strong but does not make business sense: There may be a common cause (for example, “requests with images happen to all come from a particular provider”); review the bucket details.
2. The total number of requests in the buckets is < the total number of requests on the page: Unevaluated requests are excluded from the buckets, and the difference is the “Not Evaluated” portion.
3. The r value differs between two views of the same feature: The time windows are different; r is sensitive to the time window, so use the same window when comparing.

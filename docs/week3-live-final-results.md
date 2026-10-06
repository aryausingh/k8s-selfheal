# SAGE-K8s Week 3 Live Final Results

## Dataset

- Valid runs: **28/28**

## Experiment recovery

| Workload | Enabled | Disabled | Attributable recovery |
|---|---:|---:|---:|
| W1 | 5/5 (100.0%) | 5/5 (100.0%) | +0.0 pp |
| W2 | 5/5 (100.0%) | 0/5 (0.0%) | +100.0 pp |
| W3 | 0/5 (0.0%) | 0/3 (0.0%) | +0.0 pp |

## Controller outcomes

- Incidents: **15**
- Terminal incidents: **10**
- Abandoned incidents: **5**
- Remediation attempts: **25**
- Rolled-back attempts: **20**
- Rollback attempt rate: **80.0%**
- Rollback incident rate: **50.0%**

## Terminal outcomes

- recovered: **5**
- exhausted: **5**
- escalated: **0**
- rejected: **0**
- abandoned: **5**

## Timing

- Average TTD: **15.014 s**
- Average TTM: **162.507 s**
- Average classifier latency: **6.123 s**
- Average apply: **0.007366 s**
- Average verify: **36.605 s**

## Classifier

- Persisted classifier call records represented: **15**
- Input tokens represented: **37070**
- Output tokens represented: **3204**
- Total tokens represented: **40274**
- Estimated cost represented: **$0.159270**

> Note: A1 has independent per-call recorder data. Older B/C runs store classifier metadata on CLOSED. Therefore C1 preserves the terminal classifier call, not all three attempt-level calls. Cost figures are reported only for persisted call records and are not extrapolated; represented classifier cost is not necessarily total actual experiment cost.

## Validator evaluation

- Source: `go test ./internal/classifier -run '^(TestWeek3AdversarialRejectionRate|TestWeek3LegitimateAcceptanceRate)$' -count=1 -v`

| Metric | Result |
|---|---:|
| Adversarial rejection | 15/15 (100.0%) |
| Legitimate acceptance | 30/30 (100.0%) |
| False accept rate | 0.0% |
| False reject rate | 0.0% |

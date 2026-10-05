# Final-week evidence inventory

This table records only evidence visible in the repository. Unknown evidence is
marked `MISSING`; no number is inferred.

| Artifact | Status | Owner | Evidence / next action |
|---|---|---|---|
| Week-3 audit data archived off-VM | **MISSING locally** | Arya | No `runs/` directory exists in the checked repository. Copy and verify before the demo. |
| W1/W2/W3 manifests | **EXISTS** | Arya | `hack/manifests/w1-transient.yaml`, `rollout-fixable.yaml`, `rollout-unrecoverable.yaml`. |
| Grafana lifecycle panel with real data | **MISSING locally** | Subhashini | Dashboard export and screenshot/data evidence are not present. |
| Rollback rates, attempt and incident level | **MISSING** | Subhashini | Requires archived runs and reproducible results script. |
| Recovery per workload and arm | **MISSING** | Subhashini | Requires enabled/disabled archived runs. |
| TTM decomposition | **PARTIAL** | Ananya + Subhashini | Safety emits raw transition timestamps; harness injection time, classifier timing, and archived runs are not present locally. |
| Adversarial and legitimate validator scores | **MISSING locally** | Subhashini | Test code exists, but final committed labelled sets/results are not visible here. |
| Classifier latency and cost per call | **PARTIAL** | Subhashini | Instrumentation/test fixtures exist; final run-derived output is not archived locally. |

## Owner-2 confirmation

- Exact eight-field shared audit schema: implemented on the reconciled branch.
- Durable JSONL sink and PVC path: implemented.
- UID-based pre-action candidate rejection: implemented.
- Safety/controller tests: must be green before merge.
- Live-cluster success and archived measurements: not claimed by Owner 2 without
  run evidence.


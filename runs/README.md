# Archived experiment runs

One directory per run. `audit.jsonl` is the raw append-only audit log copied
off the controller's PVC; `meta.json` records what cannot be inferred from it.

| Run | Arm | Workloads | Outcome |
|---|---|---|---|
| `A-01` | enabled | W1, W2, W3 concurrent | W2 `recovered` (1 attempt) · W3 `exhausted` (3 attempts) · W1 `rolled_back` then abandoned |
| `B-01` | disabled | W1, W2, W3 concurrent | W1 recovered unaided at +45s · W2 and W3 not recovered within 300s |

## Why the workloads run concurrently

The in-flight guard is keyed per Deployment, so W1/W2/W3 do not interfere.
One slot per arm takes about six minutes instead of thirty run serially.

`--audit-workload` is a process-level flag, so it cannot label three
workloads running under one manager. **Derive the workload from the `pod`
field instead** — the three Deployments have fixed, distinct names:

| Pod prefix | Workload |
|---|---|
| `w1-transient-` | W1 |
| `rollout-fixable-demo-` | W2 |
| `rollout-unrecoverable-demo-` | W3 |

`armLabel` is per-slot and is stamped correctly by `--audit-arm`.

## The disabled arm has no audit log

The controller is scaled to zero, so nothing writes. Recovery is judged by
hand against the same criterion the verifier applies — Ready **and**
continuously stable for 60s — and recorded in `meta.json`. Note that W2
reaches Ready briefly (its container runs `sleep 1` before exiting), which is
precisely why a single readiness check is not a recovery test.

## Reproducing

See `hack/manifests/README.md` for per-arm setup and teardown. Copy the audit
log off the PVC with a reader pod that mounts `k8s-selfheal-audit-data`.
**Do not create files in that PVC from a root pod** — the manager runs as
UID 65532 with `runAsNonRoot`, and a root-owned audit file makes it fail to
start.

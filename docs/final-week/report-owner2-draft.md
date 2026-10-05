# Owner-2 report draft

This draft covers Ananya's assigned report material. It deliberately contains
no experiment result that is not reproducible from archived runs.

## Chapter 3 — Requirements

### Functional requirements

1. Detect a CrashLoopBackOff event for a named container and preserve that
   container's restart count.
2. Accept only a final validated automation decision; a raw LLM proposal cannot
   execute.
3. Capture the exact target Deployment spec before every action.
4. Capture all Deployment-owned Pod UIDs immediately before action dispatch.
5. Execute one injected allowlisted action: `restart_pod` or `rollout_undo`.
6. Allow at most 30 seconds to observe the first Ready replacement.
7. Lock the first Ready Pod's name and UID and require 60 seconds of
   uninterrupted readiness with no named-container restart increase.
8. Restore the pre-action Deployment snapshot after verification failure.
9. Enforce a three-attempt incident budget with 30/60-second backoff and a
   terminal cooldown policy.
10. Append every safety transition and missing controller terminal outcome to
    durable JSONL using the frozen shared schema.

### Non-functional requirements

- **Safety:** unsupported or ambiguous decisions fail closed.
- **Reversibility:** every automated action starts from a restorable snapshot.
- **Attribution:** only a post-action Pod UID can satisfy recovery.
- **Boundedness:** readiness, stability, attempts, and backoff are finite.
- **Durability:** audit writes are append-only and synchronized to a PVC-backed
  file.
- **Reproducibility:** reported measurements must be regenerated from archived
  run data.
- **Isolation:** each incident owns independent state and audit entries.

### Software requirements

- Go and controller-runtime/Kubebuilder
- Kubernetes/K3s API access with Pod, Deployment, ReplicaSet, Event, and log
  permissions
- PersistentVolume support for deployed audit storage
- Prometheus/Grafana for operational visualization
- Python for the reproducible results adapter

## Chapter 4 — System design

### Architectural decomposition

The system separates probabilistic diagnosis from deterministic execution.
The Pod controller detects CrashLoopBackOff and freezes logs and Kubernetes
Events. The classifier proposes a sub-cause and action. A deterministic
validator applies the action allowlist, target checks, automation flag, and
semantic guards. The incident manager controls concurrency, attempt budget,
backoff, and cooldown. Only then is a concrete action injected into the safety
service.

The safety service performs the reversible lifecycle: capture the Deployment
snapshot, capture all pre-action Deployment Pod UIDs, execute the injected
action, resolve a replacement through controller ownership, verify continuous
health, and restore the snapshot when verification fails.

See `diagrams.md` for the architecture, control-loop, state-machine, sequence,
and deployment diagrams.

### Replacement-Pod attribution

The resolver does not trust labels alone. It reads the target Deployment,
selects ReplicaSets whose controller owner reference names that Deployment,
then selects Pods controlled by those ReplicaSets. Terminating Pods, Pods
without the named container, foreign Pods, and every UID captured before the
action are rejected.

The pre-action UID set replaced a creation-timestamp comparison. Kubernetes
API timestamps can lose sub-second precision, allowing a legitimate
same-second replacement to appear older than the action. A UID is immutable
and exact, so it is the safer attribution key.

### Two-phase verification

Verification first searches for an eligible Ready replacement for at most 30
seconds. On the first Ready observation, the verifier locks the Pod name and
UID, reads the restart count of the named container, and starts a fresh
60-second stability clock. Every subsequent poll must observe that exact Pod
Ready with the same restart count. The verifier fails immediately on any
identity, readiness, existence, status, or restart-count violation.

### Snapshot restoration

The snapshot contains the exact pre-action Deployment spec and revision.
Restoration writes that spec back and is idempotent. It is the only safety
rollback path. `rollout_undo` is a remediation action selected before entering
the safety service; it is not used as a generic recovery fallback.

### Audit design

Every line contains exactly `incidentID`, `attemptNumber`, `timestamp`,
`state`, `action`, `result`, `workload`, and `armLabel`. Safety writes the
attempt lifecycle. The controller adds `LOGGED` records for `exhausted`,
`escalated`, and `rejected`, which occur outside `Remediate`. The file writer
opens in append mode, serializes one JSON object per line, and calls `Sync`
after each append. Deployed mode mounts the file on a PVC.

## Threats to validity

### Internal validity

- Readiness and restart count are proxies for recovery; an application can be
  semantically wrong while reporting Ready.
- Evidence is frozen at detection to prevent remediation-generated Events from
  contaminating later attempts. This improves attribution but can miss a cause
  that appears only after the first action.
- Polling can observe state only at its sampling interval. A sufficiently short
  failure between polls may be missed.

### Construct validity

- The 60-second window imposes a hard minimum on measured TTM and should not be
  interpreted as action latency.
- `rolled_back` is an attempt outcome; `exhausted` is an incident outcome.
  Mixing those denominators changes the meaning of rollback rate.
- A successful Kubernetes API response means the action was accepted, not that
  the workload recovered.

### External validity

- The study covers one failure class, two reversible actions, one K3s cluster,
  and a small run count.
- W1's `emptyDir` behavior is specific to Pod replacement and may not represent
  stateful production workloads.
- Results from a single cloud VM do not establish performance under larger or
  multi-cluster deployments.

### Reliability and reproducibility

- A PVC protects against controller Pod restart but not VM/node disk loss.
  Every run must be copied off-cluster and verified by line count and size.
- Numbers are admissible only if a committed script regenerates them from
  `runs/<run>/audit.jsonl` and `meta.json`.

## Individual contribution statement — Ananya

Ananya designed and implemented the safety layer: attempt-level state machine,
Deployment snapshot capture and idempotent restoration, Deployment-based Pod
resolution, exact pre-action UID attribution, named-container continuous
verification, raw lifecycle audit events, and durable JSONL storage. She did
not implement the controller's detection/incident manager or Kubernetes action
executors, the LLM classifier/validator, experiment metrics, Grafana, workload
manifests, or cluster provisioning.


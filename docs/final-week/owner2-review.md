# Owner 2 final-week review

## Decision on the unapproved `safety.AuditEntry` change

**Raised, not approved as merged.** Commit `aa920bb` added `pod` and
`classifierMillis`, removed `workload` and `armLabel`, made fields optional,
introduced a new `CLOSED` state, and removed the durable file-backed writer.
Those changes conflicted with the frozen shared contract agreed by the three
owners:

`incidentID`, `attemptNumber`, `timestamp`, `state`, `action`, `result`,
`workload`, `armLabel`.

The reconciled Owner-2 branch keeps the useful behavior—making
`exhausted`, `escalated`, and `rejected` visible to the metrics adapter—but
records them as ordinary `LOGGED` events with the exact eight fields.
`action` is empty because those terminal records dispatch no Kubernetes
action. `attemptNumber` is `0` for escalated/rejected and `3` for an exhausted
three-attempt incident.

Classifier duration is not placed in the shared audit schema. It remains
external classifier/controller instrumentation that the metrics adapter joins
with the audit data.

## UID-based pre-action Pod rejection

**Landed on the reconciled branch.** Immediately before `Action.Execute`, the
safety service captures the UIDs of every Pod controlled by the target
Deployment. Verification rejects every Pod in that immutable set and can only
select a new UID created after the action.

This supersedes the second-truncated timestamp workaround. Kubernetes UIDs are
exact identities and avoid API timestamp precision ambiguity.

Evidence:

- `internal/safety/service.go` captures the pre-action UID set before action
  dispatch.
- `internal/safety/pod_resolver.go` follows Deployment → ReplicaSet → Pod
  controller ownership and rejects captured UIDs.
- `internal/safety/pod_resolver_test.go` covers foreign, pre-action, same-second,
  and new-UID candidates.
- `internal/safety/verifier_test.go` covers identity locking during the 60-second
  stability window.

## Durable audit status

The branch retains an append-only file writer that calls `Sync` after every
event and a PVC-backed deployment path at
`/var/lib/sage/audit/audit.jsonl`. This protects audit data across controller
Pod restarts. It does not protect against node/disk loss, so Arya's off-VM copy
remains mandatory.


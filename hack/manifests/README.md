# Integrated run — demo scenarios

Four workloads that drive the full pipeline end to end against a real cluster:
detect → collect evidence → classify → gate → act → verify → recover or roll
back → audit.

Each one exercises a different terminal outcome. Run them one at a time — the
in-flight guard is per Deployment, not global, so several at once will
interleave their logs and make the run hard to read.

| Manifest | Action taken | Expected outcome |
|---|---|---|
| `transient-recovers.yaml` | `restart_pod` | `recovered` |
| `transient-unrecoverable.yaml` | `restart_pod` | `rolled_back` |
| `rollout-fixable.yaml` | `rollout_undo` | `recovered` |
| `rollout-unrecoverable.yaml` | `rollout_undo` | `rolled_back` |

## Prerequisites

```bash
# Docker must be running, and a kind cluster must exist.
kind create cluster --name selfheal      # only if you don't already have one
kubectl config use-context kind-selfheal
```

Run the controller from your host in one terminal, and drive the scenarios
from another:

```bash
make run
```

`make run` uses your kubeconfig credentials, so the manager's RBAC does not
apply. Anything RBAC-related (`pods/log`, reading Events) only gets exercised
by `make docker-build deploy`, which is worth doing once before claiming the
generated `config/rbac/role.yaml` is correct.

Leave `CLASSIFIER_PROVIDER` unset. It defaults to the mock classifier, which is
deterministic and makes no network calls; the mock reaches the same decisions
as a real LLM on these four workloads, and costs nothing to re-run. Set it to
`anthropic` or `mistral` only when the point of the run is the LLM itself.

## What "working" looks like in the logs

```
DETECTED CrashLoopBackOff
Collected incident evidence   logBytes=52 eventCount=6      <- non-zero: evidence collection is live
remediation finished          action=restart_pod result=recovered mttr=1m17s
```

`logBytes=0 eventCount=0` followed by `ESCALATED — not safe for automation`
means evidence collection is not working — check the `Evidence` wiring in
`cmd/main.go`, and RBAC if running deployed rather than via `make run`.

Timing: expect roughly 90s from detection to a terminal outcome — 30s
readiness timeout plus a 60s stability window (`internal/safety/verifier.go`).
A `recovered` run takes the full window; a `rolled_back` run usually ends at
the 30s readiness timeout.

---

## 1. transient-recovers — `restart_pod` → `recovered`

```bash
# The node remembers the first pod that ever ran; reset it or the demo
# starts healthy and proves nothing. Node name comes from `kubectl get nodes`.
docker exec selfheal-control-plane rm -rf /tmp/k8s-selfheal

kubectl apply -f hack/manifests/transient-recovers.yaml
kubectl get pods -l app=transient-recovers-demo -w
```

The first pod logs `connection refused` and crash-loops. The classifier reads
that log, proposes `transient_failure` → `restart_pod`, the guard allows it,
the pod is deleted, and the replacement comes up healthy and stays Ready for
the full stability window.

```bash
kubectl delete -f hack/manifests/transient-recovers.yaml
```

## 2. transient-unrecoverable — `restart_pod` → `rolled_back`

```bash
kubectl apply -f hack/manifests/transient-unrecoverable.yaml
```

Same evidence, same automatable decision — but every replacement crashes too.
The verifier gives up at the readiness timeout and the service rolls back.
The snapshot restore is a no-op here (`restart_pod` never changed the
Deployment spec), which is the point: the outcome must still be `rolled_back`,
not silently treated as success.

```bash
kubectl delete -f hack/manifests/transient-unrecoverable.yaml
```

## 3. rollout-fixable — `rollout_undo` → `recovered`

This one needs two revisions: a good one to roll back *to*, then a bad one.

```bash
# Revision 1 — known good. Note the inline command override.
kubectl apply -f hack/manifests/rollout-fixable.yaml --dry-run=client -o yaml \
  | sed 's/"sleep 1; exit 1"/"sleep 3600"/' \
  | kubectl apply -f -
kubectl rollout status deployment/rollout-fixable-demo

# Revision 2 — the bad deploy.
kubectl patch deployment rollout-fixable-demo --type=json -p \
  '[{"op":"replace","path":"/spec/template/spec/containers/0/command","value":["sh","-c","sleep 1; exit 1"]}]'
kubectl get pods -l app=rollout-fixable-demo -w
```

There are no logs at all here — the container dies without printing anything.
Classification runs purely on Events: a `BackOff` on the pod plus a recent
`ScalingReplicaSet` on the Deployment is what identifies this as `bad_deploy`,
which is why the collector queries the Deployment and ReplicaSet and not just
the pod. `rollout_undo` reverts the template to revision 1 and the replacement
pod stays up.

```bash
kubectl delete deployment rollout-fixable-demo
```

## 4. rollout-unrecoverable — `rollout_undo` → `rolled_back`

```bash
# Revision 1 — already broken, on purpose.
kubectl apply -f hack/manifests/rollout-unrecoverable.yaml --dry-run=client -o yaml \
  | sed 's/"echo bad-2; exit 1"/"echo bad-1; exit 1"/' \
  | kubectl apply -f -

# Wait for CrashLoopBackOff, then roll out an equally broken revision 2.
kubectl patch deployment rollout-unrecoverable-demo --type=json -p \
  '[{"op":"replace","path":"/spec/template/spec/containers/0/command","value":["sh","-c","echo bad-2; exit 1"]}]'
```

`rollout_undo` reverts to revision 1, which is also broken, so verification
fails and the snapshot restore puts revision 2's spec back. This is the only
scenario where the rollback genuinely changes the cluster, so it is the one
that proves snapshot/restore works against a live API server.

```bash
kubectl delete deployment rollout-unrecoverable-demo
```

## Reading the audit trail

Audit entries are JSONL on the manager's stdout — the same terminal as
`make run`. One line per state transition:

```bash
make run 2>&1 | tee /tmp/selfheal-run.jsonl
grep -E '"state":' /tmp/selfheal-run.jsonl
```

A complete recovered run walks `DETECTED → SNAPSHOTTED → REMEDIATING →
VERIFYING → RECOVERED → LOGGED`; a rolled-back one substitutes `ROLLING_BACK →
ROLLED_BACK → LOGGED` for the last two.

## Cleanup

```bash
kubectl delete deployment -l 'app in (transient-recovers-demo,transient-unrecoverable-demo,rollout-fixable-demo,rollout-unrecoverable-demo)'
docker exec selfheal-control-plane rm -rf /tmp/k8s-selfheal
```

# Owner 2 viva and demo narration

## 90-second component narrative

“I owned the safety layer. Its job begins only after the controller has a final
validated action. Before that action runs, the layer captures the exact
Deployment spec and the UIDs of every Pod already owned by that Deployment.
That gives us two guarantees: restoration has a precise target, and an old Pod
cannot be mistaken for the remediation result.

After the injected action returns, verification has two phases. We allow at
most 30 seconds to observe the first Ready replacement. At that first Ready
observation we lock its name and UID, record the restart count of the named
container—not an aggregate—and start a fresh 60-second stability window. The
attempt fails immediately if the Pod disappears, loses Ready, changes identity,
loses the container status, or its restart count increases. We never switch to
another Pod during that window.

Only an uninterrupted 60 seconds produces recovered. Otherwise we restore the
pre-action Deployment snapshot; rollout undo is an action, not our rollback
mechanism. Every transition is appended to durable JSONL so the metrics are
derived from evidence rather than memory.”

## Hard questions

### Doesn't Kubernetes already restart crashing containers?

Yes. That is why the experiment includes a controller-disabled arm. Kubernetes
self-recovery is the baseline; attributable recovery is enabled minus disabled
for each workload.

### Why require 60 seconds?

It was chosen a priori, not tuned against these results. It reduces false
recovery from short-lived readiness but creates a hard TTM floor. Window
sensitivity is a stated limitation and future experiment.

### Why only two actions?

Reversibility. The system deliberately limits automated execution to
`restart_pod` and `rollout_undo`, and the safety layer can restore the exact
pre-action Deployment spec if verification fails.

### What if the LLM is wrong?

The LLM cannot execute. It produces a proposal; a deterministic validator
checks its action, target, automation flag, and semantic consistency with the
evidence. Unsafe or unsupported outcomes terminate without consuming an
attempt.

### Why poll instead of watch?

Polling was frozen for this implementation and makes continuous-window tests
deterministic. A watch reduces repeated reads and reacts quickly, but adds
reconnect/resource-version handling and must still protect against missed
events. Polling is simpler for the bounded 30+60-second verification period.

### Why capture all pre-action Pod UIDs?

Timestamps from Kubernetes are effectively second-granular, while local Go
timestamps contain sub-second precision. A valid replacement created in the
same second can compare as older. UIDs are exact identities and avoid that
ambiguity.

### Is rollout undo the rollback?

No. `rollout_undo` is one injected remediation action. Owner 2's rollback is
always restoration of the Deployment snapshot captured immediately before the
action.

### What is the difference between Ready once and continuously Ready?

Ready once only proves one observation. Continuously Ready means every poll for
the full 60-second window sees the same Pod UID Ready with no increase in the
named container's restart count.

### What did you not build?

I did not build detection, action selection, the Kubernetes action executors,
the LLM classifier/validator, experiment metrics, Grafana, cluster
provisioning, or the workload manifests. I built snapshot/restore,
replacement-Pod attribution, bounded verification, lifecycle state/audit, and
their tests.


# SAGE-K8s six-minute demo deck — 10 slides

This is the content source for the generated deck. Bracketed evidence markers
must be replaced only from archived runs; they are deliberately not guessed.

## 1. SAGE-K8s

**Safe autonomous remediation for Kubernetes CrashLoopBackOff**

Detect → classify → validate → snapshot → act → verify → restore if unsafe.

Speaker line: “We automate one failure class deeply, with a reversible two-action
allowlist and measured safety behavior.”

## 2. Problem in one sentence

Kubernetes can restart containers, but it does not decide whether a crash loop
needs a Pod restart or a Deployment rollback, verify that the chosen repair
stayed healthy, and restore the pre-action state when it did not.

## 3. The gap

- Recommendation-only tools still need a human to execute.
- Autonomous loops can amplify a wrong diagnosis.
- A successful API call is not a recovered workload.
- Safety behavior is often described but not directly exercised and measured.

## 4. Architecture

Use the component architecture from `diagrams.md`.

Speaker line: “The LLM proposes. The validator decides whether automation is
allowed. The safety layer owns reversibility and proof of recovery.”

## 5. One incident, end to end

1. Watch Pod status and detect CrashLoopBackOff.
2. Freeze logs and Events at detection.
3. Classify, then validate against evidence and the action allowlist.
4. Admit an attempt under the budget/backoff rules.
5. Snapshot the Deployment and capture all pre-action Pod UIDs.
6. Execute `restart_pod` or `rollout_undo`.
7. Poll for first Ready for at most 30 seconds.
8. Require the same Pod UID to remain healthy for 60 uninterrupted seconds.
9. Restore the exact snapshot if verification fails.
10. Append raw lifecycle evidence to durable JSONL.

## 6. Safety layer — Owner 2

- Snapshot before every action; restoration is the sole rollback path.
- Replacement attribution follows Deployment → ReplicaSet → Pod ownership.
- Exact pre-action UID exclusion; no timestamp guessing.
- Named-container restart count only; sidecars cannot cause a false failure.
- First Ready ≤30s, then one locked Pod for 60 continuous seconds.
- Immediate failure on NotReady, disappearance, UID change, missing container
  status, or restart-count increase.
- Append-only audit with one synchronized write per transition.

Speaker line: “An action is not successful because Kubernetes accepted it. It
is successful only if one attributable replacement stays healthy.”

## 7. Experimental setup

| Workload | Purpose | Enabled arm | Disabled arm |
|---|---|---|---|
| W1 transient | Can recover unaided | Controller acts | Kubernetes baseline |
| W2 fixable rollout | Good previous revision | Expected `rollout_undo` recovery | No controller action |
| W3 unrecoverable rollout | Both revisions bad | Verification failure and snapshot restore | No controller action |

Planned matrix: three workloads × two arms; target N=5 except any explicitly
documented reduced sanity-check arm.

## 8. Results

**Do not present invented numbers.** Populate from `results.py` after `runs/`
is archived.

Required cells:

- attempt-level rollback rate: `[MISSING]`
- incident-level rollback rate: `[MISSING]`
- recovery by workload and arm: `[MISSING]`
- attributable recovery (enabled − disabled): `[MISSING]`
- TTD and TTM decomposition: `[MISSING]`
- five-outcome distribution: `[MISSING]`

Live qualitative evidence to show: W2 recovery, W3 rollback in roughly the
observed 20–30-second failure window, and archived three-attempt exhaustion.

## 9. Comparison boundary

| System | What may safely be said | Evidence needed before presenting |
|---|---|---|
| Wiesinger | Uses iterative remediation/retry behavior | Cite the exact paper passage. |
| ARBITER | Includes a rollback monitor and approval-oriented experiments | Cite the exact paper passages and avoid claiming it lacks recovery. |
| SAGE-K8s | Exercises an ungated loop for one fault class and measures rollback behavior with a disabled control arm | Link every number to archived runs and `results.py`. |

Do not claim priority over ARBITER. Do not say either comparator has “no
recovery mechanism.”

## 10. Limitations and future work

- The 60-second stability window creates a hard TTM floor.
- The 30/60-second constants were chosen a priori, not tuned.
- Small sample size and one K3s cluster limit generalization.
- Two actions and one failure class favor reversibility over breadth.
- PVC persistence does not protect against node/disk loss; runs require
  off-cluster archiving.
- Frozen evidence preserves attribution but can miss causes visible only after
  the first action.

Future work: sensitivity analysis for verification windows, multi-cluster
replication, and broader reversible actions—only after the present evaluation
is complete.


# Final experiment runs — 28 runs, 2026-10-05

Single-node K3s on an Azure VM, controller deployed in-cluster at the frozen
image, live Claude classifier (`CLASSIFIER_PROVIDER=anthropic`,
`claude-sonnet-4-6`). Three workloads × two arms, N=5 per cell — except
W3-disabled, which has N=3.

Run IDs encode the cell: letter = workload (**A**=W1, **B**=W2, **C**=W3),
digit = arm (**1**=enabled, **2**=disabled).

Per-run files:

| File | What it is |
|---|---|
| `audit.jsonl` | the controller's append-only audit log for that run, copied off the audit PVC. One JSON object per line, one line per state transition. Empty in the disabled arm — the controller is scaled to zero, so nothing writes. |
| `meta.json` | what cannot be inferred from the audit log: workload, arm, injection timestamp and exit code, and for the disabled arm the hand-judged recovery outcome against the same criterion the verifier applies (Ready **and** continuously stable for 60 s). |
| `classifier-calls.jsonl` | per-call classifier records: latency, provider, model, token counts, raw response. |

## Run inventory

`Attempts` is the highest attempt number reached. `Outcome` lists the
attempt-level results in order, then the incident's terminal outcome in bold.

| Run | Workload | Arm | Injected (UTC) | Attempts | Outcome |
|---|---|---|---|---:|---|
| `A1-01` | W1 | enabled | 2026-10-05 17:20:24Z | 1 | `rolled_back` → no terminal line; recovered unaided at +126 s |
| `A1-02` | W1 | enabled | 2026-10-05 17:28:36Z | 1 | `rolled_back` → no terminal line; recovered unaided at +122 s |
| `A1-03` | W1 | enabled | 2026-10-05 17:32:07Z | 1 | `rolled_back` → no terminal line; recovered unaided at +126 s |
| `A1-04` | W1 | enabled | 2026-10-05 17:35:40Z | 1 | `rolled_back` → no terminal line; recovered unaided at +121 s |
| `A1-05` | W1 | enabled | 2026-10-05 17:39:06Z | 1 | `rolled_back` → no terminal line; recovered unaided at +128 s |
| `A2-01` | W1 | disabled | 2026-10-05 17:42:21Z | — | recovered unaided at +99 s |
| `A2-02` | W1 | disabled | 2026-10-05 17:45:06Z | — | recovered unaided at +104 s |
| `A2-03` | W1 | disabled | 2026-10-05 17:47:59Z | — | recovered unaided at +101 s |
| `A2-04` | W1 | disabled | 2026-10-05 17:50:47Z | — | recovered unaided at +104 s |
| `A2-05` | W1 | disabled | 2026-10-05 17:53:37Z | — | recovered unaided at +103 s |
| `B1-01` | W2 | enabled | 2026-10-05 14:46:27Z | 1 | `recovered` → **`recovered`** |
| `B1-02` | W2 | enabled | 2026-10-05 14:50:41Z | 1 | `recovered` → **`recovered`** |
| `B1-03` | W2 | enabled | 2026-10-05 14:54:59Z | 1 | `recovered` → **`recovered`** |
| `B1-04` | W2 | enabled | 2026-10-05 14:59:18Z | 1 | `recovered` → **`recovered`** |
| `B1-05` | W2 | enabled | 2026-10-05 15:03:36Z | 1 | `recovered` → **`recovered`** |
| `B2-01` | W2 | disabled | 2026-10-05 15:07:43Z | — | not recovered within 300 s |
| `B2-02` | W2 | disabled | 2026-10-05 15:13:53Z | — | not recovered within 300 s |
| `B2-03` | W2 | disabled | 2026-10-05 15:20:03Z | — | not recovered within 300 s |
| `B2-04` | W2 | disabled | 2026-10-05 15:26:14Z | — | not recovered within 300 s |
| `B2-05` | W2 | disabled | 2026-10-05 15:32:24Z | — | not recovered within 300 s |
| `C1-01` | W3 | enabled | 2026-10-05 13:50:38Z | 3 | `rolled_back` ×3 → **`exhausted`** |
| `C1-02` | W3 | enabled | 2026-10-05 13:59:29Z | 3 | `rolled_back` ×3 → **`exhausted`** |
| `C1-03` | W3 | enabled | 2026-10-05 14:06:40Z | 3 | `rolled_back` ×3 → **`exhausted`** |
| `C1-04` | W3 | enabled | 2026-10-05 14:13:11Z | 3 | `rolled_back` ×3 → **`exhausted`** |
| `C1-05` | W3 | enabled | 2026-10-05 14:20:18Z | 3 | `rolled_back` ×3 → **`exhausted`** |
| `C2-01` | W3 | disabled | 2026-10-05 14:27:12Z | — | not recovered within 300 s |
| `C2-02` | W3 | disabled | 2026-10-05 14:33:39Z | — | not recovered within 300 s |
| `C2-03` | W3 | disabled | 2026-10-05 14:40:06Z | — | not recovered within 300 s |

## Reading the W1-enabled rows

Those five runs are the project's negative result, not a data problem.
`restart_pod` deletes the Pod; that destroys the `emptyDir` holding W1's
restart counter, so the replacement crash-loops from scratch and cannot be
Ready inside the 30 s readiness timeout. The attempt is therefore
`rolled_back`, the incident is abandoned without a terminal line (the Pod the
incident was keyed to no longer exists, and the replacement's own incident is
a new one), and the workload then recovers on its own — about 22 s *later*
than it would have with the controller switched off.

Both statements are true at different levels of measurement, and the results
slide must say which one it means:

- per **run**, against the experiment's recovery criterion: W1-enabled is 5/5;
- per **attempt**, as the controller's own outcome: W1-enabled is 5/5
  `rolled_back`.

## Regenerating the numbers

```bash
python3 hack/experiment/results.py --runs-dir sage-final-runs \
  --md-out docs/week3-live-final-results.md
```

Fails loudly on any incomplete or malformed run rather than skipping it. The
definitions it computes against are frozen in
[`docs/measurement-definitions.md`](../docs/measurement-definitions.md).

## Caveat on classifier cost

`classifier-calls.jsonl` exists as an independent per-call recorder only for
the A1 runs. The older B and C runs carry classifier metadata on the `CLOSED`
record instead, so C1 preserves the terminal attempt's call and not all three.
Reported classifier cost covers persisted call records only and is not
extrapolated to the whole experiment.

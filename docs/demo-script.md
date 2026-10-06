# SAGE-K8s — demo script

Read this under pressure. Every command is copy-pasteable, every expected
output is something actually observed on a run, not a guess.

**Total: 6 minutes.** The 2-minute cut is at the bottom.

---

## The one mechanic that makes this work

**Nothing is injected live.** Both workloads are staged into CrashLoopBackOff
during pre-flight *with the controller scaled to zero*, and the live moment is
**scaling the controller up**. Detection then happens within ~15 seconds,
every time.

Two reasons this is not cheating, and say so if asked:

- A real crash loop takes 30–80s to appear, because the kubelet's back-off is
  10s, 20s, 40s. Waiting for that on stage is dead air with nothing to look at.
- W3 *cannot* be staged with the controller running: it has no good revision,
  so `rollout_undo` would fail with "no prior revision available" before you
  ever got to revision 2.

The honest line, delivered while you scale up: *"The workload is already
failing. I'm starting the controller now."*

---

## WHICH CLUSTER — decide before pre-flight

| | VM (K3s) | kind (local) |
|---|---|---|
| Build | `k8s-selfheal:classifier-cost-46` | `controller:v0.3.0` (current `main`) |
| Matches the results table | **Yes** — it produced the 28 runs | No, three merges newer |
| Grafana | **Yes** | No |
| Attempt budget present | Yes, proven by `exhausted` in C1-01..05 | Yes |
| Rehearsed on | Not yet | Yes |

**Prefer the VM** — demo and results then come from the same binary, which is
the first consistency an examiner checks. **Switch to kind without hesitation**
if the tunnel is flaky or you have not managed a full dry run there. A
rehearsed demo on a cluster you control beats a consistent one you have driven
once.

**Do not redeploy `main` to the VM.** That build produced the results; changing
it the night before breaks the correspondence and buys nothing.

### Connecting to the VM

The K3s API is not publicly reachable, so everything goes through an SSH
tunnel. **Terminal A stays open all evening — it *is* the tunnel:**

```bash
ssh -N -L 6443:127.0.0.1:6443 <username>@20.219.66.205
```

Every other terminal:

```bash
export KUBECONFIG=~/.kube/k3s-arya.yaml
kubectl get nodes          # proves the tunnel is up
```

Keep `KUBECONFIG` exported per-terminal rather than globally, so the kind
fallback stays reachable in any shell that has not set it.

Two failures and their fixes:

- `x509: certificate is valid for ...` → the kubeconfig `server:` must be
  `https://127.0.0.1:6443`, not the public IP. Edit it.
- `connection refused` → the tunnel died. Look at Terminal A, re-run it.

### What differs on the VM

- **No `kind load`** — the image is already on the node. Skip any build step.
- The audit PVC exists but its live files were emptied after the Week 3
  export, so it starts clean. That is expected.
- Segment 4 reads from the archived 28-run dataset, not from `runs/A-01` —
  use `runs/C1-01/audit.jsonl`, which is a real W3-enabled run that reached
  `exhausted`.

---

## PRE-FLIGHT — 45 minutes before, not 5

Run every line. Stop at the first thing that doesn't match.

```bash
cd ~/Desktop/projects/k8s-selfheal

# 1. Cluster and controller
kubectl get nodes                       # expect: 1 node, Ready
kubectl get pods -n k8s-selfheal-system # expect: controller-manager 1/1, audit-reader 1/1
kubectl get deploy -n k8s-selfheal-system k8s-selfheal-controller-manager \
  -o jsonpath='{.spec.template.spec.containers[0].image}{"\n"}'
# expect: controller:v0.3.0  (the frozen image — if this differs, STOP and redeploy)

# 2. Audit sink is open (this is what crash-loops the controller if it is wrong)
kubectl logs -n k8s-selfheal-system deploy/k8s-selfheal-controller-manager -c manager \
  | grep -i "durable audit sink"
# expect: INFO setup Using durable audit sink {"path": "/var/lib/sage/audit/audit.jsonl"}

# 3. No leftovers
kubectl get deploy | grep -E 'rollout|w1-transient'   # expect: nothing
```

**Then stage W2 and leave it crash-looping.** This is segment 2's workload.

```bash
kubectl scale -n k8s-selfheal-system deploy/k8s-selfheal-controller-manager --replicas=0
kubectl wait --for=delete pod -n k8s-selfheal-system -l control-plane=controller-manager --timeout=60s

# revision 1 — the known-good one we will roll back TO
sed 's/"sleep 1; exit 1"/"sleep 3600"/' hack/manifests/rollout-fixable.yaml | kubectl apply -f -
kubectl rollout status deployment/rollout-fixable-demo

# revision 2 — the bad deploy
kubectl patch deployment rollout-fixable-demo --type=json -p \
  '[{"op":"replace","path":"/spec/template/spec/containers/0/command","value":["sh","-c","sleep 1; exit 1"]}]'

kubectl get pods -l app=rollout-fixable-demo -w     # wait for CrashLoopBackOff, then Ctrl-C
```

**Terminal layout.** Four, pre-arranged, before anyone is watching:

| Terminal | Contents |
|---|---|
| **1** | controller logs, command typed but not run |
| **2** | `kubectl get pods -w` on the demo workload |
| **3** | the scale-up / patch commands |
| **4** | `runs/A-01/audit.jsonl` open in an editor |

Browser: Grafana panel open. Player: recordings open. Deck: open on slide 1.

---

## SEGMENT 1 — Context · 0:00–0:40

One slide. One sentence, said slowly:

> "It detects CrashLoopBackOff, fixes it from a two-action reversible
> allowlist, verifies the fix held for sixty seconds, and rolls back if it
> didn't."

Then: *"Everything after this is live on a real cluster."*

---

## SEGMENT 2 — Happy path · 0:40–2:00

**Terminal 1** — start the log stream:

```bash
kubectl logs -n k8s-selfheal-system deploy/k8s-selfheal-controller-manager -c manager -f \
  | grep -E 'DETECTED|Collected|remediation finished'
```

**Terminal 3** — the live moment:

```bash
kubectl scale -n k8s-selfheal-system deploy/k8s-selfheal-controller-manager --replicas=1
```

Say: *"The deployment is already failing — somebody shipped a bad revision.
I'm starting the controller now."*

**What you will see, and when:**

| ~Time | Output | Say |
|---|---|---|
| +15s | `DETECTED CrashLoopBackOff` | "It found it." |
| +15s | `Collected incident evidence ... logBytes=0 eventCount=9` | **"Zero log bytes. This container dies silently. It's classified purely on Kubernetes events — a rollout happened right before the crash."** |
| +65s | `remediation finished ... action=rollout_undo result=recovered mttr=1m5s` | "Recovered. And the 65 seconds is almost entirely the verification window." |

**Terminal 2** shows the replacement pod going `1/1 Running`.

The `logBytes=0` line is the best unscripted moment you have. Point at it.

---

## SEGMENT 3 — Rollback · 2:00–3:00 · **this is the contribution**

Stage W3 while you talk. Say: *"Now the case the project actually exists for
— what happens when the fix doesn't work."*

**Terminal 3:**

```bash
kubectl delete deploy rollout-fixable-demo --wait=false
kubectl scale -n k8s-selfheal-system deploy/k8s-selfheal-controller-manager --replicas=0
kubectl wait --for=delete pod -n k8s-selfheal-system -l control-plane=controller-manager --timeout=60s

# both revisions broken, on purpose
sed 's/"echo bad-2; exit 1"/"echo bad-1; exit 1"/' hack/manifests/rollout-unrecoverable.yaml | kubectl apply -f -
kubectl wait --for=jsonpath='{.status.unavailableReplicas}'=1 deploy/rollout-unrecoverable-demo --timeout=90s
kubectl patch deployment rollout-unrecoverable-demo --type=json -p \
  '[{"op":"replace","path":"/spec/template/spec/containers/0/command","value":["sh","-c","echo bad-2; exit 1"]}]'

kubectl scale -n k8s-selfheal-system deploy/k8s-selfheal-controller-manager --replicas=1
```

**What you will see:**

| ~Time | Output | Say |
|---|---|---|
| +15s | `DETECTED` then `Collected incident evidence` | "Same detection path." |
| +45s | `remediation finished ... result=rolled_back` | **"There it is. It applied the fix, watched for thirty seconds, the pod never became Ready, so it put the deployment back exactly as it was and recorded a failure. It did not claim success."** |

**Rollback fires in ~30s, not 4m33s.** That number is the full three-attempt
exhaust. Do not let anyone conflate them — if asked, say so immediately.

---

## SEGMENT 4 — Knowing when to stop · 3:00–3:40

Do **not** wait for this live. Switch to **Terminal 4**.

> "If I let that run, it tries three times and then stops permanently. That
> takes four and a half minutes, so here it is from an archived run."

```bash
python3 - <<'EOF'
import json,collections
inc=collections.defaultdict(list)
for l in open('runs/C1-01/audit.jsonl'):   # VM dataset; use runs/A-01 on kind
    d=json.loads(l); inc[d['incidentID'][:8]].append(d)
for k,v in inc.items():
    if 'unrecoverable' in v[-1].get('pod',''):   # W3
        for d in v:
            if d['state'] in ('LOGGED',):
                print(f"attempt {d['attemptNumber']}  {d['result']}")
EOF
```

Expected:

```
attempt 1  rolled_back
attempt 2  rolled_back
attempt 3  rolled_back
attempt 3  exhausted
```

> "Three attempts, then it emits `exhausted` and goes silent — permanently,
> until a human changes the deployment. I verified that by leaving one running
> for four hours and forty-three minutes while the pod kept crash-looping. The
> controller never touched it again."

That 4h43m figure is the strongest number you have. Say it slowly.

---

## SEGMENT 5 — Why we measure a baseline · 3:40–4:30

> "Last thing, and it's the one that makes the numbers mean anything."

```bash
kubectl scale -n k8s-selfheal-system deploy/k8s-selfheal-controller-manager --replicas=0
kubectl apply -f hack/manifests/w1-transient.yaml
kubectl get pods -l app=w1-transient -w
```

Pod crashes, restarts, crashes — then goes `1/1 Running` at about **+45s**,
with the controller switched off.

> "Kubernetes fixed that one by itself. So if we only ever measured with our
> controller running, we couldn't tell which recoveries were ours. That's why
> every injection runs twice — controller on and controller off — and we report
> recovery net of the baseline."

**If asked whether the controller helps this workload, answer honestly:** it
makes it *worse*. W1 counts restarts in scratch storage that dies with the
pod, so `restart_pod` resets the counter and it starts over. Attributable
recovery for W1 is negative. That is a finding about the limits of blunt
remediation, and the control arm is what exposed it.

---

## SEGMENT 6 — Numbers · 4:30–6:00

Results table, then the comparison table. Two rules:

- Every number traceable to a run in `runs/`. If it isn't, say "in progress".
- Never claim priority over ARBITER, and never say either comparator "has no
  recovery mechanism" — Wiesinger retries, ARBITER has a monitor. Say
  precisely what is missing instead.

---

## The 2-minute cut

Drop in this order: **5, then 6, then 4.** Never drop 3.

| Time | Segment |
|---|---|
| 0:00–0:20 | Context, one sentence |
| 0:20–1:10 | W2 live → recovered |
| 1:10–1:50 | W3 live → rollback fires |
| 1:50–2:00 | One line: "three attempts then it stops; measured against a controller-off baseline" |

---

## When it breaks

It will. Say this, without apologising for the cluster:

> "What should happen here is X. The cluster's being slow — here's the
> recorded run while it catches up."

Then play the recording for that segment and carry on talking. Recovery
commands, in order of how often you'll need them:

```bash
# controller wedged or crash-looping
kubectl rollout restart -n k8s-selfheal-system deploy/k8s-selfheal-controller-manager
kubectl logs -n k8s-selfheal-system deploy/k8s-selfheal-controller-manager -c manager --tail=20

# nothing being detected — is the controller actually up?
kubectl get pods -n k8s-selfheal-system

# full reset between segments
kubectl delete deploy rollout-fixable-demo rollout-unrecoverable-demo w1-transient --ignore-not-found
```

**The one failure that looks like a bug and isn't:** if the controller won't
start and the log says `permission denied` on the audit file, something created
that file as root. The manager runs as UID 65532. Fix:

```bash
kubectl exec -n k8s-selfheal-system audit-reader -- rm -f /audit/audit.jsonl
kubectl rollout restart -n k8s-selfheal-system deploy/k8s-selfheal-controller-manager
```

---

## Numbers to have in your mouth

| | |
|---|---|
| W2 recovery | **1m5s** — of which 60s is the verification window |
| W3 rollback | **~30s per attempt** |
| Full exhaust | **4m33s** — three attempts |
| Silence after exhausting | **4h43m**, while the pod kept crash-looping |
| W1 unaided | **~36–45s** |
| Readiness timeout / stability window | **30s / 60s** — chosen a priori, not tuned |

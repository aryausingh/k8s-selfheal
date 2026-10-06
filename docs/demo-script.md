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
tunnel. **Terminal A stays open all day — it *is* the tunnel.**

Use the reconnecting form, not a bare `ssh -N -L`. The plain one died during
the dry run with `Operation timed out / Broken pipe` after about ninety
minutes — the VM was fine, the connection simply dropped — and a bare tunnel
dies silently, leaving every `kubectl` with `connection refused` and nothing
on screen to explain it. This version comes back in about two seconds and
prints when it happened:

```bash
while true; do
  ssh -N -o ExitOnForwardFailure=yes -o ServerAliveInterval=15 \
      -o ServerAliveCountMax=3 -o ConnectTimeout=10 \
      -L 6443:127.0.0.1:6443 azureuser@20.219.66.205
  echo "tunnel dropped $(date -u +%H:%M:%SZ), reconnecting"
  sleep 2
done
```

If `kubectl` ever says `connection refused` mid-demo, glance at Terminal A:
either it is mid-reconnect — wait two seconds and retry — or the VM itself is
gone, which `ssh azureuser@20.219.66.205 uptime` tells you in one command.

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
- **The VM audit schema has seven fields, not nine** — no `workload`, no
  `armLabel`, because that build predates those flags:
  `timestamp · pod · state · action · result · incidentID · attemptNumber`.
  This is the same schema as the 28-run dataset, which is the point: demo and
  results come from one binary.
- Online Boutique, Chaos Mesh and Prometheus/Grafana all run on this cluster,
  and Boutique occupies the `default` namespace alongside the demo workloads.
  Harmless — the names do not collide — but `kubectl get pods` is noisy, so
  always filter with `-l app=...`.

### Dry-run status

All four live segments dry-run on the VM, 2026-10-06:

| Segment | Result | Measured |
|---|---|---|
| 2 · W2 happy path | `rollout_undo` → `recovered` | mttr **1m11.02s**, visible at +90s |
| 3 · W3 rollback | `rollout_undo` → `rolled_back` | mttr **36.7s**, visible at +84s |
| 4 · exhaust | 3 attempts → `exhausted` | **4m54s** end to end |
| 5 · W1 unaided | Ready, 3 restarts, controller off | **47s** |

`logBytes=0 eventCount=9` reproduced. Audit lines land in the PVC correctly.

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

## SEGMENTS 2 + 3 — run them from ONE scale-up · 0:40–2:20

**This is a change from the original plan, and the dry run is why.** Staging
W3 live costs about 1m45s — you must wait for revision 1 to crash-loop before
you can patch revision 2 — and that does not fit inside a sixty-second slot
with anything to look at.

So **stage W2 and W3 both during pre-flight**, and scale the controller up
**once**. They are independent Deployments, the in-flight guard is keyed per
Deployment, and both are remediated concurrently. Verified on kind across five
repetitions and on the VM.

It is also a better demo: two unrelated failures, handled at the same time,
without the operator choosing between them.

### Terminal layout for this

Two log streams, each filtered to one workload, side by side:

```bash
# Terminal 1 — the happy path
kubectl logs -n k8s-selfheal-system deploy/k8s-selfheal-controller-manager -c manager -f \
  | grep --line-buffered -E 'DETECTED|Collected|remediation finished' | grep --line-buffered fixable

# Terminal 2 — the rollback
kubectl logs -n k8s-selfheal-system deploy/k8s-selfheal-controller-manager -c manager -f \
  | grep --line-buffered -E 'DETECTED|remediation finished' | grep --line-buffered unrecoverable
```

### The live moment

```bash
kubectl scale -n k8s-selfheal-system deploy/k8s-selfheal-controller-manager --replicas=1
```

> "Two deployments are already failing. One of them has a good revision to go
> back to; the other doesn't. I'm starting the controller now — it has never
> seen either of them."

### What happens, measured on the VM

| ~Time | Terminal | Output | Say |
|---|---|---|---|
| +19–42s | both | `DETECTED CrashLoopBackOff` | "Found both." |
| +20s | 1 | `Collected incident evidence ... logBytes=0 eventCount=9` | **"Zero log bytes — this container dies silently. It is classified purely on Kubernetes events: a rollout happened immediately before the crash."** |
| **+84s** | 2 | `remediation finished ... result=rolled_back mttr=36.7s` | **"There's the one that matters. It applied the fix, watched for thirty seconds, the pod never became Ready — so it restored the deployment exactly as it was and recorded a failure. It did not claim success."** |
| **+90s** | 1 | `remediation finished ... result=recovered mttr=1m11s` | "And that one genuinely recovered. Seventy-one seconds, of which sixty are the verification window — we wait on purpose." |

**The rollback lands first.** Lead with it; it is the contribution.

Detection latency varied between **+19s and +42s** across dry runs, so do not
promise a number out loud. Say "within about a minute" and let it arrive.

## SEGMENT 4 — Knowing when to stop · 3:00–3:40

Do **not** wait for this live. Switch to **Terminal 4**.

> "If I let that run, it tries three times and then stops permanently. End to
> end that is just under five minutes — measured at 4m54s on this cluster — so
> here it is from an archived run."

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

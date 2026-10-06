# SAGE-K8s final-week diagrams

The diagrams use labels and shapes that remain distinguishable when printed in
greyscale.

## Component architecture

```mermaid
flowchart LR
    FI[Fault injector / run harness]
    API[(K3s API server)]
    PC[Pod controller\nCrashLoopBackOff detection]
    EC[Evidence collector\nlogs + Events]
    CL[LLM classifier\nproposal only]
    VA[Deterministic validator\nsemantic guard + allowlist]
    IM[Incident manager\nbudget + backoff + cooldown]
    SS[Safety service\nsnapshot → act → verify → restore]
    AC[Injected actions\nrestart_pod | rollout_undo]
    VR[Deployment Pod resolver + verifier\n30s Ready + 60s stable]
    AU[(Append-only audit.jsonl\nPVC + per-write sync)]
    PM[Prometheus]
    GF[Grafana]

    FI -->|apply W1/W2/W3| API
    API -->|Pod events| PC
    PC --> EC
    EC --> CL
    CL -->|raw proposal| VA
    VA -->|validated action or escalate| IM
    IM -->|admitted attempt| SS
    SS -->|execute injected callback| AC
    AC --> API
    SS --> VR
    VR -->|Deployment → ReplicaSet → Pod| API
    SS -->|restore exact pre-action spec on failure| API
    SS --> AU
    PC --> AU
    PC --> PM
    PM --> GF
```

## Incident sequence

```mermaid
sequenceDiagram
    autonumber
    participant H as Run harness
    participant K as K3s API
    participant C as Pod controller
    participant E as Evidence collector
    participant L as Classifier
    participant V as Deterministic validator
    participant S as Safety service
    participant A as Injected action
    participant P as Pod verifier
    participant J as Audit JSONL

    H->>K: Inject workload fault
    K-->>C: Pod status: CrashLoopBackOff
    C->>E: Collect previous logs and Events
    E-->>C: Frozen incident evidence
    C->>L: Classify incident
    L-->>V: Raw proposal
    V-->>C: Final validated decision

    alt escalate or reject
        C->>J: LOGGED(result=escalated/rejected, attempt=0)
    else automated attempt admitted
        C->>S: Remediate(DetectionEvent, injected action)
        S->>J: DETECTED
        S->>K: Capture exact Deployment spec
        S->>J: SNAPSHOTTED
        S->>K: Capture pre-action Deployment Pod UIDs
        S->>J: REMEDIATING
        S->>A: Execute()
        A->>K: restart_pod or rollout_undo
        A-->>S: API call accepted
        S->>J: VERIFYING
        loop Poll every few seconds
            S->>P: Resolve replacement through ownership
            P->>K: Read Deployment, ReplicaSets, Pods
            K-->>P: Candidate Pod status
            P-->>S: Ready + named-container restartCount
        end
        alt Ready continuously for 60 seconds
            S->>J: RECOVERED, then LOGGED(result=recovered)
        else never Ready in 30s or stability breaks
            S->>J: ROLLING_BACK
            S->>K: Restore exact pre-action Deployment spec
            S->>J: ROLLED_BACK, then LOGGED(result=rolled_back)
        end
    end

    opt three failed attempts consumed
        C->>J: LOGGED(result=exhausted, attempt=3)
    end
```

## Control loop

```mermaid
flowchart TD
    D[CrashLoopBackOff detected] --> E[Freeze logs + Events]
    E --> C[Classify incident]
    C --> V{Validator decision}
    V -->|escalate| EL[LOGGED: escalated\nattempt 0]
    V -->|reject| RJ[LOGGED: rejected\nattempt 0]
    V -->|automate| B{Attempt budget available?}
    B -->|no| EX[LOGGED: exhausted\nattempt 3]
    B -->|yes| S[Capture Deployment snapshot]
    S --> U[Capture pre-action Pod UIDs]
    U --> A[Execute injected action]
    A --> R{Ready within 30s?}
    R -->|no| RB[Restore snapshot]
    R -->|yes| W{Same UID continuously healthy for 60s?}
    W -->|yes| OK[RECOVERED → LOGGED]
    W -->|no| RB
    RB --> RO[ROLLED_BACK → LOGGED]
    RO --> Q{Attempts remaining?}
    Q -->|yes, after backoff| C
    Q -->|no, next reconcile| EX
```

## Safety state machine

```mermaid
stateDiagram-v2
    [*] --> DETECTED
    DETECTED --> SNAPSHOTTED: exact Deployment spec captured
    SNAPSHOTTED --> REMEDIATING: pre-action UIDs captured
    REMEDIATING --> VERIFYING: Action.Execute returned
    VERIFYING --> RECOVERED: uninterrupted stability window passed
    VERIFYING --> ROLLING_BACK: readiness/stability failed
    ROLLING_BACK --> ROLLED_BACK: snapshot restored
    RECOVERED --> LOGGED
    ROLLED_BACK --> LOGGED
    LOGGED --> [*]
```

`exhausted`, `escalated`, and `rejected` are incident outcomes written as
`LOGGED` records. They are not new states in this attempt-level state machine.

## Deployment view

```mermaid
flowchart TB
    subgraph VM[Azure VM — intended demo host]
        subgraph K3S[K3s cluster]
            API[(API server)]
            subgraph SYS[k8s-selfheal-system]
                M[controller-manager Pod]
                PVC[(audit-data PVC\naudit.jsonl)]
                SM[ServiceMonitor]
            end
            subgraph WORK[default namespace]
                W1[W1 transient Deployment]
                W2[W2 fixable rollout Deployment]
                W3[W3 unrecoverable rollout Deployment]
            end
            PROM[Prometheus]
            GRAF[Grafana]
        end
    end

    M <--> API
    API <--> W1
    API <--> W2
    API <--> W3
    M --> PVC
    SM --> M
    PROM --> SM
    GRAF --> PROM
```

The diagram describes the repository deployment configuration. VM availability
must be checked separately and is not inferred from this source.


package classifier

import (
	"encoding/json"
	"fmt"
)

// BuildSystemPrompt returns the fixed instruction sent to Gemini.
func BuildSystemPrompt() string {
	return `
You are a Kubernetes CrashLoopBackOff triage classifier.

Your job is NOT to freely choose or invent a fix.

Your job is to:
1. classify the most likely CrashLoopBackOff sub-cause;
2. decide whether the incident is safe for automatic remediation;
3. propose an action only within the defined safety boundary.

Return JSON only with exactly these fields:
{
  "sub_cause": "...",
  "recommended_action": "...",
  "target": {
    "kind": "...",
    "namespace": "...",
    "name": "..."
  },
  "safe_for_automation": true,
  "reasoning": "..."
}

Allowed sub-causes:
- transient_failure
- bad_deploy
- bad_config
- application_panic
- oom_adjacent
- unknown

Executable allowlisted actions:
- restart_pod
- rollout_undo

Decision rules:

1. transient_failure:
   - safe_for_automation must be true
   - recommended_action must be restart_pod
   - target must be the affected Pod
   - target.kind MUST be "Pod"
   - target.namespace MUST exactly equal the incident namespace
   - target.name MUST exactly equal the incident pod_name

2. bad_deploy:
   - safe_for_automation must be true
   - recommended_action must be rollout_undo
   - target must be the owning Deployment
   - target.kind MUST be "Deployment"
   - target.namespace MUST exactly equal the incident namespace
   - target.name MUST exactly equal owner_deployment

3. bad_config:
   - safe_for_automation must be false
   - recommended_action must be escalate_to_human
   - do not propose an automatic configuration change

4. oom_adjacent:
   - safe_for_automation must be false
   - recommended_action must be escalate_to_human
   - do not automatically change CPU or memory limits

5. application_panic:
   - safe_for_automation must be false unless the evidence clearly
     shows a transient external dependency failure
   - when uncertain, escalate_to_human

6. unknown or insufficient evidence:
   - safe_for_automation must be false
   - recommended_action must be escalate_to_human

For escalate_to_human:
- target should identify the affected Pod using its exact namespace and pod_name

Never return an empty target field.
Never invent Kubernetes resources.
Never propose deleting namespaces, changing RBAC, modifying persistent data,
scaling to zero, changing resource limits, or editing configuration.
`
}

// BuildUserPrompt converts the incident input into formatted JSON.
func BuildUserPrompt(input IncidentInput) (string, error) {
	data, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return "", fmt.Errorf(
			"marshal incident input: %w",
			err,
		)
	}

	return fmt.Sprintf(
		`Classify the following Kubernetes incident.

Incident evidence:
%s

Canonical resource identifiers:

Affected Pod:
- namespace: %s
- name: %s

Owning Deployment:
- namespace: %s
- name: %s

If recommended_action is restart_pod, copy the exact affected Pod identifiers into target.
If recommended_action is rollout_undo, copy the exact owning Deployment identifiers into target.
Do not invent resource names.`,
		string(data),
		input.Namespace,
		input.PodName,
		input.Namespace,
		input.OwnerDeployment,
	), nil
}

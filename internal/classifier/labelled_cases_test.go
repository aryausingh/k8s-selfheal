package classifier

import (
	"testing"
	"time"
)

type labelledClassificationCase struct {
	Name string

	Input IncidentInput

	ExpectedSubCause          string
	ExpectedAction            string
	ExpectedSafeForAutomation bool
}

func labelledClassificationCases() []labelledClassificationCase {

	baseDetection := DetectionEvent{
		PodName:         "checkoutservice-abc123",
		Namespace:       "default",
		ContainerName:   "checkoutservice",
		RestartCount:    5,
		OwnerDeployment: "checkoutservice",
		Timestamp:       time.Now(),
	}

	return []labelledClassificationCase{

		{
			Name: "transient connection refused",

			Input: IncidentInput{
				DetectionEvent: baseDetection,
				Logs: `
connection refused while contacting payment service
temporary network failure
`,
				Events: []string{
					"Back-off restarting failed container",
					"Container terminated with exit code 1",
				},
			},

			ExpectedSubCause:          "transient_failure",
			ExpectedAction:            ActionRestartPod,
			ExpectedSafeForAutomation: true,
		},

		{
			Name: "transient timeout",

			Input: IncidentInput{
				DetectionEvent: baseDetection,
				Logs: `
request to payment service timed out
dependency temporarily unavailable
`,
				Events: []string{
					"Back-off restarting failed container",
				},
			},

			ExpectedSubCause:          "transient_failure",
			ExpectedAction:            ActionRestartPod,
			ExpectedSafeForAutomation: true,
		},

		{
			Name: "transient temporary dependency failure",

			Input: IncidentInput{
				DetectionEvent: baseDetection,
				Logs: `
temporary dependency failure
connection to backend was refused
`,
				Events: []string{
					"Container terminated with exit code 1",
				},
			},

			ExpectedSubCause:          "transient_failure",
			ExpectedAction:            ActionRestartPod,
			ExpectedSafeForAutomation: true,
		},

		{
			Name: "transient service unavailable",

			Input: IncidentInput{
				DetectionEvent: baseDetection,
				Logs: `
payment service temporarily unavailable
application will retry the dependency
`,
				Events: []string{
					"Back-off restarting failed container",
				},
			},

			ExpectedSubCause:          "transient_failure",
			ExpectedAction:            ActionRestartPod,
			ExpectedSafeForAutomation: true,
		},

		{
			Name: "bad deployment invalid image",

			Input: IncidentInput{
				DetectionEvent: baseDetection,
				Logs: `
failed to pull image "checkoutservice:invalid-version"
`,
				Events: []string{
					"Back-off pulling image",
					"ImagePullBackOff",
				},
			},

			ExpectedSubCause:          "bad_deploy",
			ExpectedAction:            ActionRolloutUndo,
			ExpectedSafeForAutomation: true,
		},

		{
			Name: "bad deployment image pull failure",

			Input: IncidentInput{
				DetectionEvent: baseDetection,
				Logs: `
failed to pull image for checkoutservice
invalid image reference
`,
				Events: []string{
					"ImagePullBackOff",
					"Back-off pulling image",
				},
			},

			ExpectedSubCause:          "bad_deploy",
			ExpectedAction:            ActionRolloutUndo,
			ExpectedSafeForAutomation: true,
		},

		{
			Name: "bad deployment invalid container image",

			Input: IncidentInput{
				DetectionEvent: baseDetection,
				Logs: `
invalid image was configured for the checkout container
`,
				Events: []string{
					"Back-off pulling image",
				},
			},

			ExpectedSubCause:          "bad_deploy",
			ExpectedAction:            ActionRolloutUndo,
			ExpectedSafeForAutomation: true,
		},

		{
			Name: "bad deployment registry pull failure",

			Input: IncidentInput{
				DetectionEvent: baseDetection,
				Logs: `
failed to pull image from registry
`,
				Events: []string{
					"ImagePullBackOff",
				},
			},

			ExpectedSubCause:          "bad_deploy",
			ExpectedAction:            ActionRolloutUndo,
			ExpectedSafeForAutomation: true,
		},

		{
			Name: "OOMKilled",

			Input: IncidentInput{
				DetectionEvent: baseDetection,
				Logs: `
container was terminated because it exceeded available memory
`,
				Events: []string{
					"OOMKilled",
					"Container terminated with exit code 137",
				},
			},

			ExpectedSubCause:          "oom_adjacent",
			ExpectedAction:            ActionEscalateToHuman,
			ExpectedSafeForAutomation: false,
		},

		{
			Name: "out of memory",

			Input: IncidentInput{
				DetectionEvent: baseDetection,
				Logs: `
application crashed because it ran out of memory
`,
				Events: []string{
					"Container terminated",
					"Out of memory",
				},
			},

			ExpectedSubCause:          "oom_adjacent",
			ExpectedAction:            ActionEscalateToHuman,
			ExpectedSafeForAutomation: false,
		},

		{
			Name: "memory limit exceeded",

			Input: IncidentInput{
				DetectionEvent: baseDetection,
				Logs: `
container exceeded its memory limit
`,
				Events: []string{
					"OOMKilled",
				},
			},

			ExpectedSubCause:          "oom_adjacent",
			ExpectedAction:            ActionEscalateToHuman,
			ExpectedSafeForAutomation: false,
		},

		{
			Name: "memory exhaustion",

			Input: IncidentInput{
				DetectionEvent: baseDetection,
				Logs: `
memory exhaustion caused the container to terminate
`,
				Events: []string{
					"Container terminated with exit code 137",
				},
			},

			ExpectedSubCause:          "oom_adjacent",
			ExpectedAction:            ActionEscalateToHuman,
			ExpectedSafeForAutomation: false,
		},

		{
			Name: "missing environment variable",

			Input: IncidentInput{
				DetectionEvent: baseDetection,
				Logs: `
application failed because required environment variable
PAYMENT_URL is missing
`,
				Events: []string{
					"Container terminated",
					"Back-off restarting failed container",
				},
			},

			ExpectedSubCause:          "bad_config",
			ExpectedAction:            ActionEscalateToHuman,
			ExpectedSafeForAutomation: false,
		},

		{
			Name: "invalid configuration",

			Input: IncidentInput{
				DetectionEvent: baseDetection,
				Logs: `
application failed because the configuration is invalid
`,
				Events: []string{
					"Back-off restarting failed container",
				},
			},

			ExpectedSubCause:          "bad_config",
			ExpectedAction:            ActionEscalateToHuman,
			ExpectedSafeForAutomation: false,
		},

		{
			Name: "missing config value",

			Input: IncidentInput{
				DetectionEvent: baseDetection,
				Logs: `
required configuration value is missing from the environment
`,
				Events: []string{
					"Container terminated",
				},
			},

			ExpectedSubCause:          "bad_config",
			ExpectedAction:            ActionEscalateToHuman,
			ExpectedSafeForAutomation: false,
		},

		{
			Name: "invalid configuration value",

			Input: IncidentInput{
				DetectionEvent: baseDetection,
				Logs: `
application failed because PAYMENT_URL contains an invalid configuration value
`,
				Events: []string{
					"Back-off restarting failed container",
					"Container terminated",
				},
			},

			ExpectedSubCause:          "bad_config",
			ExpectedAction:            ActionEscalateToHuman,
			ExpectedSafeForAutomation: false,
		},

		{
			Name: "application panic",

			Input: IncidentInput{
				DetectionEvent: baseDetection,
				Logs: `
panic: index out of range
goroutine 1 [running]:
main.startApplication()
`,
				Events: []string{
					"Back-off restarting failed container",
				},
			},

			ExpectedSubCause:          "application_panic",
			ExpectedAction:            ActionEscalateToHuman,
			ExpectedSafeForAutomation: false,
		},

		{
			Name: "application runtime panic",

			Input: IncidentInput{
				DetectionEvent: baseDetection,
				Logs: `
panic: unexpected nil pointer dereference
goroutine 1 [running]
`,
				Events: []string{
					"Container terminated with exit code 2",
				},
			},

			ExpectedSubCause:          "application_panic",
			ExpectedAction:            ActionEscalateToHuman,
			ExpectedSafeForAutomation: false,
		},

		{
			Name: "unknown insufficient evidence",

			Input: IncidentInput{
				DetectionEvent: baseDetection,
				Logs: `
application stopped unexpectedly
`,
				Events: []string{
					"Back-off restarting failed container",
				},
			},

			ExpectedSubCause:          "unknown",
			ExpectedAction:            ActionEscalateToHuman,
			ExpectedSafeForAutomation: false,
		},

		{
			Name: "unknown generic failure",

			Input: IncidentInput{
				DetectionEvent: baseDetection,
				Logs: `
container exited unexpectedly and the cause is unclear
`,
				Events: []string{
					"Back-off restarting failed container",
				},
			},

			ExpectedSubCause:          "unknown",
			ExpectedAction:            ActionEscalateToHuman,
			ExpectedSafeForAutomation: false,
		},
	}
}

func TestLabelledClassificationCasesIntegrity(t *testing.T) {
	cases := labelledClassificationCases()

	if len(cases) != 20 {
		t.Fatalf("expected exactly 20 labelled classification cases, got %d", len(cases))
	}

	names := make(map[string]struct{}, len(cases))

	for i, tc := range cases {
		if tc.Name == "" {
			t.Errorf("case %d has empty name", i)
		}

		if _, exists := names[tc.Name]; exists {
			t.Errorf("case %d has duplicate name %q", i, tc.Name)
		}
		names[tc.Name] = struct{}{}

		if tc.Input.DetectionEvent.PodName == "" {
			t.Errorf("case %q has empty pod name", tc.Name)
		}

		if tc.Input.DetectionEvent.Namespace == "" {
			t.Errorf("case %q has empty namespace", tc.Name)
		}

		if tc.Input.DetectionEvent.ContainerName == "" {
			t.Errorf("case %q has empty container name", tc.Name)
		}

		if tc.Input.Logs == "" && len(tc.Input.Events) == 0 {
			t.Errorf("case %q has no logs or events", tc.Name)
		}

		if tc.ExpectedSubCause == "" {
			t.Errorf("case %q has empty expected sub-cause", tc.Name)
		}

		if tc.ExpectedAction == "" {
			t.Errorf("case %q has empty expected action", tc.Name)
		}
	}
}

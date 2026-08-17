package contracts

import "time"

// DetectionEvent is emitted by the operator (Owner 1) the moment a Pod is
// confirmed to be in CrashLoopBackOff. Owner 2's safety layer consumes it.
type DetectionEvent struct {
	PodName         string
	Namespace       string
	ContainerName   string // which container is crash-looping — verify THIS one's restart count, not the pod as a whole
	RestartCount    int32
	OwnerDeployment string // empty if not Deployment-owned
	Timestamp       time.Time
}

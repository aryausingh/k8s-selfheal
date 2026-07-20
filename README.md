# k8s-selfheal
A Kubernetes self-healing controller that safely auto-remediates CrashLoopBackOff, and — unlike existing tools that stop at suggestions or gate on humans — verifies recovery actually held and automatically rolls back when it didn't. We build and measure the safety layer production tools leave out, on one failure class, deeply.

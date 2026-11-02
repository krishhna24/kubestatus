// Package health turns Kubernetes Deployment and Pod objects into health
// verdicts and human-readable diagnostics.
//
// Everything in this package is pure: it only reads API objects that were
// fetched elsewhere, so it can be tested without a cluster.
package health

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

// Status is the overall verdict for a Deployment.
type Status string

const (
	// Healthy means every desired replica is ready and available, and no
	// failure condition is reported.
	Healthy Status = "HEALTHY"
	// Degraded means at least one replica is ready but the Deployment is not
	// fully healthy.
	Degraded Status = "DEGRADED"
	// Unhealthy means replicas are desired but none of them is ready.
	Unhealthy Status = "UNHEALTHY"
)

// DeploymentHealth is the evaluated state of one Deployment.
type DeploymentHealth struct {
	Name string

	// Replica counts. Desired comes from the spec, the rest from status.
	Desired   int32
	Ready     int32
	Available int32
	Updated   int32

	Status Status

	// Issues are failure signals reported by the Deployment controller or
	// problems found while inspecting the Deployment (such as a bad selector).
	Issues []string
	// Notes are informational, such as an in-progress rollout.
	Notes []string

	// Pods holds diagnostics for the Pods matched by the Deployment's
	// selector. It is nil when Pod information could not be retrieved.
	Pods []PodDiagnosis
}

// Report is the result of inspecting one namespace.
type Report struct {
	Namespace   string
	Deployments []DeploymentHealth
	// PodsErr is set when Pods could not be listed. Deployment health is
	// still valid in that case because it comes from Deployment status.
	PodsErr error
}

// Summary counts Deployments by status.
type Summary struct {
	Total, Healthy, Degraded, Unhealthy int
}

// Summary tallies the Deployments in the report.
func (r Report) Summary() Summary {
	s := Summary{Total: len(r.Deployments)}
	for _, d := range r.Deployments {
		switch d.Status {
		case Healthy:
			s.Healthy++
		case Degraded:
			s.Degraded++
		case Unhealthy:
			s.Unhealthy++
		}
	}
	return s
}

// DesiredReplicas returns spec.replicas, applying the API default of 1 when
// the field is unset.
func DesiredReplicas(d *appsv1.Deployment) int32 {
	if d.Spec.Replicas == nil {
		return 1
	}
	return *d.Spec.Replicas
}

// ClassifyDeployment evaluates a Deployment's replica counts and conditions.
//
// Rules, in order:
//   - desired == 0                                   -> HEALTHY
//   - ready >= desired, available >= desired,
//     and no failure condition                       -> HEALTHY
//   - ready > 0                                      -> DEGRADED
//   - otherwise                                      -> UNHEALTHY
//
// Readiness (the Pod passes its readiness probe) and availability (the Pod
// has been ready for at least minReadySeconds) are checked separately because
// a Pod can be ready without yet being available.
func ClassifyDeployment(d *appsv1.Deployment) DeploymentHealth {
	h := DeploymentHealth{
		Name:      d.Name,
		Desired:   DesiredReplicas(d),
		Ready:     d.Status.ReadyReplicas,
		Available: d.Status.AvailableReplicas,
		Updated:   d.Status.UpdatedReplicas,
	}

	h.Issues = failureConditions(d)

	if d.Spec.Paused {
		h.Notes = append(h.Notes, "rollout is paused")
	}
	if d.Status.ObservedGeneration < d.Generation {
		h.Notes = append(h.Notes, "controller has not yet observed the latest spec")
	} else if h.Desired > 0 && h.Updated < h.Desired {
		h.Notes = append(h.Notes, fmt.Sprintf("rollout in progress: %d/%d replicas updated", h.Updated, h.Desired))
	}

	switch {
	case h.Desired == 0:
		h.Status = Healthy
	case h.Ready >= h.Desired && h.Available >= h.Desired && len(h.Issues) == 0:
		h.Status = Healthy
	case h.Ready > 0:
		h.Status = Degraded
	default:
		h.Status = Unhealthy
	}
	return h
}

// failureConditions extracts Deployment conditions that signal a failure:
// Progressing=False (for example ProgressDeadlineExceeded) and
// ReplicaFailure=True (for example a quota or admission rejection).
func failureConditions(d *appsv1.Deployment) []string {
	var issues []string
	for _, c := range d.Status.Conditions {
		failed := (c.Type == appsv1.DeploymentProgressing && c.Status == corev1.ConditionFalse) ||
			(c.Type == appsv1.DeploymentReplicaFailure && c.Status == corev1.ConditionTrue)
		if !failed {
			continue
		}
		msg := fmt.Sprintf("%s=%s", c.Type, c.Status)
		if c.Reason != "" {
			msg += " (" + c.Reason + ")"
		}
		if c.Message != "" {
			msg += ": " + c.Message
		}
		issues = append(issues, msg)
	}
	return issues
}

// PodDiagnosis is the evaluated state of one Pod.
type PodDiagnosis struct {
	Name string
	// Phase is the raw Pod phase reported by the kubelet.
	Phase corev1.PodPhase
	// Reason is the most specific short explanation of the Pod's state, in
	// the spirit of kubectl's STATUS column (e.g. CrashLoopBackOff).
	Reason          string
	ReadyContainers int
	TotalContainers int
	Restarts        int32
	// Problems are detailed, per-container explanations.
	Problems []string
	// Healthy is true when the Pod is Running with every container ready.
	Healthy bool
}

// DiagnosePod interprets a Pod's phase, scheduling condition and container
// statuses.
func DiagnosePod(p *corev1.Pod) PodDiagnosis {
	d := PodDiagnosis{
		Name:            p.Name,
		Phase:           p.Status.Phase,
		TotalContainers: len(p.Spec.Containers),
	}

	var reasons []string // candidate Reason values, most specific first

	if p.DeletionTimestamp != nil {
		reasons = append(reasons, "Terminating")
	}

	if p.Status.Phase == corev1.PodFailed {
		r := p.Status.Reason
		if r == "" {
			r = "Failed"
		}
		reasons = append(reasons, r)
		if p.Status.Message != "" {
			d.Problems = append(d.Problems, "pod failed: "+p.Status.Message)
		}
	}

	unscheduled := false
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse {
			unscheduled = true
			r := c.Reason
			if r == "" {
				r = "Unschedulable"
			}
			reasons = append(reasons, r)
			msg := "not scheduled"
			if c.Message != "" {
				msg += ": " + c.Message
			}
			d.Problems = append(d.Problems, msg)
		}
	}

	// Init containers run before the app containers; a failure here explains
	// why the app containers never started.
	for _, cs := range p.Status.InitContainerStatuses {
		if r, problem := initContainerProblem(cs); problem != "" {
			reasons = append(reasons, "Init:"+r)
			d.Problems = append(d.Problems, problem)
		}
	}

	statuses := make(map[string]corev1.ContainerStatus, len(p.Status.ContainerStatuses))
	for _, cs := range p.Status.ContainerStatuses {
		statuses[cs.Name] = cs
	}

	if len(p.Status.ContainerStatuses) == 0 && !unscheduled && p.Status.Phase != corev1.PodFailed {
		d.Problems = append(d.Problems, "no container status reported yet")
	}

	for _, c := range p.Spec.Containers {
		cs, ok := statuses[c.Name]
		if !ok {
			if len(p.Status.ContainerStatuses) > 0 {
				d.Problems = append(d.Problems, fmt.Sprintf("container %q: no status reported", c.Name))
			}
			continue
		}
		d.Restarts += cs.RestartCount
		if cs.Ready {
			d.ReadyContainers++
			continue
		}
		r, problem := containerProblem(cs)
		if r != "" {
			reasons = append(reasons, r)
		}
		if problem != "" {
			d.Problems = append(d.Problems, problem)
		}
	}

	d.Healthy = p.Status.Phase == corev1.PodRunning &&
		p.DeletionTimestamp == nil &&
		d.TotalContainers > 0 &&
		d.ReadyContainers == d.TotalContainers

	switch {
	case len(reasons) > 0:
		d.Reason = reasons[0]
	case p.Status.Phase == "":
		d.Reason = "Unknown"
	default:
		d.Reason = string(p.Status.Phase)
	}
	return d
}

// containerProblem explains why a non-ready app container is not ready.
// It returns a short reason and a detailed message.
func containerProblem(cs corev1.ContainerStatus) (reason, problem string) {
	last := lastTermination(cs)
	switch {
	case cs.State.Waiting != nil:
		w := cs.State.Waiting
		reason = w.Reason
		if reason == "" {
			reason = "Waiting"
		}
		problem = fmt.Sprintf("container %q waiting: %s", cs.Name, reason)
		if w.Message != "" {
			problem += " - " + w.Message
		}
		problem += last
	case cs.State.Terminated != nil:
		t := cs.State.Terminated
		reason = t.Reason
		if reason == "" {
			reason = "Terminated"
		}
		problem = fmt.Sprintf("container %q terminated: %s (exit code %d)", cs.Name, reason, t.ExitCode)
	case cs.State.Running != nil:
		reason = "NotReady"
		problem = fmt.Sprintf("container %q running but not ready (readiness probe not passing)", cs.Name)
		problem += last
	default:
		reason = "Unknown"
		problem = fmt.Sprintf("container %q: state not reported", cs.Name)
	}
	return reason, problem
}

// initContainerProblem reports a failing or stuck init container. A completed
// init container, or one that is simply still running, is not a problem.
func initContainerProblem(cs corev1.ContainerStatus) (reason, problem string) {
	switch {
	case cs.State.Waiting != nil && cs.State.Waiting.Reason != "" && cs.State.Waiting.Reason != "PodInitializing":
		reason = cs.State.Waiting.Reason
		problem = fmt.Sprintf("init container %q waiting: %s", cs.Name, reason)
		if cs.State.Waiting.Message != "" {
			problem += " - " + cs.State.Waiting.Message
		}
		problem += lastTermination(cs)
	case cs.State.Terminated != nil && cs.State.Terminated.ExitCode != 0:
		reason = cs.State.Terminated.Reason
		if reason == "" {
			reason = "Error"
		}
		problem = fmt.Sprintf("init container %q failed: %s (exit code %d)", cs.Name, reason, cs.State.Terminated.ExitCode)
	}
	return reason, problem
}

// lastTermination describes the previous termination of a restarted
// container, which is usually the real cause behind a CrashLoopBackOff.
func lastTermination(cs corev1.ContainerStatus) string {
	t := cs.LastTerminationState.Terminated
	if t == nil {
		return ""
	}
	reason := t.Reason
	if reason == "" {
		reason = "Terminated"
	}
	return fmt.Sprintf("; last terminated: %s (exit code %d)", reason, t.ExitCode)
}

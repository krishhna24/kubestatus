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

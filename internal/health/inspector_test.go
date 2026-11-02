package health

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func ptr[T any](v T) *T { return &v }

// deployment builds a Deployment whose controller has observed the latest
// spec and whose replicas are all updated, so tests only need to vary the
// fields they care about.
func deployment(desired *int32, ready, available int32, conds ...appsv1.DeploymentCondition) *appsv1.Deployment {
	updated := int32(1)
	if desired != nil {
		updated = *desired
	}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Generation: 2},
		Spec:       appsv1.DeploymentSpec{Replicas: desired},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 2,
			ReadyReplicas:      ready,
			AvailableReplicas:  available,
			UpdatedReplicas:    updated,
			Conditions:         conds,
		},
	}
}

func TestClassifyDeployment(t *testing.T) {
	deadline := appsv1.DeploymentCondition{
		Type:    appsv1.DeploymentProgressing,
		Status:  corev1.ConditionFalse,
		Reason:  "ProgressDeadlineExceeded",
		Message: `ReplicaSet "web-7d4b" has timed out progressing.`,
	}
	quota := appsv1.DeploymentCondition{
		Type:    appsv1.DeploymentReplicaFailure,
		Status:  corev1.ConditionTrue,
		Reason:  "FailedCreate",
		Message: "exceeded quota",
	}
	progressingOK := appsv1.DeploymentCondition{
		Type:   appsv1.DeploymentProgressing,
		Status: corev1.ConditionTrue,
		Reason: "NewReplicaSetAvailable",
	}

	tests := []struct {
		name       string
		d          *appsv1.Deployment
		want       Status
		wantDesire int32
		wantIssues int
	}{
		{"fully healthy", deployment(ptr[int32](3), 3, 3, progressingOK), Healthy, 3, 0},
		{"partially ready", deployment(ptr[int32](3), 2, 2), Degraded, 3, 0},
		{"no replicas ready", deployment(ptr[int32](2), 0, 0), Unhealthy, 2, 0},
		{"scaled to zero", deployment(ptr[int32](0), 0, 0), Healthy, 0, 0},
		{"nil replicas defaults to one, ready", deployment(nil, 1, 1), Healthy, 1, 0},
		{"nil replicas defaults to one, not ready", deployment(nil, 0, 0), Unhealthy, 1, 0},
		// Ready but not yet available (minReadySeconds not elapsed) must not
		// be reported as healthy: readiness and availability are distinct.
		{"ready but unavailable", deployment(ptr[int32](3), 3, 1), Degraded, 3, 0},
		{"all ready with progress deadline exceeded", deployment(ptr[int32](2), 2, 2, deadline), Degraded, 2, 1},
		{"none ready with replica failure", deployment(ptr[int32](2), 0, 0, quota), Unhealthy, 2, 1},
		{"both failure conditions", deployment(ptr[int32](2), 1, 1, deadline, quota), Degraded, 2, 2},
		// Surge during a rollout can make ready exceed desired.
		{"more ready than desired", deployment(ptr[int32](2), 3, 3), Healthy, 2, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyDeployment(tt.d)
			if got.Status != tt.want {
				t.Errorf("Status = %s, want %s", got.Status, tt.want)
			}
			if got.Desired != tt.wantDesire {
				t.Errorf("Desired = %d, want %d", got.Desired, tt.wantDesire)
			}
			if len(got.Issues) != tt.wantIssues {
				t.Errorf("Issues = %q, want %d issue(s)", got.Issues, tt.wantIssues)
			}
		})
	}
}

func TestClassifyDeploymentIssueText(t *testing.T) {
	d := deployment(ptr[int32](1), 0, 0, appsv1.DeploymentCondition{
		Type:    appsv1.DeploymentProgressing,
		Status:  corev1.ConditionFalse,
		Reason:  "ProgressDeadlineExceeded",
		Message: "timed out",
	})
	got := ClassifyDeployment(d).Issues
	want := "Progressing=False (ProgressDeadlineExceeded): timed out"
	if len(got) != 1 || got[0] != want {
		t.Fatalf("Issues = %q, want [%q]", got, want)
	}
}

func TestClassifyDeploymentNotes(t *testing.T) {
	stale := deployment(ptr[int32](2), 2, 2)
	stale.Generation = 3

	rolling := deployment(ptr[int32](4), 4, 4)
	rolling.Status.UpdatedReplicas = 1

	paused := deployment(ptr[int32](1), 1, 1)
	paused.Spec.Paused = true

	tests := []struct {
		name string
		d    *appsv1.Deployment
		want string
	}{
		{"stale generation", stale, "not yet observed"},
		{"rollout in progress", rolling, "1/4 replicas updated"},
		{"paused", paused, "paused"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			notes := strings.Join(ClassifyDeployment(tt.d).Notes, "|")
			if !strings.Contains(notes, tt.want) {
				t.Errorf("Notes = %q, want substring %q", notes, tt.want)
			}
		})
	}
}

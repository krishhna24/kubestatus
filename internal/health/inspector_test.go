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

// pod builds a Pod with the given phase and one app container per status.
func pod(phase corev1.PodPhase, statuses ...corev1.ContainerStatus) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "web-abc"},
		Status: corev1.PodStatus{
			Phase:             phase,
			ContainerStatuses: statuses,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodScheduled, Status: corev1.ConditionTrue},
			},
		},
	}
	for _, s := range statuses {
		p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: s.Name})
	}
	return p
}

func running(name string, ready bool) corev1.ContainerStatus {
	return corev1.ContainerStatus{
		Name:  name,
		Ready: ready,
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	}
}

func waiting(name, reason, msg string) corev1.ContainerStatus {
	return corev1.ContainerStatus{
		Name:  name,
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason, Message: msg}},
	}
}

func TestDiagnosePod(t *testing.T) {
	crashLoop := waiting("app", "CrashLoopBackOff", "back-off 40s restarting failed container")
	crashLoop.RestartCount = 4
	crashLoop.LastTerminationState.Terminated = &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1}

	oomLoop := waiting("app", "CrashLoopBackOff", "")
	oomLoop.RestartCount = 2
	oomLoop.LastTerminationState.Terminated = &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137}

	unschedulable := pod(corev1.PodPending)
	unschedulable.Spec.Containers = []corev1.Container{{Name: "app"}}
	unschedulable.Status.Conditions = []corev1.PodCondition{{
		Type:    corev1.PodScheduled,
		Status:  corev1.ConditionFalse,
		Reason:  "Unschedulable",
		Message: "0/1 nodes are available: 1 Insufficient cpu.",
	}}

	evicted := pod(corev1.PodFailed)
	evicted.Spec.Containers = []corev1.Container{{Name: "app"}}
	evicted.Status.Reason = "Evicted"
	evicted.Status.Message = "The node was low on resource: memory."

	failedNoReason := pod(corev1.PodFailed)
	failedNoReason.Spec.Containers = []corev1.Container{{Name: "app"}}

	noStatuses := pod(corev1.PodPending)
	noStatuses.Spec.Containers = []corev1.Container{{Name: "app"}, {Name: "sidecar"}}

	partialStatuses := pod(corev1.PodRunning, running("app", true))
	partialStatuses.Spec.Containers = append(partialStatuses.Spec.Containers, corev1.Container{Name: "sidecar"})

	initCrash := pod(corev1.PodPending, waiting("app", "PodInitializing", ""))
	initCrash.Status.InitContainerStatuses = []corev1.ContainerStatus{
		{Name: "migrate", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}},
	}

	initDone := pod(corev1.PodRunning, running("app", true))
	initDone.Status.InitContainerStatuses = []corev1.ContainerStatus{
		{Name: "migrate", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Completed"}}},
	}

	terminating := pod(corev1.PodRunning, running("app", true))
	terminating.DeletionTimestamp = &metav1.Time{}

	tests := []struct {
		name        string
		p           *corev1.Pod
		wantReason  string
		wantReady   int
		wantTotal   int
		wantHealthy bool
		wantProblem string // substring expected in the joined problems; "" means no problems
	}{
		{"running and ready", pod(corev1.PodRunning, running("app", true), running("sidecar", true)), "Running", 2, 2, true, ""},
		{"running but not ready", pod(corev1.PodRunning, running("app", true), running("sidecar", false)), "NotReady", 1, 2, false, `"sidecar" running but not ready`},
		{"crash loop shows last exit", pod(corev1.PodRunning, crashLoop), "CrashLoopBackOff", 0, 1, false, "last terminated: Error (exit code 1)"},
		{"crash loop caused by OOM", pod(corev1.PodRunning, oomLoop), "CrashLoopBackOff", 0, 1, false, "OOMKilled (exit code 137)"},
		{"image pull backoff", pod(corev1.PodPending, waiting("app", "ImagePullBackOff", `Back-off pulling image "nginx:nope"`)), "ImagePullBackOff", 0, 1, false, "nginx:nope"},
		{"err image pull", pod(corev1.PodPending, waiting("app", "ErrImagePull", "")), "ErrImagePull", 0, 1, false, "ErrImagePull"},
		{"config error", pod(corev1.PodPending, waiting("app", "CreateContainerConfigError", `secret "db" not found`)), "CreateContainerConfigError", 0, 1, false, `secret "db" not found`},
		{"pending unschedulable", unschedulable, "Unschedulable", 0, 1, false, "Insufficient cpu"},
		{"failed evicted", evicted, "Evicted", 0, 1, false, "low on resource"},
		{"failed without reason", failedNoReason, "Failed", 0, 1, false, ""},
		{"missing container statuses", noStatuses, "Pending", 0, 2, false, "no container status reported yet"},
		{"one container status missing", partialStatuses, "Running", 1, 2, false, `"sidecar": no status reported`},
		{"init container crash", initCrash, "Init:CrashLoopBackOff", 0, 1, false, `init container "migrate" waiting: CrashLoopBackOff`},
		{"completed init container is fine", initDone, "Running", 1, 1, true, ""},
		{"terminating", terminating, "Terminating", 1, 1, false, ""},
		{"empty phase", &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "x"}}, "Unknown", 0, 0, false, "no container status reported yet"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DiagnosePod(tt.p)
			if got.Reason != tt.wantReason {
				t.Errorf("Reason = %q, want %q", got.Reason, tt.wantReason)
			}
			if got.ReadyContainers != tt.wantReady || got.TotalContainers != tt.wantTotal {
				t.Errorf("containers = %d/%d, want %d/%d", got.ReadyContainers, got.TotalContainers, tt.wantReady, tt.wantTotal)
			}
			if got.Healthy != tt.wantHealthy {
				t.Errorf("Healthy = %v, want %v", got.Healthy, tt.wantHealthy)
			}
			problems := strings.Join(got.Problems, "\n")
			if tt.wantProblem == "" && problems != "" {
				t.Errorf("unexpected problems: %q", got.Problems)
			}
			if tt.wantProblem != "" && !strings.Contains(problems, tt.wantProblem) {
				t.Errorf("Problems = %q, want substring %q", got.Problems, tt.wantProblem)
			}
		})
	}
}

func TestDiagnosePodSumsRestarts(t *testing.T) {
	a := running("app", true)
	a.RestartCount = 2
	b := running("sidecar", true)
	b.RestartCount = 3
	if got := DiagnosePod(pod(corev1.PodRunning, a, b)).Restarts; got != 5 {
		t.Errorf("Restarts = %d, want 5", got)
	}
}

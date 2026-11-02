package report

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/krishhna24/kubestatus/internal/health"
)

func sampleReport() health.Report {
	return health.Report{
		Namespace: "shop",
		Deployments: []health.DeploymentHealth{
			{
				Name: "api", Desired: 2, Ready: 2, Available: 2, Updated: 2, Status: health.Healthy,
				Pods: []health.PodDiagnosis{
					{Name: "api-1", Phase: corev1.PodRunning, Reason: "Running", ReadyContainers: 1, TotalContainers: 1, Healthy: true},
					{Name: "api-2", Phase: corev1.PodRunning, Reason: "Running", ReadyContainers: 1, TotalContainers: 1, Healthy: true},
				},
			},
			{
				Name: "frontend", Desired: 2, Ready: 0, Available: 0, Updated: 2, Status: health.Unhealthy,
				Issues: []string{"Progressing=False (ProgressDeadlineExceeded): timed out"},
				Pods: []health.PodDiagnosis{{
					Name: "frontend-x", Phase: corev1.PodRunning, Reason: "CrashLoopBackOff",
					TotalContainers: 1, Restarts: 7,
					Problems: []string{`container "app" waiting: CrashLoopBackOff; last terminated: Error (exit code 1)`},
				}},
			},
			{Name: "orphan", Desired: 1, Status: health.Unhealthy, Pods: []health.PodDiagnosis{}},
		},
	}
}

func render(t *testing.T, r health.Report, opts Options) string {
	t.Helper()
	var buf bytes.Buffer
	if err := Write(&buf, r, opts); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return buf.String()
}

func TestWrite(t *testing.T) {
	out := render(t, sampleReport(), Options{})

	for _, want := range []string{
		"KUBESTATUS | Namespace: shop",
		"DEPLOYMENT   READY   AVAILABLE   UP-TO-DATE   STATUS",
		"frontend     0/2     0           2            UNHEALTHY",
		"frontend: [issue] Progressing=False (ProgressDeadlineExceeded): timed out",
		"frontend (UNHEALTHY)",
		"frontend-x   Running   0/1     7          CrashLoopBackOff",
		"last terminated: Error (exit code 1)",
		"orphan (UNHEALTHY)\n  no Pods match this Deployment's selector",
		"Deployments: 3",
		"Healthy:     1",
		"Unhealthy:   2",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n--- output ---\n%s", want, out)
		}
	}
	// Pods of a healthy Deployment are hidden by default.
	if strings.Contains(out, "api-1") {
		t.Errorf("healthy pod shown without --all-pods:\n%s", out)
	}
}

func TestWriteAllPods(t *testing.T) {
	out := render(t, sampleReport(), Options{AllPods: true})
	if !strings.Contains(out, "api (HEALTHY)") || !strings.Contains(out, "api-2") {
		t.Errorf("--all-pods did not show healthy pods:\n%s", out)
	}
}

func TestWriteShowsUnhealthyPodOfHealthyDeployment(t *testing.T) {
	r := sampleReport()
	r.Deployments[0].Pods[1] = health.PodDiagnosis{Name: "api-surge", Phase: corev1.PodPending, Reason: "ImagePullBackOff", TotalContainers: 1}
	out := render(t, r, Options{})
	if !strings.Contains(out, "api-surge") || strings.Contains(out, "api-1") {
		t.Errorf("expected only the unhealthy pod of a healthy deployment:\n%s", out)
	}
}

func TestWritePodsUnavailable(t *testing.T) {
	r := sampleReport()
	r.PodsErr = errors.New("permission denied listing pods")
	for i := range r.Deployments {
		r.Deployments[i].Pods = nil
	}
	out := render(t, r, Options{})
	if !strings.Contains(out, "POD DIAGNOSTICS\n  unavailable: permission denied listing pods") {
		t.Errorf("missing pod-unavailable notice:\n%s", out)
	}
	if !strings.Contains(out, "SUMMARY") {
		t.Errorf("summary missing when pods are unavailable:\n%s", out)
	}
}

func TestWriteEmptyNamespace(t *testing.T) {
	out := render(t, health.Report{Namespace: "empty"}, Options{})
	if !strings.Contains(out, `No Deployments found in namespace "empty".`) {
		t.Errorf("unexpected output:\n%s", out)
	}
	if strings.Contains(out, "SUMMARY") {
		t.Errorf("summary should be omitted for an empty namespace:\n%s", out)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

func TestWritePropagatesWriterErrors(t *testing.T) {
	if err := Write(failingWriter{}, sampleReport(), Options{}); err == nil {
		t.Fatal("expected write error")
	}
}

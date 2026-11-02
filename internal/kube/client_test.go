package kube

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/krishhna24/kubestatus/internal/health"
)

const ns = "shop"

func ptr[T any](v T) *T { return &v }

func newDeployment(name string, replicas, ready int32, sel *metav1.LabelSelector) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       appsv1.DeploymentSpec{Replicas: ptr(replicas), Selector: sel},
		Status: appsv1.DeploymentStatus{
			ReadyReplicas:     ready,
			AvailableReplicas: ready,
			UpdatedReplicas:   replicas,
		},
	}
}

func newPod(name, namespace string, lbls map[string]string, ready bool) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: lbls},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:  "app",
				Ready: ready,
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
			}},
		},
	}
}

func matchLabels(kv ...string) *metav1.LabelSelector {
	m := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return &metav1.LabelSelector{MatchLabels: m}
}

func podNames(d health.DeploymentHealth) []string {
	var names []string
	for _, p := range d.Pods {
		names = append(names, p.Name)
	}
	return names
}

func byName(t *testing.T, r health.Report, name string) health.DeploymentHealth {
	t.Helper()
	for _, d := range r.Deployments {
		if d.Name == name {
			return d
		}
	}
	t.Fatalf("deployment %q not in report", name)
	return health.DeploymentHealth{}
}

func TestInspectMatchesPodsBySelector(t *testing.T) {
	// Same selector, different namespace: must not be listed.
	elsewhere := newDeployment("elsewhere", 1, 0, matchLabels("app", "web"))
	elsewhere.Namespace = "other"

	cs := fake.NewClientset(
		newDeployment("web", 2, 1, matchLabels("app", "web")),
		newDeployment("api", 1, 1, &metav1.LabelSelector{
			MatchExpressions: []metav1.LabelSelectorRequirement{
				{Key: "app", Operator: metav1.LabelSelectorOpIn, Values: []string{"api", "api-v2"}},
			},
		}),
		elsewhere,
		// Pod names deliberately do not share the Deployment's name prefix:
		// matching must come from labels only.
		newPod("zzz-1", ns, map[string]string{"app": "web"}, true),
		newPod("aaa-2", ns, map[string]string{"app": "web", "extra": "x"}, false),
		newPod("api-x", ns, map[string]string{"app": "api-v2"}, true),
		newPod("web-unrelated", ns, map[string]string{"app": "worker"}, true),
		// Same labels but another namespace: must not be matched.
		newPod("web-elsewhere", "other", map[string]string{"app": "web"}, true),
	)

	r, err := Inspect(context.Background(), cs, ns)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if r.PodsErr != nil {
		t.Fatalf("PodsErr: %v", r.PodsErr)
	}
	if len(r.Deployments) != 2 {
		t.Fatalf("got %d deployments, want 2", len(r.Deployments))
	}
	// Deployments are sorted by name.
	if r.Deployments[0].Name != "api" || r.Deployments[1].Name != "web" {
		t.Errorf("order = %s, %s; want api, web", r.Deployments[0].Name, r.Deployments[1].Name)
	}

	web := byName(t, r, "web")
	if got := strings.Join(podNames(web), ","); got != "aaa-2,zzz-1" {
		t.Errorf("web pods = %s, want aaa-2,zzz-1", got)
	}
	if web.Status != health.Degraded {
		t.Errorf("web status = %s, want DEGRADED", web.Status)
	}
	if got := strings.Join(podNames(byName(t, r, "api")), ","); got != "api-x" {
		t.Errorf("api pods = %s, want api-x", got)
	}

	// Exactly one list per resource type, both namespace-scoped, no writes.
	actions := cs.Actions()
	if len(actions) != 2 {
		t.Fatalf("got %d API actions, want 2: %v", len(actions), actions)
	}
	for _, a := range actions {
		if a.GetVerb() != "list" || a.GetNamespace() != ns {
			t.Errorf("unexpected action %s %s in namespace %q", a.GetVerb(), a.GetResource().Resource, a.GetNamespace())
		}
	}
}

func TestInspectEmptyNamespaceSkipsPodList(t *testing.T) {
	cs := fake.NewClientset()
	r, err := Inspect(context.Background(), cs, ns)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(r.Deployments) != 0 {
		t.Errorf("got %d deployments, want 0", len(r.Deployments))
	}
	if n := len(cs.Actions()); n != 1 {
		t.Errorf("got %d API actions, want 1 (no pod list when there are no deployments)", n)
	}
}

func TestInspectSelectorProblems(t *testing.T) {
	cs := fake.NewClientset(
		newDeployment("nil-selector", 1, 1, nil),
		newDeployment("empty-selector", 1, 1, &metav1.LabelSelector{}),
		newDeployment("bad-selector", 1, 1, &metav1.LabelSelector{
			MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "app", Operator: "Bogus"}},
		}),
		newPod("p", ns, map[string]string{"app": "web"}, true),
	)
	r, err := Inspect(context.Background(), cs, ns)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	for _, tc := range []struct{ name, want string }{
		{"nil-selector", "empty label selector"},
		{"empty-selector", "empty label selector"},
		{"bad-selector", "invalid label selector"},
	} {
		d := byName(t, r, tc.name)
		if len(d.Pods) != 0 {
			t.Errorf("%s: matched %d pods, want 0", tc.name, len(d.Pods))
		}
		if d.Pods == nil {
			t.Errorf("%s: Pods is nil, want empty (pods were listed successfully)", tc.name)
		}
		if !strings.Contains(strings.Join(d.Issues, "|"), tc.want) {
			t.Errorf("%s: issues = %q, want substring %q", tc.name, d.Issues, tc.want)
		}
	}
}

func TestInspectAPIErrors(t *testing.T) {
	gr := schema.GroupResource{Group: "apps", Resource: "deployments"}
	tests := []struct {
		name     string
		err      error
		wantText string
		check    func(error) bool
	}{
		{"forbidden", apierrors.NewForbidden(gr, "", errors.New("no access")), "deploy/rbac.yaml", apierrors.IsForbidden},
		{"unauthorized", apierrors.NewUnauthorized("bad token"), "not authenticated", apierrors.IsUnauthorized},
		{"timeout", context.DeadlineExceeded, "timed out", func(err error) bool { return errors.Is(err, context.DeadlineExceeded) }},
		{"server error", apierrors.NewInternalError(errors.New("etcd down")), "listing deployments", apierrors.IsInternalError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cs := fake.NewClientset()
			cs.PrependReactor("list", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
				return true, nil, tt.err
			})
			_, err := Inspect(context.Background(), cs, ns)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tt.wantText) {
				t.Errorf("error %q does not contain %q", err, tt.wantText)
			}
			// The original error must stay inspectable through wrapping.
			if !tt.check(err) {
				t.Errorf("error %v lost its type when wrapped", err)
			}
		})
	}
}

func TestInspectPodListFailureKeepsDeploymentHealth(t *testing.T) {
	cs := fake.NewClientset(newDeployment("web", 2, 2, matchLabels("app", "web")))
	cs.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", errors.New("no access"))
	})
	r, err := Inspect(context.Background(), cs, ns)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if r.PodsErr == nil || !apierrors.IsForbidden(r.PodsErr) {
		t.Fatalf("PodsErr = %v, want a Forbidden error", r.PodsErr)
	}
	if len(r.Deployments) != 1 || r.Deployments[0].Status != health.Healthy {
		t.Fatalf("deployment health missing or wrong: %+v", r.Deployments)
	}
	if r.Deployments[0].Pods != nil {
		t.Errorf("Pods = %v, want nil when pod information is unavailable", r.Deployments[0].Pods)
	}
}

const testKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: c
  cluster:
    server: https://127.0.0.1:6443
users:
- name: u
  user:
    token: not-a-real-token
contexts:
- name: with-ns
  context: {cluster: c, user: u, namespace: team-a}
- name: no-ns
  context: {cluster: c, user: u}
current-context: with-ns
`

func writeKubeconfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNewClient(t *testing.T) {
	path := writeKubeconfig(t, testKubeconfig)

	tests := []struct {
		name    string
		opts    Options
		wantNS  string
		wantErr string
	}{
		{"current context namespace", Options{Kubeconfig: path}, "team-a", ""},
		{"explicit context without namespace", Options{Kubeconfig: path, Context: "no-ns"}, "default", ""},
		{"unknown context", Options{Kubeconfig: path, Context: "missing"}, "", "loading kubeconfig"},
		{"missing file", Options{Kubeconfig: filepath.Join(t.TempDir(), "nope")}, "", "loading kubeconfig"},
		{"malformed file", Options{Kubeconfig: writeKubeconfig(t, "not: [valid")}, "", "loading kubeconfig"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cs, gotNS, err := NewClient(tt.opts)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want error containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			if cs == nil {
				t.Fatal("nil clientset")
			}
			if gotNS != tt.wantNS {
				t.Errorf("namespace = %q, want %q", gotNS, tt.wantNS)
			}
		})
	}
}

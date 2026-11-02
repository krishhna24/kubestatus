// Package kube loads Kubernetes client configuration and reads the
// Deployments and Pods that kubestatus inspects. It only ever issues
// read-only (list) requests.
package kube

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/krishhna24/kubestatus/internal/health"
)

// Options selects which kubeconfig and context to use. Empty fields fall
// back to the standard kubectl behaviour ($KUBECONFIG, ~/.kube/config and
// the current context).
type Options struct {
	Kubeconfig string
	Context    string
	// Timeout bounds every individual HTTP request to the API server.
	Timeout time.Duration
}

// NewClient builds a typed clientset and returns the namespace configured
// for the selected kubeconfig context ("default" if none is set).
func NewClient(opts Options) (kubernetes.Interface, string, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	rules.ExplicitPath = opts.Kubeconfig
	overrides := &clientcmd.ConfigOverrides{CurrentContext: opts.Context}
	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides)

	cfg, err := cc.ClientConfig()
	if err != nil {
		return nil, "", fmt.Errorf("loading kubeconfig: %w", err)
	}
	cfg.UserAgent = "kubestatus"
	cfg.Timeout = opts.Timeout

	ns, _, err := cc.Namespace()
	if err != nil {
		return nil, "", fmt.Errorf("resolving namespace from kubeconfig: %w", err)
	}

	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, "", fmt.Errorf("creating Kubernetes client: %w", err)
	}
	return cs, ns, nil
}

// Inspect lists the Deployments and Pods in a namespace and evaluates their
// health. It makes exactly two API requests regardless of how many
// Deployments exist: Pods are listed once and matched to Deployments using
// each Deployment's label selector.
//
// A failure to list Deployments is returned as an error. A failure to list
// Pods is recorded in Report.PodsErr so Deployment health can still be shown.
func Inspect(ctx context.Context, cs kubernetes.Interface, namespace string) (health.Report, error) {
	report := health.Report{Namespace: namespace}

	deployments, err := cs.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return report, apiError(err, "deployments", namespace)
	}
	if len(deployments.Items) == 0 {
		return report, nil
	}

	pods, err := cs.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		report.PodsErr = apiError(err, "pods", namespace)
	}

	items := deployments.Items
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })

	for i := range items {
		d := &items[i]
		h := health.ClassifyDeployment(d)

		if report.PodsErr == nil {
			sel, err := Selector(d)
			if err != nil {
				h.Issues = append(h.Issues, err.Error())
				h.Pods = []health.PodDiagnosis{}
			} else {
				h.Pods = diagnose(MatchPods(sel, pods.Items))
			}
		}
		report.Deployments = append(report.Deployments, h)
	}
	return report, nil
}

// Selector converts a Deployment's label selector. A nil or empty selector
// is rejected: an empty selector would match every Pod in the namespace,
// which is never what a Deployment means (the API server forbids it for
// apps/v1, but objects from other sources may not be validated).
func Selector(d *appsv1.Deployment) (labels.Selector, error) {
	ls := d.Spec.Selector
	if ls == nil || (len(ls.MatchLabels) == 0 && len(ls.MatchExpressions) == 0) {
		return nil, errors.New("deployment has an empty label selector; pods cannot be matched")
	}
	sel, err := metav1.LabelSelectorAsSelector(ls)
	if err != nil {
		return nil, fmt.Errorf("invalid label selector: %w", err)
	}
	return sel, nil
}

// MatchPods returns the Pods whose labels satisfy the selector, sorted by name.
func MatchPods(sel labels.Selector, pods []corev1.Pod) []corev1.Pod {
	var out []corev1.Pod
	for _, p := range pods {
		if sel.Matches(labels.Set(p.Labels)) {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func diagnose(pods []corev1.Pod) []health.PodDiagnosis {
	out := make([]health.PodDiagnosis, 0, len(pods))
	for i := range pods {
		out = append(out, health.DiagnosePod(&pods[i]))
	}
	return out
}

// apiError adds context to API failures, with a hint for the common
// permission and missing-namespace cases.
func apiError(err error, resource, namespace string) error {
	switch {
	case apierrors.IsForbidden(err):
		return fmt.Errorf("permission denied listing %s in namespace %q (kubestatus needs get/list on %s; see deploy/rbac.yaml): %w",
			resource, namespace, resource, err)
	case apierrors.IsUnauthorized(err):
		return fmt.Errorf("not authenticated to the cluster while listing %s (check your kubeconfig credentials): %w", resource, err)
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("timed out listing %s in namespace %q: %w", resource, namespace, err)
	default:
		return fmt.Errorf("listing %s in namespace %q: %w", resource, namespace, err)
	}
}

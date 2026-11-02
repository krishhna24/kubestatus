// Command kubestatus inspects the health of Kubernetes Deployments and their
// Pods in a single namespace.
//
// Exit codes:
//
//	0  every Deployment is HEALTHY (or there are none)
//	1  at least one Deployment is DEGRADED or UNHEALTHY
//	2  usage, configuration or API error
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/krishhna24/kubestatus/internal/health"
	"github.com/krishhna24/kubestatus/internal/kube"
	"github.com/krishhna24/kubestatus/internal/report"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

const (
	exitHealthy   = 0
	exitUnhealthy = 1
	exitError     = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("kubestatus", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		namespace   string
		kubeconfig  string
		kubeContext string
		timeout     time.Duration
		allPods     bool
		showVersion bool
	)
	fs.StringVar(&namespace, "namespace", "", "namespace to inspect (default: namespace of the kubeconfig context, else \"default\")")
	fs.StringVar(&namespace, "n", "", "shorthand for --namespace")
	fs.StringVar(&kubeconfig, "kubeconfig", "", "path to a kubeconfig file (default: $KUBECONFIG or ~/.kube/config)")
	fs.StringVar(&kubeContext, "context", "", "kubeconfig context to use (default: current context)")
	fs.DurationVar(&timeout, "timeout", 10*time.Second, "overall timeout for Kubernetes API requests")
	fs.BoolVar(&allPods, "all-pods", false, "show every Pod, including those of healthy Deployments")
	fs.BoolVar(&showVersion, "version", false, "print the version and exit")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: kubestatus [flags]\n\nInspect Deployment and Pod health in a Kubernetes namespace.\n\nFlags:\n")
		fs.PrintDefaults()
		fmt.Fprintf(fs.Output(), "\nExit codes: 0 all healthy, 1 degraded or unhealthy Deployments found, 2 error.\n")
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitHealthy
		}
		return exitError
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "kubestatus: unexpected arguments: %v\n", fs.Args())
		fs.Usage()
		return exitError
	}
	if showVersion {
		fmt.Fprintf(stdout, "kubestatus %s\n", version)
		return exitHealthy
	}
	if timeout <= 0 {
		fmt.Fprintln(stderr, "kubestatus: --timeout must be positive")
		return exitError
	}

	cs, contextNS, err := kube.NewClient(kube.Options{Kubeconfig: kubeconfig, Context: kubeContext, Timeout: timeout})
	if err != nil {
		fmt.Fprintf(stderr, "kubestatus: %v\n", err)
		return exitError
	}
	if namespace == "" {
		namespace = contextNS
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	r, err := kube.Inspect(ctx, cs, namespace)
	if err != nil {
		fmt.Fprintf(stderr, "kubestatus: %v\n", err)
		return exitError
	}

	if err := report.Write(stdout, r, report.Options{AllPods: allPods}); err != nil {
		fmt.Fprintf(stderr, "kubestatus: writing report: %v\n", err)
		return exitError
	}
	return exitCode(r)
}

// exitCode maps a report to the process exit status. An incomplete
// inspection (Pods could not be listed) is an error even if Deployment
// status looks fine, so automation never mistakes it for a clean result.
func exitCode(r health.Report) int {
	if r.PodsErr != nil {
		return exitError
	}
	s := r.Summary()
	if s.Degraded > 0 || s.Unhealthy > 0 {
		return exitUnhealthy
	}
	return exitHealthy
}

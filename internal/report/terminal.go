// Package report renders a health.Report as plain-text tables for a terminal.
package report

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/krishhna24/kubestatus/internal/health"
)

// Options controls what is printed.
type Options struct {
	// AllPods prints every Pod. By default only Pods that belong to a
	// non-healthy Deployment, or that are themselves unhealthy, are shown.
	AllPods bool
}

// Write renders the report to w.
func Write(w io.Writer, r health.Report, opts Options) error {
	ew := &errWriter{w: w}

	ew.printf("KUBESTATUS | Namespace: %s\n\n", r.Namespace)

	if len(r.Deployments) == 0 {
		ew.printf("No Deployments found in namespace %q.\n", r.Namespace)
		return ew.err
	}

	writeDeployments(ew, r)
	writeIssues(ew, r)
	writePods(ew, r, opts)
	writeSummary(ew, r.Summary())
	return ew.err
}

func writeDeployments(ew *errWriter, r health.Report) {
	tw := tabwriter.NewWriter(ew, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw, "DEPLOYMENT\tREADY\tAVAILABLE\tUP-TO-DATE\tSTATUS")
	for _, d := range r.Deployments {
		fmt.Fprintf(tw, "%s\t%d/%d\t%d\t%d\t%s\n", d.Name, d.Ready, d.Desired, d.Available, d.Updated, d.Status)
	}
	ew.check(tw.Flush())
}

// writeIssues prints Deployment-level issues and notes. They are kept out of
// the table so long condition messages do not break column alignment.
func writeIssues(ew *errWriter, r health.Report) {
	var found bool
	for _, d := range r.Deployments {
		if len(d.Issues) > 0 || len(d.Notes) > 0 {
			found = true
			break
		}
	}
	if !found {
		return
	}
	ew.printf("\nDEPLOYMENT CONDITIONS\n")
	for _, d := range r.Deployments {
		for _, issue := range d.Issues {
			ew.printf("  %s: [issue] %s\n", d.Name, issue)
		}
		for _, note := range d.Notes {
			ew.printf("  %s: [note] %s\n", d.Name, note)
		}
	}
}

func writePods(ew *errWriter, r health.Report, opts Options) {
	if r.PodsErr != nil {
		ew.printf("\nPOD DIAGNOSTICS\n  unavailable: %v\n", r.PodsErr)
		return
	}

	header := false
	for _, d := range r.Deployments {
		pods := visiblePods(d, opts)
		noPods := d.Pods != nil && len(d.Pods) == 0 && d.Desired > 0
		if len(pods) == 0 && !noPods {
			continue
		}
		if !header {
			ew.printf("\nPOD DIAGNOSTICS\n")
			header = true
		}
		ew.printf("\n%s (%s)\n", d.Name, d.Status)
		if noPods {
			ew.printf("  no Pods match this Deployment's selector\n")
			continue
		}

		tw := tabwriter.NewWriter(ew, 0, 0, 3, ' ', 0)
		fmt.Fprintln(tw, "  POD\tPHASE\tREADY\tRESTARTS\tREASON")
		for _, p := range pods {
			fmt.Fprintf(tw, "  %s\t%s\t%d/%d\t%d\t%s\n", p.Name, phase(p), p.ReadyContainers, p.TotalContainers, p.Restarts, p.Reason)
		}
		ew.check(tw.Flush())

		for _, p := range pods {
			for _, problem := range p.Problems {
				ew.printf("  %s: %s\n", p.Name, problem)
			}
		}
	}
}

func visiblePods(d health.DeploymentHealth, opts Options) []health.PodDiagnosis {
	if opts.AllPods || d.Status != health.Healthy {
		return d.Pods
	}
	var out []health.PodDiagnosis
	for _, p := range d.Pods {
		if !p.Healthy {
			out = append(out, p)
		}
	}
	return out
}

func phase(p health.PodDiagnosis) string {
	if p.Phase == "" {
		return "Unknown"
	}
	return string(p.Phase)
}

func writeSummary(ew *errWriter, s health.Summary) {
	ew.printf("\nSUMMARY\n")
	tw := tabwriter.NewWriter(ew, 0, 0, 1, ' ', 0)
	fmt.Fprintf(tw, "Deployments:\t%d\n", s.Total)
	fmt.Fprintf(tw, "Healthy:\t%d\n", s.Healthy)
	fmt.Fprintf(tw, "Degraded:\t%d\n", s.Degraded)
	fmt.Fprintf(tw, "Unhealthy:\t%d\n", s.Unhealthy)
	ew.check(tw.Flush())
}

// errWriter remembers the first write error so rendering code does not need
// to check every call.
type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) Write(p []byte) (int, error) {
	if e.err != nil {
		return 0, e.err
	}
	n, err := e.w.Write(p)
	e.err = err
	return n, err
}

func (e *errWriter) printf(format string, args ...any) {
	fmt.Fprintf(e, format, args...)
}

func (e *errWriter) check(err error) {
	if e.err == nil {
		e.err = err
	}
}

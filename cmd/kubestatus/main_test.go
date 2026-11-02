package main

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/krishhna24/kubestatus/internal/health"
)

func TestExitCode(t *testing.T) {
	dep := func(s health.Status) health.DeploymentHealth { return health.DeploymentHealth{Status: s} }
	tests := []struct {
		name string
		r    health.Report
		want int
	}{
		{"no deployments", health.Report{}, exitHealthy},
		{"all healthy", health.Report{Deployments: []health.DeploymentHealth{dep(health.Healthy), dep(health.Healthy)}}, exitHealthy},
		{"one degraded", health.Report{Deployments: []health.DeploymentHealth{dep(health.Healthy), dep(health.Degraded)}}, exitUnhealthy},
		{"one unhealthy", health.Report{Deployments: []health.DeploymentHealth{dep(health.Unhealthy)}}, exitUnhealthy},
		{"pods unavailable", health.Report{Deployments: []health.DeploymentHealth{dep(health.Healthy)}, PodsErr: errors.New("x")}, exitError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := exitCode(tt.r); got != tt.want {
				t.Errorf("exitCode = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestRunWithoutCluster(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-kubeconfig")
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{"version", []string{"--version"}, exitHealthy, "kubestatus dev", ""},
		{"help", []string{"-h"}, exitHealthy, "", "Usage: kubestatus"},
		{"unknown flag", []string{"--bogus"}, exitError, "", "flag provided but not defined"},
		{"positional args", []string{"default"}, exitError, "", "unexpected arguments"},
		{"bad timeout", []string{"--timeout", "0s"}, exitError, "", "--timeout must be positive"},
		{"missing kubeconfig", []string{"--kubeconfig", missing}, exitError, "", "loading kubeconfig"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(tt.args, &stdout, &stderr)
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d (stderr: %s)", code, tt.wantCode, stderr.String())
			}
			if !strings.Contains(stdout.String(), tt.wantStdout) {
				t.Errorf("stdout = %q, want substring %q", stdout.String(), tt.wantStdout)
			}
			if !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want substring %q", stderr.String(), tt.wantStderr)
			}
		})
	}
}

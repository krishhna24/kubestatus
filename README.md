# kubestatus

[![CI](https://github.com/krishhna24/kubestatus/actions/workflows/ci.yaml/badge.svg)](https://github.com/krishhna24/kubestatus/actions/workflows/ci.yaml)

A small, read-only command-line tool that inspects the Deployments in a
Kubernetes namespace, finds their Pods through label selectors, classifies
their health, and explains why Pods are not serving.

```bash
kubestatus -n production
```

## Problem

Answering "is this namespace healthy, and if not, why?" usually takes several
commands: `kubectl get deploy`, `kubectl get pods`, `kubectl describe pod ...`
and some mental matching of ReplicaSet hashes to Deployments. `kubestatus`
does that in one read-only pass and returns an exit code that scripts and CI
jobs can act on.

## Features

- **Namespace inspection:** lists every Deployment in one namespace.
- **Deployment health:** compares desired, ready, available and up-to-date
  replicas and classifies each Deployment as `HEALTHY`, `DEGRADED` or
  `UNHEALTHY`.
- **Failure conditions:** reports `Progressing=False` (for example
  `ProgressDeadlineExceeded`) and `ReplicaFailure=True` separately.
- **Pod discovery by selector:** matches Pods using each Deployment's
  `spec.selector` (both `matchLabels` and `matchExpressions`), never by Pod
  name.
- **Pod diagnostics:** shows the Pod phase, ready containers, restarts and the
  most specific reason: `CrashLoopBackOff`, `ImagePullBackOff`, `ErrImagePull`,
  `CreateContainerConfigError`, `OOMKilled`, `Unschedulable` (with the
  scheduler's message), failing init containers, failing readiness probes,
  `Evicted` and `Terminating`.
- **Exit status:** `0` when all Deployments are healthy, `1` when any is
  degraded or unhealthy, and `2` on errors.

## Example

This is real output from a single-node [kind](https://kind.sigs.k8s.io/)
cluster running [`deploy/examples/demo.yaml`](deploy/examples/demo.yaml):

```text
$ kubestatus -n kubestatus-demo
KUBESTATUS | Namespace: kubestatus-demo

DEPLOYMENT    READY   AVAILABLE   UP-TO-DATE   STATUS
bad-image     0/1     0           1            UNHEALTHY
crashloop     0/1     0           1            UNHEALTHY
healthy-web   2/2     2           2            HEALTHY
never-ready   0/1     0           1            UNHEALTHY
scaled-zero   0/0     0           0            HEALTHY
spread        1/2     1           2            DEGRADED

DEPLOYMENT CONDITIONS
  bad-image: [issue] Progressing=False (ProgressDeadlineExceeded): ReplicaSet "bad-image-8654b46b64" has timed out progressing.

POD DIAGNOSTICS

bad-image (UNHEALTHY)
  POD                          PHASE     READY   RESTARTS   REASON
  bad-image-8654b46b64-rt7zh   Pending   0/1     0          ImagePullBackOff
  bad-image-8654b46b64-rt7zh: container "app" waiting: ImagePullBackOff - Back-off pulling image "nginx:0.0.0-does-not-exist": ErrImagePull: rpc error: code = NotFound desc = failed to pull and unpack image "docker.io/library/nginx:0.0.0-does-not-exist": failed to resolve reference "docker.io/library/nginx:0.0.0-does-not-exist": docker.io/library/nginx:0.0.0-does-not-exist: not found

crashloop (UNHEALTHY)
  POD                          PHASE     READY   RESTARTS   REASON
  crashloop-8675d98659-6nc7m   Running   0/1     4          Error
  crashloop-8675d98659-6nc7m: container "app" terminated: Error (exit code 1)

never-ready (UNHEALTHY)
  POD                            PHASE     READY   RESTARTS   REASON
  never-ready-5867b5dc5b-cs2hr   Running   0/1     0          NotReady
  never-ready-5867b5dc5b-cs2hr: container "nginx" running but not ready (readiness probe not passing)

spread (DEGRADED)
  POD                       PHASE     READY   RESTARTS   REASON
  spread-6b6885544b-42k6d   Running   1/1     0          Running
  spread-6b6885544b-wgqnf   Pending   0/1     0          Unschedulable
  spread-6b6885544b-wgqnf: not scheduled: 0/1 nodes are available: 1 node(s) didn't match pod anti-affinity rules. preemption: 0/1 nodes are available: 1 No preemption victims found for incoming pod.

SUMMARY
Deployments: 6
Healthy:     2
Degraded:    1
Unhealthy:   3

$ echo $?
1
```

By default, Pods of healthy Deployments are hidden. Pods that are unhealthy on
their own are still shown. Pass `--all-pods` to list every Pod.

## Health classification

`desired` is `spec.replicas`, or `1` when the field is unset (the API
default). `ready`, `available` and `up-to-date` come from the Deployment's
`status`.

| Status      | Rule (evaluated top to bottom)                                                  |
| ----------- | ------------------------------------------------------------------------------- |
| `HEALTHY`   | `desired == 0` (scaled to zero)                                                 |
| `HEALTHY`   | `ready >= desired` **and** `available >= desired` **and** no failure condition  |
| `DEGRADED`  | `ready > 0`, but the rule above is not met                                      |
| `UNHEALTHY` | `desired > 0` and `ready == 0`                                                  |

Notes on these rules:

- **Readiness and availability are different.** A Pod is *ready* when its
  readiness probe passes. It becomes *available* only after staying ready for
  `minReadySeconds`. A Deployment with all replicas ready but not yet
  available is `DEGRADED`.
- **Scheduling is not readiness.** A Pod that is `Pending` as `Unschedulable`
  counts as not ready. Its scheduler message is shown in the Pod diagnostics.
- **Failure conditions** (`Progressing=False`, `ReplicaFailure=True`) are
  listed under *DEPLOYMENT CONDITIONS*. They prevent a `HEALTHY` verdict, so a
  rollout stuck on a bad image is not hidden by the old replicas that are
  still serving.
- **Rollouts.** If the controller has not yet observed the latest spec, or
  fewer replicas are up to date than desired, a `[note]` is printed. Notes do
  not change the status by themselves.
- **Crash loops without readiness probes.** A container with no readiness
  probe counts as ready whenever it is running, even if it crashes every few
  seconds. Deployment status follows the API's readiness counts. However, a
  ready container whose last exit was a failure is flagged in the Pod
  diagnostics, so it is never silently reported as fine.

## Exit codes

| Code | Meaning                                                                       |
| ---- | ----------------------------------------------------------------------------- |
| `0`  | Every Deployment is `HEALTHY`, or the namespace has no Deployments            |
| `1`  | At least one Deployment is `DEGRADED` or `UNHEALTHY`                          |
| `2`  | Usage error, kubeconfig error, API error or timeout, or Pods could not be listed |

If Pods cannot be listed (for example, RBAC allows Deployments but not Pods),
the Deployment table is still printed, but the exit code is `2`. That way an
incomplete inspection is never mistaken for a clean one.

## Architecture

```text
cmd/kubestatus/        flag parsing, wiring, exit codes
internal/kube/         kubeconfig loading, client-go clientset, list calls, selector matching
internal/health/       pure classification and Pod diagnostics (no API calls)
internal/report/       text/tabwriter terminal rendering
deploy/rbac.yaml       least-privilege namespaced Role + RoleBinding
deploy/examples/       demo workloads for a disposable cluster
```

```text
main ──► kube.NewClient ──► kube.Inspect ──► health.ClassifyDeployment / health.DiagnosePod
                                  │
                                  └──► report.Write ──► stdout, exit code
```

Design decisions:

- **Two API calls per run.** `kubestatus` lists Deployments once and Pods
  once in the namespace. It then matches Pods to Deployments on the client
  side with `metav1.LabelSelectorAsSelector`. The number of requests stays
  constant no matter how many Deployments there are. If the namespace has no
  Deployments, the Pod list is skipped.
- **The health logic is pure.** `internal/health` takes API objects and
  returns values, so every rule is covered by table-driven tests without a
  cluster.
- **Unsafe selectors are rejected.** A nil or empty selector would match every
  Pod in the namespace, so it is reported as an issue instead.
- **Few dependencies.** `client-go`, `api` and `apimachinery` (v0.36), plus
  the standard library: `flag` for the CLI and `text/tabwriter` for output.
- **Bounded calls.** All API calls share one `context.Context` with a
  `--timeout` (default 10s), which is also set as the per-request HTTP
  timeout. Ctrl-C cancels it.

## Prerequisites

- Go 1.26+ (to build from source)
- Access to a Kubernetes cluster through a kubeconfig. The tool was tested
  against Kubernetes v1.36 on kind. client-go v0.36 supports the usual ±1
  minor version skew.

## Installation

```bash
go install github.com/krishhna24/kubestatus/cmd/kubestatus@latest
```

From source:

```bash
git clone https://github.com/krishhna24/kubestatus.git
cd kubestatus
make build          # produces ./kubestatus
```

With Docker (no Go toolchain required):

```bash
docker build -t kubestatus .
docker run --rm --network host --user "$(id -u):$(id -g)" \
  -v "$HOME/.kube/config:/kubeconfig:ro" \
  kubestatus --kubeconfig /kubeconfig -n default
```

The image is a static binary on `distroless/static:nonroot`, with no shell.
`--user` lets the container read your kubeconfig, which is normally mode
`600`. `--network host` is only needed when the API server listens on
localhost, as it does with kind. Exec-based auth plugins (for example cloud
CLIs) are not available inside the image.

## Usage

```text
kubestatus [flags]

  -n, --namespace string   namespace to inspect (default: namespace of the kubeconfig context, else "default")
      --kubeconfig string  path to a kubeconfig file (default: $KUBECONFIG or ~/.kube/config)
      --context string     kubeconfig context to use (default: current context)
      --timeout duration   overall timeout for Kubernetes API requests (default 10s)
      --all-pods           show every Pod, including those of healthy Deployments
      --version            print the version and exit
```

Examples:

```bash
kubestatus                                  # namespace from the current context
kubestatus -n payments --context prod-eu    # a specific namespace and context
kubestatus -n payments --all-pods
kubestatus -n payments || echo "payments needs attention"
```

## Authentication

`kubestatus` loads credentials the same way `kubectl` does, through
client-go's `clientcmd`:

1. `--kubeconfig`, if given
2. otherwise `$KUBECONFIG` (a colon-separated list is merged)
3. otherwise `~/.kube/config`

`--context` selects a context. If neither `--context` nor `-n` is given, the
current context and its namespace are used. Auth plugins and exec credential
providers configured in the kubeconfig work as they do for kubectl.

`kubestatus` never prints, stores or modifies credentials.

## Kubernetes permissions

The tool is read-only and only issues `list` requests:

| API group | Resource      | Verbs        |
| --------- | ------------- | ------------ |
| `apps`    | `deployments` | `get`, `list` |
| `""`      | `pods`        | `get`, `list` |

[`deploy/rbac.yaml`](deploy/rbac.yaml) contains a namespaced `Role` and
`RoleBinding` with exactly these permissions: no write verbs, no wildcards,
and no access to Secrets or ConfigMaps. Replace `NAMESPACE` and the subject,
then apply it:

```bash
sed 's/NAMESPACE/payments/' deploy/rbac.yaml | kubectl apply -f -
kubectl auth can-i list pods -n payments --as kubestatus-user        # yes
kubectl auth can-i delete pods -n payments --as kubestatus-user      # no
```

Without permission, the error explains what is missing:

```text
kubestatus: permission denied listing deployments in namespace "kube-system" (kubestatus needs get/list on deployments; see deploy/rbac.yaml): deployments.apps is forbidden: User "system:serviceaccount:kubestatus-demo:kubestatus-sa" cannot list resource "deployments" in API group "apps" in the namespace "kube-system"
```

## Testing

```bash
make check      # gofmt check, go vet, go test -race, build
```

The unit tests cover:

- `internal/health`: table-driven tests for every classification rule and Pod
  state. These include zero and nil replicas, ready-but-unavailable, failure
  conditions, stale generations, Pending/Unschedulable, Failed/Evicted,
  CrashLoopBackOff (including OOMKilled), ImagePullBackOff, config errors,
  init container failures, missing container statuses and Terminating Pods.
- `internal/kube`: the client-go **fake clientset**. Tests cover selector
  matching (labels and expressions, namespace isolation, no name-prefix
  assumptions), exactly two read-only API calls per run, empty and invalid
  selectors, and Forbidden/Unauthorized/timeout/server errors injected through
  reactors. Separate tests load kubeconfig files (context namespace, unknown
  context, missing and malformed files).
- `internal/report` and `cmd/kubestatus`: rendering, hidden and visible Pods,
  writer errors, exit codes and flag handling.

The fake clientset does not run the API server, admission, the scheduler or
controllers. Those behaviours were verified separately on a live kind cluster:

```bash
kind create cluster --name kubestatus
kubectl apply -f deploy/examples/demo.yaml
# wait about a minute for images to pull and the bad-image progress deadline to expire
./kubestatus -n kubestatus-demo
kind delete cluster --name kubestatus
```

The live run also checked a ServiceAccount bound to `deploy/rbac.yaml`: it
could inspect its namespace, was refused elsewhere (exit 2), and could not
modify anything (`kubectl auth can-i`).

## Limitations

- Inspects one namespace and one cluster per run.
- Inspects Deployments only, not StatefulSets, DaemonSets or bare Pods.
- If two Deployments have overlapping selectors, a Pod is listed under both.
  This matches what each selector selects, but `ownerReferences` are not used
  to tell them apart.
- A namespace that does not exist looks the same as an empty one, because
  Kubernetes returns an empty list for both.
- Health is a point-in-time snapshot. A crash-looping container can be
  briefly ready between restarts (see *Crash loops without readiness probes*).
- Kubernetes Events are not read, so some causes (for example a failed volume
  mount) appear only as a generic waiting reason such as `ContainerCreating`.
- Plain-text output only.

This is a small diagnostic tool, not a monitoring system, and it is not
production-hardened.

## Future improvements

| Version | Potential features                                            |
| ------- | ------------------------------------------------------------- |
| v0.2    | JSON output, multiple namespaces, Kubernetes Events            |
| v0.3    | Watch mode, resource requests and limits                       |
| v0.4    | Prometheus metrics, advanced diagnostics, release automation   |

## License

[MIT](LICENSE)

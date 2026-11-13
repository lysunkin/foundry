# AGENT.md

Rules for AI coding agents (and humans) working in this repository.
Stack: **Go**, **Docker**, **Kubernetes**.

These rules are mandatory. If a rule conflicts with a user request, say so and ask how to proceed. Do not silently ignore a rule.

---

## 1. Golden Rule: Clarify First, Code Later

**No production code is written until the requirements are fully understood and the user has explicitly approved the written spec.**

Guessing is a failure mode. Asking is cheap; rewriting is expensive.

### 1.1 Workflow for every new feature or non-trivial change

1. **Understand**: Read the request. Inspect the relevant parts of the repo (existing packages, patterns, configs, manifests, tests) before asking anything, so questions are informed and not generic.
2. **Clarify**: Ask clarifying questions (see 1.2). Ask in batches grouped by topic, numbered so the user can answer by number. Repeat until no open questions remain. Follow-up answers often raise new questions, so keep asking.
3. **Spec**: Write a short implementation spec (template in 1.4) and ask for explicit approval ("approved", "go ahead", etc.).
4. **Plan**: List the files and packages that will be added or changed, the tests that will be written, and any migrations or manifest changes.
5. **Implement**: Write the code, tests, and docs together, in small reviewable steps.
6. **Verify**: Run the checks in section 9 and report results honestly.

### 1.2 What to ask about

Do not ask about things you can learn from the codebase. Do ask about everything else, including "minor" details. Cover at least:

**Functional behavior**
- What exactly should happen? Who triggers it, and what is the expected output?
- Happy path, edge cases, and invalid input. What should happen in each?
- Is this user-facing, internal, or both? What are the acceptance criteria?
- Is anything explicitly out of scope?

**Interfaces and contracts**
- New or changed API (REST/gRPC/CLI/events)? Exact paths, methods, fields, types, status codes, error format?
- Versioning and backward compatibility. Is a breaking change acceptable?
- Pagination, filtering, sorting, idempotency, rate limiting?

**Data**
- New or changed data model, DB tables, indexes, migrations?
- Data volume, retention, consistency requirements, transactions?
- Is there existing data that must be migrated or backfilled?

**Non-functional requirements**
- Expected load, latency, and throughput targets?
- Timeouts, retries, backoff, circuit breaking?
- Concurrency and ordering requirements?

**Security and privacy**
- Authentication and authorization rules? Roles and permissions?
- Does it handle PII or secrets? Audit logging requirements?
- Input validation and trust boundaries?

**Operations**
- Required logs, metrics, traces, alerts?
- Configuration: new env vars, flags, config-file fields? Defaults?
- Feature flag or gradual rollout needed? Rollback plan?

**Docker and Kubernetes**
- Does the Dockerfile, base image, or build change?
- New Kubernetes resources (Deployment, Service, Ingress, CronJob, ConfigMap, Secret, HPA, PDB, NetworkPolicy)?
- Resource requests and limits, replicas, probes, graceful shutdown behavior?
- Which environments are affected (dev/stage/prod)? Helm or Kustomize changes?

**Dependencies and compatibility**
- Are new third-party libraries acceptable? Any preferred or banned ones?
- Supported Go version, OS/arch targets, minimum Kubernetes version?

**Testing and delivery**
- Expected test types (unit, integration, e2e)? Any fixtures or test environments?
- Documentation to update (README, OpenAPI, runbooks, CHANGELOG)?
- Deadline or priority constraints that affect scope?

### 1.3 Rules of engagement for questions

- Ask **specific** questions with **suggested defaults** when sensible: *"Should the endpoint return 404 or an empty list when no results are found? I suggest 200 with an empty list."*
- Never batch-answer your own questions and proceed. Wait for the user.
- Do not stop asking just because the user seems impatient. Instead, make questions easy to answer (numbered, yes/no, with defaults).
- If the user says "use your judgment" on specific points, record those decisions as explicit assumptions in the spec and get approval.
- Do not write "temporary" or "placeholder" production code while questions are open. Throwaway exploration must be clearly labeled and never committed.

### 1.4 Spec template (present before coding)

```markdown
## Feature spec: <name>

**Goal:** one or two sentences.
**In scope:** ...
**Out of scope:** ...

**Behavior:** step-by-step description, including edge cases and errors.
**API / interface changes:** exact signatures, routes, payloads, status codes.
**Data changes:** schema, migrations, backfills.
**Config changes:** env vars, flags, defaults.
**Security considerations:** ...
**Observability:** logs, metrics, traces.
**Docker / Kubernetes changes:** ...
**Dependencies:** new libraries and why.
**Test plan:** unit / integration / e2e cases.
**Rollout and rollback:** ...
**Assumptions:** every decision made on the user's behalf.
**Open questions:** must be empty before implementation.

Approve this spec to start implementation? (yes / changes)
```

### 1.5 Scope of the gate

- **New features, behavior changes, API/schema changes, new dependencies, infra changes:** full workflow above.
- **Bug fixes:** reproduce first. If root cause or expected behavior is ambiguous, ask. If it is clear, state your diagnosis and proposed fix briefly, then proceed.
- **Trivial changes** (typos, comments, formatting, obvious one-line fixes): may proceed directly.
- **Refactors:** ask about scope and risk. Behavior must not change unless agreed.

When in doubt, treat the task as a feature and ask.

---

## 2. General Engineering Principles

- **Minimal, focused diffs.** One concern per change. No unrelated refactors, renames, or reformatting in the same change.
- **Follow existing conventions** in the repo before introducing new ones. If a convention seems wrong, raise it. Do not silently diverge.
- **Simple over clever.** Prefer readable, boring code. Apply YAGNI. Do not add abstractions, options, or config for hypothetical future needs.
- **Fail loudly and early** on programmer errors and invalid config. Handle expected runtime errors gracefully.
- **No dead code, commented-out code, or stray TODOs.** A TODO must reference an issue: `// TODO(#123): ...`.
- **Never invent facts.** Do not make up APIs, library functions, flags, or Kubernetes fields. Check the docs or the code. Say "I don't know" when you don't.
- **Never commit secrets,** tokens, credentials, or real customer data, including in tests, examples, and history.
- **Be honest in reporting.** If tests were not run, or something is untested or incomplete, say so plainly.

---

## 3. Go Rules

### 3.1 Project layout

- Standard layout: `cmd/<app>/main.go` for entrypoints, `internal/` for private code, `pkg/` only for code intended to be imported by other modules.
- `main` stays thin: parse config, wire dependencies, start, handle shutdown. Business logic lives in packages.
- Package names are short, lowercase, singular, no underscores, and describe what the package provides (not `util`, `common`, `helpers`, `misc`).
- Avoid import cycles by design: depend on abstractions at package boundaries, keep dependency direction one-way.
- Pin the Go version in `go.mod` and keep `go.sum` committed. Run `go mod tidy` after dependency changes.

### 3.2 Formatting and linting

- Code must pass `gofmt` / `goimports` with no diff.
- Code must pass `go vet` and `golangci-lint run` using the repo's `.golangci.yml`. Recommended linters: `errcheck`, `govet`, `staticcheck`, `gosec`, `revive`, `gocritic`, `ineffassign`, `unused`, `errorlint`, `bodyclose`, `noctx`, `sloglint`, `misspell`.
- Do not disable a linter or add `//nolint` without a specific linter name and a justification comment.

### 3.3 Naming and style

- Follow *Effective Go* and *Go Code Review Comments*.
- Exported identifiers have doc comments starting with the identifier name.
- Receiver names are short and consistent (1–2 letters). No `this` or `self`.
- Avoid stutter: `user.Service`, not `user.UserService`.
- Acronyms are all caps: `ID`, `URL`, `HTTP`, `JSON`.
- Keep functions short and single-purpose. Prefer early returns over deep nesting.
- Avoid global mutable state and `init()` functions with side effects.
- No naked returns in non-trivial functions. No `panic` for expected errors.

### 3.4 Errors

- Always handle errors. Never discard with `_` unless explicitly justified in a comment.
- Wrap with context using `%w`: `fmt.Errorf("load user %d: %w", id, err)`. Messages are lowercase, no trailing punctuation.
- Use `errors.Is` / `errors.As`, never string comparison.
- Define sentinel errors (`var ErrNotFound = errors.New("not found")`) or typed errors for conditions callers need to branch on.
- Log **or** return an error, not both. Log at the boundary (handler, worker loop, `main`).
- Do not expose internal error details to external clients. Map to safe public errors.

### 3.5 Context

- `context.Context` is the first parameter of any function that does I/O, blocks, or may be cancelled. Name it `ctx`. Never store it in structs.
- Propagate the caller's context. Do not use `context.Background()` deep in the call stack.
- All outbound calls (HTTP, DB, gRPC) must have timeouts or deadlines.
- Use `http.NewRequestWithContext`, `db.QueryContext`, etc.
- Use context values only for request-scoped metadata (trace IDs, auth identity), with unexported key types.

### 3.6 Concurrency

- Every goroutine has a clear owner, a defined lifetime, and a shutdown path. No fire-and-forget goroutines.
- Prefer `errgroup.Group` for fan-out with error propagation and `sync.WaitGroup` for simple joins.
- Protect shared state with `sync.Mutex` / `sync.RWMutex` or `atomic`, or avoid sharing via channels. Document what a mutex guards.
- The sender closes channels, never the receiver. Do not close a channel more than once.
- Bound concurrency (worker pools, semaphores). Avoid unbounded goroutine spawning.
- Tests for concurrent code must pass with `go test -race`.

### 3.7 Interfaces and design

- **Accept interfaces, return structs.** Define interfaces where they are consumed, and keep them small.
- Do not create an interface until there are at least two implementations or a real need (e.g. testing seam).
- Use constructors (`NewXxx`) with explicit dependency injection. No hidden singletons.
- Use functional options or a config struct for constructors with many parameters.
- Make the zero value useful where practical.
- Prefer composition over embedding unless embedding is clearly intended.

### 3.8 Configuration

- Follow the 12-factor approach: configuration via environment variables and/or flags, parsed once at startup into a typed struct, validated, with sensible defaults.
- Fail fast on invalid or missing required config with a clear message.
- Never log secrets. Redact sensitive fields.
- Secrets come from the environment or mounted files (Kubernetes Secrets), never from source code.

### 3.9 Logging, metrics, tracing

- Use structured logging (`log/slog` unless the repo already standardizes on another logger). No `fmt.Println` or `log.Printf` in production code.
- Log levels: `Debug` (dev detail), `Info` (lifecycle/business events), `Warn` (recoverable), `Error` (needs attention). Include stable keys (`request_id`, `user_id`, `err`).
- Expose Prometheus-style metrics for requests, errors, latency, and key business events. Use bounded-cardinality labels only.
- Propagate trace context (OpenTelemetry) across service boundaries if tracing is used in the repo.
- Never log PII, tokens, passwords, or full request bodies.

### 3.10 HTTP / gRPC services

- Configure `http.Server` with `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`, `IdleTimeout`. Never use `http.ListenAndServe` bare in production.
- Reuse a shared `http.Client` with timeouts. Always `defer resp.Body.Close()` and drain the body when needed.
- Validate and bound all input (body size limits, field validation). Treat all external input as untrusted.
- Implement **graceful shutdown**: handle `SIGTERM`/`SIGINT` via `signal.NotifyContext`, call `server.Shutdown(ctx)` with a timeout, and stop workers cleanly.
- Expose separate health endpoints: `/healthz` (liveness) and `/readyz` (readiness). Readiness reflects dependency availability.
- Use consistent error response format and correct status codes. Make write endpoints idempotent where feasible.

### 3.11 Database

- Use parameterized queries only. Never build SQL with string concatenation.
- Always pass context, set connection pool limits (`SetMaxOpenConns`, `SetMaxIdleConns`, `SetConnMaxLifetime`), and close rows (`defer rows.Close()`, check `rows.Err()`).
- Schema changes go through versioned, reversible migrations. Migrations must be safe for rolling deploys (backward-compatible with the previous app version).
- Keep transactions short and explicit. Roll back on error.

### 3.12 Testing

- Every feature and bug fix ships with tests. A bug fix includes a regression test that fails without the fix.
- Prefer **table-driven tests** with `t.Run` subtests and descriptive case names. Use `t.Parallel()` where safe.
- Use `t.Helper()`, `t.Cleanup()`, and `t.TempDir()`. Use `testify` only if the repo already does.
- Test behavior through public APIs. Avoid testing implementation details.
- Mock at boundaries via small interfaces (hand-written fakes preferred; generated mocks only if the repo already uses them).
- Integration tests use build tags (`//go:build integration`) or a separate directory, and use real dependencies via containers (e.g. testcontainers) or docker compose.
- No `time.Sleep` for synchronization. Inject clocks or use channels and `Eventually`-style helpers.
- Run with `-race`. Aim for meaningful coverage of logic and error paths, not a vanity percentage.
- Use fuzz tests (`testing.F`) for parsers and input handling where useful.
- Tests must be deterministic and must not depend on external network or test order.

### 3.13 Dependencies

- Prefer the standard library. Add a third-party dependency only with approval and a reason (maintained, widely used, license-compatible, small footprint).
- Run `govulncheck ./...` and fix or report known vulnerabilities.
- No vendoring or `replace` directives unless the repo already does so or it is agreed.

### 3.14 Security

- Run `gosec` (via golangci-lint). Resolve findings or justify them.
- Use `crypto/rand` for secrets and tokens, never `math/rand`.
- Use constant-time comparison (`subtle.ConstantTimeCompare`) for secrets.
- Enforce TLS 1.2+ for outbound and inbound connections where applicable. Never set `InsecureSkipVerify: true` outside tests.
- Sanitize and validate file paths, URLs, and user-supplied identifiers (path traversal, SSRF, injection).
- Hash passwords with bcrypt/argon2, never plain hashes.

### 3.15 Performance

- Do not optimize prematurely. Measure first with benchmarks (`testing.B`) and `pprof`.
- Preallocate slices and maps when size is known. Avoid unnecessary allocations in hot paths.
- Use `strings.Builder` for string concatenation in loops. Use `sync.Pool` only when profiling justifies it.

### 3.16 Documentation

- Doc comments on all exported symbols and every package (`doc.go` or package comment).
- Document non-obvious decisions with *why* comments, not *what* comments.
- Update README, OpenAPI/proto files, and runbooks when behavior changes.

---

## 4. Docker Rules

### 4.1 Dockerfile

- Use **multi-stage builds**: a builder stage with the Go toolchain, a minimal final stage.
- Final image: `gcr.io/distroless/static-debian12:nonroot`, `scratch`, or `alpine` if a shell is genuinely needed. Choose per project and document it.
- **Pin base images** to a specific version (and a digest for production). Never use `latest`.
- Build static binaries: `CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=$VERSION" -o /app ./cmd/<app>`. If CGO is required, say so and adjust the base image.
- **Run as a non-root user** (`USER 65532:65532` or `USER nonroot`). Never run as root.
- Optimize layer caching: copy `go.mod` and `go.sum`, run `go mod download`, then copy source. Use BuildKit cache mounts (`--mount=type=cache`) for the Go build and module caches.
- One process per container. Use exec-form `ENTRYPOINT ["/app"]` so signals reach the process (needed for graceful shutdown).
- Do not bake secrets, `.env` files, or credentials into images or build args. Use BuildKit secrets for build-time credentials.
- Add OCI labels (`org.opencontainers.image.source`, `version`, `revision`).
- Use `COPY`, not `ADD`, unless extracting archives. Combine and clean up `apt`/`apk` steps when needed (`--no-install-recommends`, remove caches).
- `EXPOSE` documents the port. Do not rely on it for behavior.
- `HEALTHCHECK` is optional for Kubernetes (probes are used instead) but useful for plain Docker and Compose.

### 4.2 .dockerignore

- Maintain a `.dockerignore` excluding `.git`, `bin/`, `vendor/` (if unused), test data, docs, local env files, IDE files, and `*.md` that are not needed.

### 4.3 Compose and local development

- `docker-compose.yml` is for local dev and integration tests only. Pin image versions, define health checks, use named volumes, and never put real secrets in it.
- Provide a `make` or `task` target for common flows (`make build`, `make test`, `make docker-build`, `make run`).

### 4.4 Image hygiene

- Scan images in CI (e.g. Trivy or Grype). Fail the build on high/critical vulnerabilities that have fixes.
- Generate an SBOM and sign images if the project requires supply-chain guarantees.
- Tag images immutably (git SHA and semantic version). Do not overwrite release tags.
- Lint Dockerfiles with `hadolint`.

---

## 5. Kubernetes Rules

### 5.1 Manifests in general

- Manifests are declarative and live in the repo (`deploy/` or `k8s/`, using Helm or Kustomize as the repo already does). Do not `kubectl apply` ad-hoc changes without updating the repo.
- Use current, non-deprecated API versions. Validate with `kubeconform` / `kubeval` and `kubectl apply --dry-run=server`.
- Use standard labels on all resources: `app.kubernetes.io/name`, `instance`, `version`, `component`, `part-of`, `managed-by`.
- Explicit namespaces. Never deploy into `default`.
- Use pinned image tags (version or digest). Never `latest`. `imagePullPolicy: IfNotPresent` for immutable tags.

### 5.2 Workload requirements

Every Deployment/StatefulSet/Job container **must** define:

- **Resource requests and limits** for CPU and memory (justified by measurement; set memory limit ≥ request, and consider setting `GOMEMLIMIT` to ~90% of the memory limit; avoid CPU limits unless required, but always set CPU requests).
- **Probes:** `livenessProbe`, `readinessProbe`, and `startupProbe` where startup is slow. Probes must not depend on downstream services for liveness.
- **Graceful termination:** `terminationGracePeriodSeconds` greater than the app's shutdown timeout. Optionally use a `preStop` hook (short sleep) to let endpoints drain.
- **Security context:**
  ```yaml
  securityContext:
    runAsNonRoot: true
    runAsUser: 65532
    allowPrivilegeEscalation: false
    readOnlyRootFilesystem: true
    capabilities:
      drop: ["ALL"]
    seccompProfile:
      type: RuntimeDefault
  ```
  Use `emptyDir` volumes for writable paths.
- `automountServiceAccountToken: false` unless the pod needs API access, and use a dedicated ServiceAccount with least-privilege RBAC.

### 5.3 Availability and scaling

- At least 2 replicas for production services, with `RollingUpdate` strategy (`maxUnavailable: 0` or low, sensible `maxSurge`).
- Add `PodDisruptionBudget` for production services.
- Add `topologySpreadConstraints` or pod anti-affinity to spread replicas across nodes/zones.
- Use `HorizontalPodAutoscaler` when load varies. Ensure resource requests are set, since HPA depends on them.
- Jobs and CronJobs define `backoffLimit`, `activeDeadlineSeconds`, `concurrencyPolicy`, and history limits.

### 5.4 Configuration and secrets

- Non-sensitive config via `ConfigMap`, sensitive via `Secret` (ideally synced from an external secret manager: External Secrets, Sealed Secrets, or SOPS). **Never commit plaintext secrets.**
- Prefer mounting secrets as files or using `secretKeyRef`. Do not put secrets in `args` or plain `env` values.
- Roll pods on config change (checksum annotation in Helm, or hashed ConfigMap names in Kustomize).

### 5.5 Networking and security

- Provide a `Service` with correctly named ports, and `Ingress`/Gateway with TLS where exposed.
- Add `NetworkPolicy` with default-deny and explicit allow rules.
- Apply Pod Security Standards (`restricted` where possible).
- RBAC: namespaced Roles over ClusterRoles, no wildcards, no `cluster-admin` for workloads.

### 5.6 Helm / Kustomize

- Helm: values are documented, have sane defaults, and are validated with `values.schema.json`. Run `helm lint` and `helm template` in CI. Keep templates free of logic that hides intent.
- Kustomize: base plus overlays per environment. Overlays patch and never duplicate the base.
- Environment differences live in values/overlays only, never in the application image.

### 5.7 Observability in the cluster

- Expose metrics endpoint and add `ServiceMonitor`/`PodMonitor` or scrape annotations according to repo convention.
- Logs go to stdout/stderr in structured (JSON) form. No log files inside containers.
- Alerts and dashboards are updated when new critical behavior is added.

---

## 6. API and Data Compatibility

- Do not make breaking API, schema, config, or metric changes without explicit approval and a migration path.
- Use expand/contract migrations (add new, dual-write/read, switch, remove old) so rolling updates and rollbacks are safe.
- Version public APIs. Deprecate before removing, and document it in the CHANGELOG.

---

## 7. Git and Review Conventions

- Branch naming: `feature/<short-name>`, `fix/<short-name>`, `chore/<short-name>`.
- Commits follow **Conventional Commits** (`feat:`, `fix:`, `refactor:`, `test:`, `docs:`, `chore:`, `ci:`), written in imperative mood, small and atomic.
- Pull requests include: what and why, link to the approved spec/issue, test evidence, rollout/rollback notes, and screenshots or sample output where relevant.
- Do not force-push shared branches, rewrite published history, or bypass CI/hooks.
- Update `CHANGELOG.md` for user-visible changes.

---

## 8. Things the Agent Must Never Do

- Write feature code before the spec is approved (section 1).
- Add dependencies, change public APIs, change DB schemas, or modify infrastructure without approval.
- Skip, delete, or weaken tests to make a build pass.
- Disable linters, security checks, or CI steps to get green.
- Commit secrets, credentials, `.env` files, or generated binaries.
- Use `latest` tags, run containers as root, or deploy to `default` namespace.
- Make unrelated changes "while I was there".
- Claim something works without having run it.
- Fabricate APIs, flags, or library behavior.

---

## 9. Definition of Done

A change is complete only when **all** of the following are true:

- [ ] Requirements were clarified and the spec approved (for features).
- [ ] `gofmt` / `goimports` produce no diff.
- [ ] `go vet ./...` and `golangci-lint run` pass.
- [ ] `go build ./...` succeeds.
- [ ] `go test -race ./...` passes, with new or updated tests covering the change (including error paths and edge cases).
- [ ] `govulncheck ./...` shows no unaddressed vulnerabilities.
- [ ] Docker image builds, runs as non-root, and passes `hadolint` and the image scan.
- [ ] Kubernetes manifests/charts pass `helm lint` / `kubeconform` / server-side dry-run, and include resources, probes, and security context.
- [ ] Config, env vars, and migrations are documented, and rollout/rollback is described.
- [ ] README, API docs, and CHANGELOG are updated where relevant.
- [ ] The final report lists what was changed, what was tested (and how), what was **not** tested, and any remaining risks or follow-ups.

---

## 10. Suggested Make Targets

Keep these available so agents and humans run the same commands:

```makefile
fmt:          ## format code
	gofmt -s -w . && goimports -w .
lint:         ## run linters
	golangci-lint run ./...
test:         ## unit tests with race detector
	go test -race -cover ./...
test-int:     ## integration tests
	go test -race -tags=integration ./...
vuln:         ## vulnerability scan
	govulncheck ./...
build:        ## build binary
	CGO_ENABLED=0 go build -trimpath -o bin/app ./cmd/app
docker-build: ## build image
	docker build -t app:dev .
k8s-validate: ## validate manifests
	helm lint deploy/chart && kubeconform -strict -summary deploy/
```

Adjust paths and tools to match the repository. If a target above does not exist yet, propose adding it as part of the relevant change.

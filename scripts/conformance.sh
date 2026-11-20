#!/usr/bin/env bash
# Runs interoperability checks against a freshly built registry.
#
#   scripts/conformance.sh suite    OCI distribution-spec conformance suite
#   scripts/conformance.sh crane    crane push/pull/copy/delete round trip
#   scripts/conformance.sh docker   docker build/push/pull round trip
#
# Environment:
#   AUTH=1        run with Basic auth (generated user, anonymous pull off) and
#                 check that unauthenticated pushes are rejected
#   PORT          registry port on 127.0.0.1 (default 5000)
#   REPORT_DIR    where the suite's junit.xml/report.html and registry.log
#                 go (default ./conformance-report)
#   CRANE         crane binary to use (default: install the pinned version)
#
# Needs Go and git; "crane" installs crane if missing; "docker" needs a Docker
# daemon that can reach 127.0.0.1:$PORT on this host (Docker allows plain
# HTTP to 127.0.0.0/8 by default).
set -euo pipefail

# Pinned versions. Bump deliberately; the suite pin is checked by commit.
SUITE_TAG=v1.1.1
SUITE_COMMIT=a139cc423184af6078077b9b7ee336eddbd03f8f
CRANE_VERSION=v0.22.1
# Referrers API and OCI-Subject are not implemented yet (roadmap R8).
# Remove this once R8 lands.
SUITE_SKIP='References setup|listing references'

MODE=${1:-}
AUTH=${AUTH:-0}
PORT=${PORT:-5000}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
REPORT_DIR=${REPORT_DIR:-$ROOT/conformance-report}
REG=127.0.0.1:$PORT
WORK=$(mktemp -d)
PID=
IMAGES=()

log() { printf '==> %s\n' "$*" >&2; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

cleanup() {
  local status=$?
  if [[ -n $PID ]]; then
    kill "$PID" 2>/dev/null || true
    wait "$PID" 2>/dev/null || true
  fi
  if ((${#IMAGES[@]})); then
    docker rmi -f "${IMAGES[@]}" >/dev/null 2>&1 || true
  fi
  if [[ -f $WORK/registry.log ]]; then
    mkdir -p "$REPORT_DIR"
    cp "$WORK/registry.log" "$REPORT_DIR/registry-$MODE.log"
    if ((status != 0)); then
      log "registry log (last 50 lines; full log in $REPORT_DIR/registry-$MODE.log):"
      tail -n 50 "$WORK/registry.log" >&2
    fi
  fi
  rm -rf "$WORK"
  exit "$status"
}
trap cleanup EXIT

start_registry() {
  log "building registry"
  (cd "$ROOT" && go build -o "$WORK/foundry-registry" ./cmd/registry)
  local args=(-addr "$REG" -data "$WORK/data" -log-level debug)
  if [[ $AUTH == 1 ]]; then
    USERNAME=ci
    PASSWORD=$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')
    # -i reads the password from stdin, keeping it out of the process list.
    htpasswd -niB "$USERNAME" <<<"$PASSWORD" >"$WORK/htpasswd"
    args+=(-auth-htpasswd "$WORK/htpasswd" -auth-anonymous-pull=false)
  fi
  log "starting registry on $REG (auth: $AUTH)"
  "$WORK/foundry-registry" "${args[@]}" >"$WORK/registry.log" 2>&1 &
  PID=$!
  for _ in $(seq 1 50); do
    if curl -fsS -o /dev/null "http://$REG/readyz" 2>/dev/null; then
      return
    fi
    kill -0 "$PID" 2>/dev/null || fail "registry exited during startup"
    sleep 0.2
  done
  fail "registry not ready after 10s"
}

run_suite() {
  log "building conformance suite $SUITE_TAG"
  git clone -q --depth 1 --branch "$SUITE_TAG" \
    https://github.com/opencontainers/distribution-spec.git "$WORK/ds" 2>/dev/null
  local got
  got=$(git -C "$WORK/ds" rev-parse HEAD)
  [[ $got == "$SUITE_COMMIT" ]] || fail "suite $SUITE_TAG is commit $got, expected $SUITE_COMMIT"
  (cd "$WORK/ds/conformance" && GOFLAGS=-mod=mod go test -c -o "$WORK/conformance.test" .)

  mkdir -p "$REPORT_DIR"
  local env=(
    OCI_ROOT_URL="http://$REG"
    OCI_NAMESPACE=conformance/repo
    OCI_CROSSMOUNT_NAMESPACE=conformance/other
    OCI_TEST_PULL=1 OCI_TEST_PUSH=1
    OCI_TEST_CONTENT_DISCOVERY=1 OCI_TEST_CONTENT_MANAGEMENT=1
    OCI_HIDE_SKIPPED_WORKFLOWS=0 OCI_DEBUG=0
    OCI_REPORT_DIR="$REPORT_DIR"
  )
  if [[ $AUTH == 1 ]]; then
    env+=(OCI_USERNAME="$USERNAME" OCI_PASSWORD="$PASSWORD")
  fi
  log "running conformance suite (skipping: $SUITE_SKIP)"
  (cd "$WORK" && env "${env[@]}" ./conformance.test -ginkgo.skip="$SUITE_SKIP" -ginkgo.no-color)
}

run_crane() {
  local crane=${CRANE:-}
  if [[ -z $crane ]]; then
    log "installing crane $CRANE_VERSION"
    GOBIN="$WORK/bin" go install "github.com/google/go-containerregistry/cmd/crane@$CRANE_VERSION"
    crane=$WORK/bin/crane
  fi
  # Keep credentials out of the user's ~/.docker.
  export DOCKER_CONFIG=$WORK/docker-config
  mkdir -p "$DOCKER_CONFIG"
  local c=("$crane" --insecure)

  mkdir -p "$WORK/layer"
  printf 'foundry crane round trip %s\n' "$(date +%s)-$RANDOM$RANDOM" >"$WORK/layer/hello.txt"
  tar -C "$WORK/layer" -cf "$WORK/layer.tar" .

  if [[ $AUTH == 1 ]]; then
    log "crane: unauthenticated push must fail"
    if "${c[@]}" append -f "$WORK/layer.tar" -t "$REG/ci/crane:v1" >/dev/null 2>&1; then
      fail "unauthenticated crane push succeeded"
    fi
    "${c[@]}" auth login "$REG" -u "$USERNAME" --password-stdin <<<"$PASSWORD"
  fi

  log "crane: push, inspect, pull"
  local ref digest copy
  ref=$("${c[@]}" append -f "$WORK/layer.tar" -t "$REG/ci/crane:v1")
  digest=${ref##*@}
  [[ $("${c[@]}" digest "$REG/ci/crane:v1") == "$digest" ]] || fail "digest by tag differs from pushed digest"
  "${c[@]}" manifest "$REG/ci/crane@$digest" | grep -q '"layers"' || fail "manifest has no layers"
  "${c[@]}" pull "$REG/ci/crane:v1" "$WORK/pulled.tar"
  "${c[@]}" ls "$REG/ci/crane" | grep -qx v1 || fail "tag v1 not listed"
  "${c[@]}" catalog "$REG" | grep -qx ci/crane || fail "ci/crane not in catalog"

  log "crane: copy (cross-repository mount) and delete"
  "${c[@]}" copy "$REG/ci/crane:v1" "$REG/ci/crane-copy:v1"
  copy=$("${c[@]}" digest "$REG/ci/crane-copy:v1")
  [[ $copy == "$digest" ]] || fail "copied digest $copy != $digest"
  "${c[@]}" delete "$REG/ci/crane-copy@$digest"
  if "${c[@]}" digest "$REG/ci/crane-copy:v1" >/dev/null 2>&1; then
    fail "deleted manifest still resolves"
  fi
  "${c[@]}" digest "$REG/ci/crane:v1" >/dev/null || fail "original lost after deleting the copy"
  log "crane: OK"
}

run_docker() {
  # Keep credentials out of the user's ~/.docker, but keep talking to the
  # daemon of the current context (e.g. colima).
  if [[ -z ${DOCKER_HOST:-} ]]; then
    DOCKER_HOST=$(docker context inspect --format '{{.Endpoints.docker.Host}}')
    export DOCKER_HOST
  fi
  export DOCKER_CONFIG=$WORK/docker-config
  mkdir -p "$DOCKER_CONFIG"
  log "docker: $(docker info --format 'server {{.ServerVersion}}, storage driver {{.Driver}}, {{.DriverStatus}}')"

  local tag=$REG/ci/docker:v1
  mkdir -p "$WORK/ctx"
  printf 'foundry docker round trip %s\n' "$(date +%s)-$RANDOM$RANDOM" >"$WORK/ctx/payload"
  printf 'FROM scratch\nCOPY payload /payload\n' >"$WORK/ctx/Dockerfile"
  docker build -q -t "$tag" "$WORK/ctx" >/dev/null
  IMAGES+=("$tag")

  if [[ $AUTH == 1 ]]; then
    log "docker: unauthenticated push must fail"
    if docker push -q "$tag" >/dev/null 2>&1; then
      fail "unauthenticated docker push succeeded"
    fi
    docker login "$REG" -u "$USERNAME" --password-stdin <<<"$PASSWORD"
  fi

  log "docker: push, remove, pull"
  docker push -q "$tag" >/dev/null
  local pushed pulled
  pushed=$(docker image inspect --format '{{index .RepoDigests 0}}' "$tag")
  docker rmi -f "$tag" >/dev/null
  docker pull -q "$tag" >/dev/null
  pulled=$(docker image inspect --format '{{index .RepoDigests 0}}' "$tag")
  [[ $pushed == "$pulled" ]] || fail "pulled $pulled, pushed $pushed"
  docker image inspect "$tag" >/dev/null
  log "docker: OK ($pushed)"
}

case $MODE in
  suite) start_registry; run_suite ;;
  crane) start_registry; run_crane ;;
  docker) start_registry; run_docker ;;
  *) echo "usage: $0 suite|crane|docker" >&2; exit 2 ;;
esac

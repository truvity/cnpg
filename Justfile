# Development commands for cnpg (charts and, later, clients)

# Disable go.work (parent workspace interferes with standalone module builds)
export GOWORK := "off"

# Format all Go files (gofmt + goimports via golangci-lint)
fmt:
    golangci-lint fmt ./...

# The chart render tests (chart_test.go et al. drive `helm template`; they
# carry the golden and negative roles for cnpg-cluster and cnpg-database)
# and the golden renders of the charts that use tests/cases: cnpg-platform.
test:
    hack/golden.sh
    go test ./... -coverprofile=coverage.out

# Regenerate the golden renders — review the diff before committing.
golden:
    hack/golden.sh update

# Run linters
lint:
    golangci-lint run ./...

# Lint + render the charts against a minimum values set. The schema is
# part of the lint: an unknown top-level key must fail the render, not
# be silently ignored (values.schema.json, component contract C2).
# cnpg-cluster's deep posture assertions live in chart_test.go (recipe
# `test`), not here.
charts:
    #!/usr/bin/env bash
    set -euo pipefail
    helm lint charts/cnpg-cluster --set clusterName=pg --set namespace=default --set profile=devel
    helm template pg charts/cnpg-cluster \
        --set clusterName=pg --set namespace=default --set profile=devel >/dev/null
    if helm template pg charts/cnpg-cluster \
        --set clusterName=pg --set namespace=default --set profile=devel --set bogusKey=1 >/dev/null 2>&1; then
      echo "cnpg-cluster: an unknown key rendered" >&2
      exit 1
    fi
    helm lint charts/cnpg-database --set clusterName=pg --set namespace=default --set databaseName=app
    helm template db charts/cnpg-database \
        --set clusterName=pg --set namespace=default --set databaseName=app >/dev/null
    if helm template db charts/cnpg-database \
        --set clusterName=pg --set namespace=default --set databaseName=app --set bogusKey=1 >/dev/null 2>&1; then
      echo "cnpg-database: an unknown key rendered" >&2
      exit 1
    fi
    for values in tests/invalid/cnpg-database/*.yaml; do
      if helm template invalid charts/cnpg-database -f "$values" >/dev/null 2>&1; then
        echo "RENDERED BUT SHOULD HAVE FAILED: $values" >&2
        exit 1
      fi
    done
    # cnpg-platform: schema, every negative fixture, and the example values.
    # Not `! helm template`: bash's `set -e` ignores a negated command.
    helm lint charts/cnpg-platform
    if helm template x charts/cnpg-platform --set bogusKey=1 >/dev/null 2>&1; then
      echo "cnpg-platform: an unknown key rendered" >&2
      exit 1
    fi
    for values in tests/invalid/cnpg-platform/*.yaml; do
      if helm template invalid charts/cnpg-platform -f "$values" >/dev/null 2>&1; then
        echo "RENDERED BUT SHOULD HAVE FAILED: $values" >&2
        exit 1
      fi
    done
    # cnpg-cluster: every negative fixture (each a refusal: schema or render-time).
    for values in tests/invalid/cnpg-cluster/*.yaml; do
      if helm template invalid charts/cnpg-cluster -f "$values" >/dev/null 2>&1; then
        echo "RENDERED BUT SHOULD HAVE FAILED: $values" >&2
        exit 1
      fi
    done
    helm template x charts/cnpg-platform -f examples/operator/cnpg-platform.values.yaml >/dev/null
    # cnpg-client (library chart): lints alone, renders through the test
    # consumer, and every negative fixture must be refused there.
    helm lint charts/cnpg-client
    helm lint tests/consumer --set client.cluster=pg --set client.role=app --set client.podSelector.app=app
    for values in tests/invalid/cnpg-client/*.yaml; do
      if helm template invalid tests/consumer -f "$values" >/dev/null 2>&1; then
        echo "RENDERED BUT SHOULD HAVE FAILED: $values" >&2
        exit 1
      fi
    done
    echo "charts: schema-validated lint and render OK"

# DEPRECATED: alias for `charts`, kept for anyone with the old name
# muscle-memoried in. Remove after the next tagged release.
chart-lint: charts

# The reason this repository can be public. Runs in CI as its own job.
leak-canary:
    hack/leak-canary.sh

# Run Go vulnerability check
vuln:
    govulncheck ./...

# Run go mod tidy
tidy:
    go mod tidy

# Clean build artifacts
clean:
    rm -rf dist/ coverage.out

# Everything CI runs on a pull request. `vuln` is deliberately excluded
# — a standard-library advisory with no released fix must not turn
# every PR red on a finding nobody can act on; security.yaml runs it
# separately, daily and un-required.
check: test lint charts leak-canary

# Build a snapshot release locally (no push, no tag) — exercises
# goreleaser's build, archive and changelog machinery (the cnpgctl archives).
snapshot:
    goreleaser release --snapshot --clean

# Package the Helm charts locally (the release workflow stamps the
# real version from the tag).
package:
    helm package charts/cnpg-cluster --destination dist/
    helm package charts/cnpg-database --destination dist/
    helm package charts/cnpg-platform --destination dist/
    helm package charts/cnpg-client --destination dist/

# DEPRECATED: alias for `package`. Remove after the next tagged release.
helm-package: package

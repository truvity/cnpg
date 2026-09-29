# Development commands for cnpg-cluster (chart-only repository)

# Disable go.work (parent workspace interferes with standalone module builds)
export GOWORK := "off"

# Format all Go files (gofmt + goimports via golangci-lint)
fmt:
    golangci-lint fmt ./...

# Run the chart render tests (chart_test.go et al. drive `helm
# template`). This repository has no golden renders — its Go tests
# carry that role, including the negative (schema-rejected) cases.
test:
    go test ./... -coverprofile=coverage.out

# Run linters
lint:
    golangci-lint run ./...

# Lint + render both charts against a minimum values set. The schema is
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
# goreleaser's GitHub-release/changelog machinery; this repo ships no
# binaries (builds are skipped), so there is nothing else for it to do.
snapshot:
    goreleaser release --snapshot --clean

# Package both Helm charts locally (the release workflow stamps the
# real version from the tag).
package:
    helm package charts/cnpg-cluster --destination dist/
    helm package charts/cnpg-database --destination dist/

# DEPRECATED: alias for `package`. Remove after the next tagged release.
helm-package: package

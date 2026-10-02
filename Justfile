# Development commands for cnpg (charts, cnpgctl and the client adapters)

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

# The kind conformance suite: cert-manager, approver-policy, trust-manager
# and the upstream operator on a disposable kind cluster, the charts on top,
# and what they do together asserted (conformance/, docs/conformance.md).
# Needs docker, kind, kubectl and helm and the network; NOT part of `check`,
# which stays free of all of them. Set CNPG_CONFORMANCE_BACKUP=1 to add the
# backup and point-in-time restore assertion, CNPG_CONFORMANCE_KEEP=1 to
# keep the cluster afterwards.
conformance:
    CNPG_CONFORMANCE=required go test ./conformance/ -count=1 -v -timeout 50m

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

# Parse every VMRule expression in the golden renders with the real
# VictoriaMetrics binary (truvity/observability rulecheck, pinned), so an
# expression the parser refuses can never ship. PrometheusRule goldens are
# not read by rulecheck; their expressions are the same strings.
rulecheck:
    go run github.com/truvity/observability/cmd/rulecheck@v0.19.0 \
        -vm-version v1.152.0 -vl-version v1.50.0 tests/golden/cnpg-platform

# The reason this repository can be public. Runs in CI as its own job.
leak-canary:
    hack/leak-canary.sh

# The TypeScript client adapter (clients/ts): lint, type check, unit tests,
# build. No Postgres needed; the conformance cases skip here.
clients-ts:
    #!/usr/bin/env bash
    set -euo pipefail
    cd clients/ts
    npm ci --no-audit --no-fund
    npm run lint
    npm run typecheck
    npm test
    npm run build
    # What a release would publish: the built output and the README only.
    # (The version is stamped from the tag at release time.)
    npm pack --dry-run

# The Kotlin client adapter (clients/kotlin): compile and unit tests, then the
# dry run of what a release publishes (the jar and the sources jar, no deploy).
# No Postgres needed; the conformance cases skip here.
clients-kotlin:
    #!/usr/bin/env bash
    set -euo pipefail
    cd clients/kotlin
    mvn -B -ntp clean verify
    mvn -B -ntp -DskipTests package
    # The version is stamped from the tag at release time.
    jar=$(ls target/cnpg-client-*.jar | grep -v -- '-sources' | head -1)
    listing="$(jar tf "$jar")"
    grep -q 'com/truvity/cnpg/CnpgPool.class' <<<"$listing"
    ls target/cnpg-client-*-sources.jar >/dev/null

# The Go client adapter against a real TLS PostgreSQL (digest-pinned image,
# certificates generated per run; clients/conformance/). Needs docker. The
# guard fails the recipe unless every case in clients/conformance/cases.txt
# ran and passed: a skipped suite is not a green one. NOT part of `check`
# (which stays free of docker); CI runs it as its own job.
clients-go-conformance:
    #!/usr/bin/env bash
    set -euo pipefail
    trap 'clients/conformance/pg-tls.sh down' EXIT
    eval "$(clients/conformance/pg-tls.sh up)"
    out="$(mktemp)"
    CNPG_CLIENTS_PG=required go test ./clients/go/... -run TestConformance -count=1 -json >"$out" || { grep -E '"Action":"(fail|output)"' "$out" | head -80 >&2; exit 1; }
    clients/conformance/guard.sh go "$out"

# The Kotlin client adapter against the same TLS PostgreSQL and case list. Two
# cases wait out HikariCP's 30 second minimum connection lifetime.
clients-kotlin-conformance:
    #!/usr/bin/env bash
    set -euo pipefail
    trap 'clients/conformance/pg-tls.sh down' EXIT
    eval "$(clients/conformance/pg-tls.sh up)"
    (cd clients/kotlin && CNPG_CLIENTS_PG=required mvn -B -ntp clean test -Dtest=ConformanceTest -Dsurefire.failIfNoSpecifiedTests=true)
    clients/conformance/guard.sh kotlin clients/kotlin/target/surefire-reports/TEST-com.truvity.cnpg.ConformanceTest.xml

# The TypeScript client adapter against the same TLS PostgreSQL and case list.
clients-ts-conformance:
    #!/usr/bin/env bash
    set -euo pipefail
    trap 'clients/conformance/pg-tls.sh down' EXIT
    eval "$(clients/conformance/pg-tls.sh up)"
    out="$(mktemp)"
    (cd clients/ts && npm ci --no-audit --no-fund && CNPG_CLIENTS_PG=required npx vitest run test/conformance.test.ts --reporter=json --outputFile="$out") || { cat "$out" >&2; exit 1; }
    clients/conformance/guard.sh ts "$out"

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
check: test lint charts leak-canary rulecheck clients-ts clients-kotlin

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

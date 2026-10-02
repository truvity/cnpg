# Development commands for cnpg (charts, cnpgctl and the client adapters)

crd-charts := "barman-cloud-crds"

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

# The CRD mirror charts ({{ crd-charts }}): upstream CRDs vendored verbatim
# as charts/<chart>/templates/crds.yaml, generated from charts/<chart>/crdctl.yaml
# by crdctl (truvity/ocictl, pinned below). The release workflow packages
# the chart directory as it stands, so the generated file is COMMITTED;
# nothing is fetched at release time. `just crds` regenerates it after a
# pinned_version bump and sets Chart.yaml's version and appVersion to that
# upstream version (the chart's version IS the upstream version; the release
# publishes it at that version, once, via hack/release-crd-charts.sh).
# Review the diff before committing.
crdctl := "github.com/truvity/ocictl/cmd/crdctl@v0.7.1"

crds:
    #!/usr/bin/env bash
    set -euo pipefail
    export GOWORK=off
    # crdctl reads the upstream repository through the GitHub API; CI hands the
    # job token over as GITHUB_PACKAGES_TOKEN, which lifts the anonymous rate limit.
    export GITHUB_TOKEN="${GITHUB_TOKEN:-${GITHUB_PACKAGES_TOKEN:-}}"
    for chart in {{ crd-charts }}; do
      go run {{ crdctl }} build --config "charts/$chart/crdctl.yaml"
      pinned="$(sed -n 's/^pinned_version: *"\{0,1\}v\{0,1\}\([^"]*\)"\{0,1\} *$/\1/p' "charts/$chart/crdctl.yaml")"
      sed -i "s/^version: .*/version: $pinned/; s/^appVersion: .*/appVersion: \"$pinned\"/" "charts/$chart/Chart.yaml"
      upstream="$(sed -n 's/^repo: *//p' "charts/$chart/crdctl.yaml")"
      sed -i "s|^  truvity.io/mirror: .*|  truvity.io/mirror: \"$upstream@$pinned\"|" "charts/$chart/Chart.yaml"
    done

# The vendored CRDs still equal what crdctl produces from the pinned upstream
# version: a hand edit or a pin bumped without `just crds` fails here.
crds-check: crds
    git diff --exit-code -- 'charts/*/templates/crds.yaml' 'charts/*/Chart.yaml'

# Lint the CRD mirror charts: they take no values, so any key must be refused
# (values.schema.json; one negative fixture per chart under tests/invalid/), and
# the render must contain CRDs only.
crd-charts-lint:
    #!/usr/bin/env bash
    set -euo pipefail
    for chart in {{ crd-charts }}; do
      helm lint "charts/$chart"
      # The chart's version is the upstream version in crdctl.yaml.
      pinned="$(sed -n 's/^pinned_version: *"\{0,1\}v\{0,1\}\([^"]*\)"\{0,1\} *$/\1/p' "charts/$chart/crdctl.yaml")"
      version="$(sed -n 's/^version: *\(.*\)$/\1/p' "charts/$chart/Chart.yaml")"
      appversion="$(sed -n 's/^appVersion: *"\{0,1\}\([^"]*\)"\{0,1\} *$/\1/p' "charts/$chart/Chart.yaml")"
      upstream="$(sed -n 's/^repo: *//p' "charts/$chart/crdctl.yaml")"
      mirror="$(sed -n 's/^  truvity.io\/mirror: *"\(.*\)"$/\1/p' "charts/$chart/Chart.yaml")"
      if [ "$mirror" != "$upstream@$pinned" ]; then
        echo "$chart: Chart.yaml truvity.io/mirror '$mirror' is not '$upstream@$pinned' (run just crds)" >&2
        exit 1
      fi
      if [ -z "$pinned" ] || [ "$version" != "$pinned" ] || [ "$appversion" != "$pinned" ]; then
        echo "$chart: Chart.yaml version '$version' / appVersion '$appversion' is not crdctl.yaml pinned_version '$pinned' (run just crds)" >&2
        exit 1
      fi
      if helm template x "charts/$chart" --set bogusKey=1 >/dev/null 2>&1; then
        echo "$chart: an unknown key rendered" >&2
        exit 1
      fi
      for values in tests/invalid/"$chart"/*.yaml; do
        if helm template invalid "charts/$chart" -f "$values" >/dev/null 2>&1; then
          echo "RENDERED BUT SHOULD HAVE FAILED: $values" >&2
          exit 1
        fi
      done
      kinds="$(helm template x "charts/$chart" | grep -E '^kind:' | sort -u)"
      if [ "$kinds" != "kind: CustomResourceDefinition" ]; then
        echo "$chart: renders more than CustomResourceDefinitions:" >&2
        echo "$kinds" >&2
        exit 1
      fi
      echo "$chart: lint, schema and CRD-only render OK"
    done

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

# The Python client adapter (clients/python): lint, types, unit tests, then the
# dry run of what a release attaches to the GitHub release (wheel and sdist).
# No Postgres needed; the conformance cases skip here.
clients-python:
    #!/usr/bin/env bash
    set -euo pipefail
    cd clients/python
    uv sync --locked
    uv run ruff check .
    uv run ruff format --check .
    uv run mypy src tests
    uv run pytest
    rm -rf dist
    uv build
    ls dist/truvity_cnpg_client-*-py3-none-any.whl dist/truvity_cnpg_client-*.tar.gz >/dev/null

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

# The Python client adapter against the same TLS PostgreSQL and case list.
clients-python-conformance:
    #!/usr/bin/env bash
    set -euo pipefail
    trap 'clients/conformance/pg-tls.sh down' EXIT
    eval "$(clients/conformance/pg-tls.sh up)"
    out="$(mktemp)"
    (cd clients/python && uv sync --locked && CNPG_CLIENTS_PG=required uv run pytest tests/test_conformance.py -q --junitxml="$out") || { cat "$out" >&2; exit 1; }
    clients/conformance/guard.sh python "$out"

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
check: test lint charts crd-charts-lint crds-check leak-canary rulecheck clients-ts clients-kotlin clients-python

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
    for chart in {{ crd-charts }}; do helm package "charts/$chart" --destination dist/; done

# DEPRECATED: alias for `package`. Remove after the next tagged release.
helm-package: package

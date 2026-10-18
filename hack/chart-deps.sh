#!/usr/bin/env bash
# Resolve the chart dependencies that live in a registry (today: static-site
# depends on the platform's service-lib at oci://ghcr.io/truvity/charts). The
# archives go to charts/<chart>/charts/, which is git-ignored; the committed
# Chart.lock pins the digest. Local `file://` dependencies are not touched.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
for chart in "$root"/charts/*/; do
  if grep -q 'repository: oci://' "$chart/Chart.yaml"; then
    helm dependency build "$chart" >/dev/null
  fi
done

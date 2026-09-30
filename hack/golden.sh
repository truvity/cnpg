#!/usr/bin/env bash
# Golden renders: every tests/cases/<chart>/<case>/values.yaml is rendered
# with `helm template` and compared byte-for-byte against
# tests/golden/<chart>/<case>.yaml. A template change that alters output
# therefore shows up as a reviewable diff, with no cluster involved.
#
#   hack/golden.sh          compare (CI)
#   hack/golden.sh update   regenerate the golden files
set -euo pipefail

mode="${1:-check}"
root="$(cd "$(dirname "$0")/.." && pwd)"
fail=0

for values in "$root"/tests/cases/*/*/values.yaml; do
  case_dir="$(dirname "$values")"
  case_name="$(basename "$case_dir")"
  chart="$(basename "$(dirname "$case_dir")")"
  golden="$root/tests/golden/$chart/$case_name.yaml"

  # A case may pin its release name (tests/cases/<chart>/<case>/release) as
  # well as its namespace: object names and selectors that carry the release
  # are exactly what a second install in one namespace depends on.
  # cnpg-client is a library chart: it renders only through the test
  # consumer, which includes every helper.
  chart_dir="$root/charts/$chart"
  [ "$chart" = cnpg-client ] && chart_dir="$root/tests/consumer"

  release="$(cat "$case_dir/release" 2>/dev/null || echo "$chart")"

  rendered="$(helm template "$release" "$chart_dir" \
      --namespace "$(cat "$case_dir/namespace" 2>/dev/null || echo default)" \
      -f "$values")"

  if [ "$mode" = update ]; then
    mkdir -p "$(dirname "$golden")"
    printf '%s\n' "$rendered" > "$golden"
    echo "updated $golden"
    continue
  fi

  if ! diff -u "$golden" <(printf '%s\n' "$rendered"); then
    echo "GOLDEN MISMATCH: $chart/$case_name — run 'just golden' and review the diff"
    fail=1
  fi
done

[ "$fail" = 0 ] && echo "golden renders match"
exit $fail

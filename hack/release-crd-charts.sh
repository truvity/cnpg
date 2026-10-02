#!/usr/bin/env bash
# Publish the CRD mirror charts at THEIR OWN version, never the tag's.
#
# A CRD mirror chart's version is the upstream version it mirrors (the
# pinned_version in its crdctl.yaml, kept in Chart.yaml by `just crds`), so
# a chart is published only when that version is not yet in the registry:
# an existing version is skipped, never overwritten. A release that does not
# move an upstream pin therefore publishes nothing here.
#
# The shared release workflow (release-public.yaml) stamps the tag onto every
# chart it is given and has no per-chart version option, so these charts are
# not in its `charts` list; the release.yaml `crd-charts` job runs this.
#
#   CRD_CHARTS    space-separated chart directory names under charts/
#   HELMCTL       path to helmctl (default: helmctl on PATH)
#   REGISTRY      default ghcr.io
#   REPO_PREFIX   default truvity/charts
#   DRY_RUN=1     check and package, never push
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
helmctl="${HELMCTL:-helmctl}"
registry="${REGISTRY:-ghcr.io}"
prefix="${REPO_PREFIX:-truvity/charts}"
out="$(mktemp -d)"
trap 'rm -rf "$out"' EXIT

# 0 = the version exists, 1 = it does not. Anything else (network, auth, a
# 5xx) fails the script: "could not tell" must never read as "absent", or
# an outage would turn into an overwrite.
exists() {
  local repo="$1" version="$2" token code
  token="$(curl -fsS "https://${registry}/token?service=${registry}&scope=repository:${repo}:pull" \
    | python3 -c 'import json,sys; print(json.load(sys.stdin)["token"])')"
  code="$(curl -sS -o /dev/null -w '%{http_code}' -I \
    -H "Authorization: Bearer ${token}" \
    -H 'Accept: application/vnd.oci.image.manifest.v1+json, application/vnd.oci.image.index.v1+json' \
    "https://${registry}/v2/${repo}/manifests/${version}")"
  case "$code" in
    200) return 0 ;;
    404) return 1 ;;
    *) echo "::error::registry answered ${code} for ${repo}:${version}" >&2; exit 1 ;;
  esac
}

for chart in ${CRD_CHARTS:?CRD_CHARTS is required}; do
  dir="$root/charts/$chart"
  version="$(sed -n 's/^version: *"\{0,1\}\([^"]*\)"\{0,1\} *$/\1/p' "$dir/Chart.yaml")"
  pinned="$(sed -n 's/^pinned_version: *"\{0,1\}v\{0,1\}\([^"]*\)"\{0,1\} *$/\1/p' "$dir/crdctl.yaml")"
  if [ -z "$version" ] || [ "$version" != "$pinned" ]; then
    echo "::error::${chart}: Chart.yaml version '${version}' is not crdctl.yaml pinned_version '${pinned}' (run just crds)" >&2
    exit 1
  fi

  echo "::group::${chart} ${version}"
  if exists "${prefix}/${chart}" "$version"; then
    echo "${registry}/${prefix}/${chart}:${version} already exists; not overwriting"
  else
    "$helmctl" package --chart "$dir" --version "$version" --output "$out"
    if [ "${DRY_RUN:-}" = 1 ]; then
      echo "DRY RUN: would push ${registry}/${prefix}/${chart}:${version}"
    else
      "$helmctl" push --tgz "$out/${chart}-${version}.tgz" \
        --registry "$registry" --repository "${prefix}/${chart}" \
        --name "$chart" --version "$version"
    fi
  fi
  echo "::endgroup::"
done

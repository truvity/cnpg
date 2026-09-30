# 0002. The operator chart ships the platform's objects, not the operator

Status: accepted

## Context

`cnpg-operator` is meant to be the one thing a platform installs per
Kubernetes cluster. The obvious shape is an umbrella chart: the upstream
`cloudnative-pg` and `plugin-barman-cloud` charts as pinned dependencies,
with this repository's preset as their values.

## Findings

The release workflow packages a chart with `helmctl package`. The library
behind it can vendor a chart's dependencies (`helm dependency update` before
packaging), but the command does not expose that, and the shared release
workflow passes no flag for it. Packaging a chart that declares dependencies
therefore fails at `helm package`, because `charts/` is empty. Committing the
upstream tarballs to get around that would put third-party binaries in
history and make every renovate bump a large diff.

## Decision

`cnpg-operator` carries no subcharts. It renders the objects a platform adds
around the upstream operator: storage classes, a metrics network policy,
baseline alert rules and an admission policy for database roles. The operator
preset (inherited labels and annotations, in-place instance-manager updates,
rollout delays) and the barman-cloud plugin preset ship as values files under
`examples/operator/`, tested for shape in Go.

The rollout delay differs between a disposable environment and a production
one. That difference is an overlay file
(`cloudnative-pg.fast-rollout.values.yaml`), chosen by the platform per
environment. No template branches on an environment name.

Every feature is off by default, and no default names an estate: class names
and provisioner, namespaces, the scraper's selector and the tenant selector
are inputs, refused at render when a feature is on without them.

## Consequences

- A platform runs three Helm releases for the operator side (operator,
  plugin, `cnpg-operator`) instead of one.
- When the release path can package dependencies, the two upstream charts
  can move in as dependencies with enable conditions, and the preset files
  become the chart's own values. Nothing a consumer writes for
  `cnpg-operator` changes.
- Alert expressions are checked structurally in Go; `promtool` is not in the
  dev shell. Adding it would let `promtool check rules` run over the golden.

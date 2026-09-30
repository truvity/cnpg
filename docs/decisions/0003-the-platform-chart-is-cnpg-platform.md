# 0003. The platform chart is `cnpg-platform`; the operator stays upstream

Status: accepted. Supersedes the chart name in 0002.

## Context

0002 named the chart of cluster-wide add-ons `cnpg-operator`, and kept open
the option of folding the upstream `cloudnative-pg` and `plugin-barman-cloud`
charts into it as dependencies once the release tool could vendor them. The
name suggests that the chart is, or will be, the operator. It is not.

## Decision

The CloudNativePG operator is upstream's `cloudnative-pg` chart, and the
barman-cloud plugin is upstream's `plugin-barman-cloud`. A consumer installs
both directly. This repository ships only:

- recommended values for them under `examples/operator/`, and
- its own chart of the cluster-wide add-ons upstream lacks: storage classes,
  a metrics network policy, baseline alert rules and the admission policy
  that refuses superuser-type `DatabaseRole`s.

That chart is named `cnpg-platform`. The chart never contained the operator,
and its name no longer hints that it replaces it. It had not been released
under the old name, so the rename costs a consumer nothing. The rest of 0002
(no subcharts, everything off by default, no estate names, environment
differences as an overlay file) stands.

## Why upstream stays the operator

- A consumer takes upstream's security fixes the day upstream ships them,
  with no release of ours in between.
- Renovate tracks the upstream charts directly; there is no wrapper version
  to bump in lockstep.
- A wrapper chart would only make sense once the release tool can vendor
  dependencies, and even then only if it adds something a values file does
  not. A preset is a values file.

## Consequences

- A platform runs three Helm releases on the operator side (operator,
  plugin, `cnpg-platform`), as under 0002.
- `charts/cnpg-operator` becomes `charts/cnpg-platform`, and
  `examples/operator/cnpg-operator.values.yaml` becomes
  `examples/operator/cnpg-platform.values.yaml`. The upstream operator
  examples are unchanged.

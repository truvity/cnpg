# Changelog

One heading per tag, newest first, prose written for a consumer deciding
whether to move. See `docs/adoption.md` for the zero-diff gate every
upgrade is expected to clear.

## Unreleased

- **Feature:** new chart `cnpg-operator`, for the platform that installs the CloudNativePG operator once per Kubernetes cluster: optional StorageClasses, a metrics NetworkPolicy, baseline alert rules (PrometheusRule or VMRule) and a ValidatingAdmissionPolicy that refuses privileged `DatabaseRole`s. Everything is off by default and nothing is an estate default. It ships no subcharts; the operator and barman-cloud plugin presets are values under `examples/operator/`. `cnpg-cluster` and `cnpg-database` render byte-identical to v2.1.1.
- **Change:** the Go module path is `github.com/truvity/cnpg/v2`, ahead of the repository rename from `truvity/cnpg-cluster` to `truvity/cnpg`. No Go package imports it; chart OCI paths are chart-name based and do not change.
- First `docs/decisions/` records: `0001` (absorb and rename, charts by owner), `0002` (the operator chart).

## v2.1.1

- **Fix:** both `values.schema.json` files admit `global`. Helm hands `global` to every subchart, and the strict schema refused it (`additional properties 'global' not allowed`), so v2.0.0 to v2.1.0 could not be embedded as a dependency at all. Render diff: none; a consumer that pins the chart as a subchart can move to this release.

## v2.1.0

- **Feature:** `monitoring.podMonitor.enabled` (default `false`) renders a `monitoring.coreos.com/v1` `PodMonitor` for the cluster's instance pods (port `metrics`, `/metrics`), with optional `interval`, `metricRelabelings` and `labels`. The operator's own `spec.monitoring.enablePodMonitor` is deprecated upstream, so the chart renders the object itself. With the value at its default the render is byte-identical to v2.0.0; nothing to do on upgrade.

## v2.0.1

- **Fix:** with `serverTLS.issuerRef` set, the server `Certificate` now carries `secretTemplate.labels: {cnpg.io/reload: "true"}` and the `-server-ca` Secret carries the same label. CloudNativePG reloads a user-provided server Secret only when it has that label; without it a cert-manager renewal was not served until the instance restarted, so a client using `sslmode=verify-full` would fail once the old certificate expired. Render diff: the label on those two objects, nothing else. Charts that never set `serverTLS.issuerRef` are unchanged.

## v2.0.0

- **Breaking:** the `database` nodeSelector default and the unconditional `arch` toleration are gone; `scheduling.databasePool` defaults to empty and `scheduling.tolerations` to `[]`. Estates that relied on them set the values shown in `docs/adoption.md`.
- Both charts gain `values.schema.json`; negative Go tests cover rejected values.
- `appVersion` aligned to `1.30.0` for both charts.
- Repository brought onto the component template: `renovate.json` extends the shared preset, `.envrc`, `.editorconfig`, template recipe names (`charts`, `package`) with the old names kept as aliases, `docs/adoption.md` and `docs/reference.md`, README in the contract's heading order.

## v1.2.1

- No chart change. Routine CI dependency maintenance (ci-workflows pin
  bumps, a shared import-ban lint rule).

## v1.2.0

- **Backups and recovery can target any S3-compatible object store**,
  not only AWS. `backup.endpoint` (and `bootstrap.recovery.source.endpoint`)
  points the `ObjectStore` at Cloudflare R2, MinIO or Ceph RGW;
  `backup.existingSecret` supplies static keys where there is no pod
  identity to inherit; `backup.encryption: ""` omits the
  `x-amz-server-side-encryption` header for stores that reject it (R2
  among them). With every new value at its default the render is
  byte-identical to v1.1.8 — an AWS install sees no diff on upgrade.
- Documentation rework: the README gained a "Backups on an S3-compatible
  store" section and a public-consumer-facing pass over the rest.

## v1.1.8

- No chart change. CI dependency bump only.

## v1.1.7

- No chart change. CI dependency bump only.

## v1.1.6

- **Optional server certificate from a cert-manager issuer.** Setting
  `serverTLS.issuerRef.name` moves the cluster's server certificate to a
  named issuer; the client CA, replication and `pg_hba` are unaffected.
  Unset, the render is unchanged from v1.1.5 — the operator keeps
  minting its own CA.

## v1.1.5

- No chart change. CI dependency bump only.

## v1.1.4

- No chart change. CI and release-automation moves only: renovate and
  the devbox-update job now run from the shared fleet caller instead of
  a per-repo workflow, and auto-release mints its own token instead of
  holding a long-lived key.

## v1.1.3

- No chart change. Devbox package alignment.

## v1.1.2

- No chart change. Devbox package alignment and CI dependency bumps.

## v1.1.1

- No chart change. CI unified on the shared `ci-workflows` release lane
  (a security-labelled merge releases same-day instead of waiting for
  the weekly cron); the Go toolchain moved to a pinned 1.27 triad.

## v1.1.0

- **Profile-aware pool selector.** `scheduling.databasePool` becomes
  settable on the `devel` profile too, not only `prod`: an explicit
  value pins either profile to that Karpenter node pool; left unset,
  `prod` still defaulted to the `database` pool and `devel` rode the
  cluster's default pools (the pre-1.1.0 behaviour); an empty string
  opted out on either profile.

  (Superseded in the current chart: `scheduling.databasePool` no longer
  carries a profile-aware default at all — see `docs/adoption.md` for
  the migration from this release's behaviour.)

## v1.0.0

- Initial release. `charts/cnpg-cluster` (a CNPG `Cluster` with the
  devel/prod profile model, roles, declared databases, backup
  `ObjectStore`s and `ScheduledBackup`s) and `charts/cnpg-database` (a
  logical `Database` inside an existing cluster), extracted from their
  maintainers' internal estate.

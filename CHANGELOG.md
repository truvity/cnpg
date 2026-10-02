# Changelog

One heading per tag, newest first, prose written for a consumer deciding
whether to move. See `docs/adoption.md` for the zero-diff gate every
upgrade is expected to clear.

## Unreleased

## v2.8.0

- **Feature:** `charts/barman-cloud-crds`, the CustomResourceDefinitions of the barman-cloud plugin, moves here from `truvity/ocictl` and is published from here (`oci://ghcr.io/truvity/charts/barman-cloud-crds`) at the UPSTREAM version it mirrors, as it was from `truvity/ocictl`: `0.13.0` today, already in the registry, so the first release here publishes nothing for it. A release publishes the chart only when its version is not in the registry yet, and never overwrites one. The rendered CRDs are byte-identical to the ocictl chart. The CRDs are generated from `crdctl.yaml` by `just crds` (crdctl from a pinned `truvity/ocictl` release) and committed; `just crds-check` fails when they drift from the pinned upstream.
- No other chart changes.

## v2.7.0

- **Feature:** Kotlin/JVM adapter `clients/kotlin` (`com.truvity.cnpg:cnpg-client`, pgjdbc + HikariCP, Java 21 or later, built with Maven like the Keycloak providers). Same contract and the same 14 conformance cases as the Go and TypeScript adapters: `verify-full` only with the CA file as the sole trust root, no free-form JDBC URL, the CA, client certificate, key and password file read again for every new connection (`ReloadingDataSource`; pgjdbc reads the TLS files itself per connection), retry of connection-class errors only (the whole cause chain is read, because pgjdbc and HikariCP wrap), a health check bounded to 2s, and opt-in OpenTelemetry spans (API only) that carry the statement and never its arguments. Two things differ from the other languages and are documented in `clients/kotlin/README.md`: pgjdbc reads the client key as DER PKCS#8, so `PGSSLKEY` points at the `cnpg-client` chart's `key.der` (`keyDer: true`); and HikariCP will not retire a connection sooner than 30 seconds, so a shorter `CNPG_CLIENT_CONN_MAX_LIFETIME` is a configuration error instead of a silent 30 minutes.
- **Feature:** Python adapter `clients/python` (`truvity-cnpg-client`, import `truvity_cnpg`, psycopg 3 + psycopg_pool, Python 3.10 or later, built and locked with uv). Same contract and the same 14 conformance cases. Distributed as a wheel and an sdist attached to the GitHub release of the tag (GitHub Packages has no Python registry; release assets of a public repository need no token): `pip install "truvity-cnpg-client[binary] @ https://github.com/truvity/cnpg/releases/download/v2.7.0/truvity_cnpg_client-2.7.0-py3-none-any.whl"`. Public PyPI is a later decision. libpq reads the TLS files for every connection; the password file is read by the pool's connection class for every connection. Needs the client key at `0600` when owned by the process's own user (libpq's rule); the chart's `0440` works with `fsGroup`.
- **Feature:** the release workflow publishes the Kotlin artifact to GitHub Packages (Maven, `https://maven.pkg.github.com/truvity/cnpg`) at the tag's version, after the release job and next to the npm job. GitHub Packages needs a token in `settings.xml` even for public packages; see `clients/kotlin/README.md`. CI runs the same build as a dry run.
- **Test:** `clients/conformance/pg-tls.sh` also writes the client keys as DER PKCS#8 (`*.key.der`); new CI recipes `clients-kotlin`, `clients-kotlin-conformance`, `clients-python` and `clients-python-conformance`; `devbox.json` gains `temurin-bin-21`, `maven`, `python@3.14.4` and `uv`. `guard.sh` reads JUnit XML (`guard.sh kotlin|python <report>`).
- No chart changes: every chart renders byte-identical to v2.6.0.

## v2.6.0

- **Feature:** PostgreSQL client adapters under `clients/`: one contract (`clients/README.md`), one conformance case list, one implementation per language. They connect with `verify-full` only (a missing CA, or any weaker `sslmode`, is a configuration error), read the CA, client certificate, key and password file again for every new connection (so a cert-manager renewal needs no restart), retry only connection-class errors (what a CloudNativePG switchover looks like: `57P01`, `57P02`, `57P03`, `08xxx`, `25006`, `53300`, EOF, reset) with jittered backoff, expose a health check, and offer opt-in OpenTelemetry spans that carry the statement with its placeholders and never its arguments. Their inputs are the libpq environment the `cnpg-client` library chart already renders (`PGHOST`, `PGSSLROOTCERT`, `PGSSLCERT`, `PGSSLKEY`, ...) plus `CNPG_CLIENT_*` tuning variables. Python and Kotlin follow.
- **Feature:** Go adapter `github.com/truvity/cnpg/v2/clients/go/pgclient` (pgx/v5) and `.../pgclient/otelpg` (OpenTelemetry API only). It is part of the existing module, so the new minor is all a consumer needs: `go get github.com/truvity/cnpg/v2@v2.6.0`. The module's dependency graph gains pgx and the OpenTelemetry API; the charts and `cnpgctl` are unchanged.
- **Feature:** TypeScript adapter `clients/ts` (`@truvity/cnpg-client`, node-postgres). Published to GitHub Packages (`https://npm.pkg.github.com`) by the release workflow at the tag's version; the package contains only the built output and the README. GitHub Packages answers 401 to anonymous installs even for a public package, so installing needs an `.npmrc` with a token that has `read:packages` (`clients/ts/README.md`). Public registries (npmjs, PyPI, Maven Central) are a later step.
- **Test:** `clients/conformance/` runs each adapter against a real PostgreSQL that serves TLS: certificates generated per run, an image pinned by digest, 14 cases (verify-full, unknown CA, host name mismatch, refused weaker modes, password and certificate roles, certificate and password rotation, backend termination, a primary switch, statement timeout, permanent errors not retried, health, tracing without arguments). A guard fails the CI job unless every case ran and passed, so a skipped suite cannot be green. New CI recipes `clients-ts`, `clients-go-conformance` and `clients-ts-conformance`; `devbox.json` gains `nodejs@22`.
- No chart changes: every chart renders byte-identical to v2.5.0.

## v2.5.0

- **Feature:** `cnpg-platform` baseline alerts gain `CnpgBackupNotConfigured` (warning, `for: 1h`): `cnpg_collector_up{cnpg_cluster_backup_expected!="false"} unless on (namespace, pod) barman_cloud_cloudnative_pg_io_last_available_backup_timestamp`, because a cluster with no backups has no backup series and `CnpgBackupTooOld` read it as healthy. Values `alerts.backupNotConfigured.enabled` (`true`) and `.for` (`1h`), following `alerts.selector`. An install that renders the alerts gains one rule; set `enabled: false` to keep the old set.
- **Feature:** `cnpg-cluster` can declare that a cluster is deliberately not backed up: `backup.expected` (default `true`) and `backup.notExpectedReason`. `expected: false` with an empty reason fails the render. It adds the pod label `cnpg-cluster/backup-expected: "false"` via `spec.inheritedMetadata.labels` (applied to running pods in place, no restart) and `podTargetLabels` on the `PodMonitor`, so the series carry `cnpg_cluster_backup_expected="false"` and the new alert skips the cluster. A default render is unchanged. See `docs/reference.md`.

## v2.4.0

- **Behaviour change:** with `replication.enabled`, the chart-issued replication `Certificate` now writes Secret `<clusterName>-replication-tls` (and is named the same), not `<clusterName>-replication`; `spec.certificates.replicationTLSSecret` follows. `<clusterName>-replication`, `-ca` and `-server` are the names CloudNativePG generates itself when it manages certificates, and cert-manager never overwrites a Secret it did not issue, so adopting a Cluster the operator used to manage left the Certificate stuck in `IncorrectIssuer` while the Cluster, already on the per-database client CA, failed with `x509: certificate signed by unknown authority`. The other chart Secrets (`<clusterName>-client-ca`, `-server-tls`, `-server-ca`) do not collide and are unchanged. New value `replication.secretName` (default empty = `<clusterName>-replication-tls`). A cluster already adopted under the old name keeps working only if the operator Secret `<clusterName>-replication` was replaced by the cert-manager one; to keep that Secret, set `replication.secretName: <clusterName>-replication` (for `app-db`: `app-db-replication`), which renders exactly as before. Otherwise expect a new Secret and one reload of the instances. After adoption, the unused operator Secrets may be deleted once the Cluster is Ready; see `docs/adoption.md`.
- **Feature:** `cnpg-cluster` renders its `PodMonitor` by default. `monitoring.podMonitor.enabled` is now `true`, but the object is rendered only when the cluster serves the `monitoring.coreos.com/v1` `PodMonitor` API (`.Capabilities.APIVersions`), so a cluster without the Prometheus-operator CRDs installs unchanged; `helm template` needs `--api-versions monitoring.coreos.com/v1/PodMonitor` to show it. Set `enabled: false` to opt out. Where the CRD is served, the render gains one `PodMonitor` (job `<namespace>/<clusterName>`) and nothing else.
- **Feature:** the default `PodMonitor` drops the `cnpg_pg_settings_*` family (roughly 500-700 series per instance) before storage, except the eight settings the upstream CloudNativePG Grafana dashboard reads. New values `monitoring.podMonitor.dropPgSettings` (`true`) and `keepPgSettings`; your `metricRelabelings` are rendered after the drop and keep working. Set `dropPgSettings: false` to keep every series.
- **Docs:** no `cluster` label is added by the scrape. The exporter already emits `cluster="<clusterName>"` on its own series, which is what the upstream dashboard's `cluster` variable reads; a target label of that name would turn the exporter's into `exported_cluster`. See `docs/reference.md`.
- **Feature:** `cnpg-cluster` can render an ingress `NetworkPolicy` for the metrics port: `monitoring.networkPolicy.from` (NetworkPolicyPeer list, default `[]`). It is rendered, for TCP 9187 on this cluster's instance pods, only when `from` is non-empty; `monitoring.networkPolicy.enabled: true` with an empty `from` fails the render, because an empty peer list means allow-all.
- **Feature:** `cnpg-platform` baseline alerts guard against silent absence: `CnpgInstanceExporterDown` (`cnpg_collector_up == 0`) and `CnpgInstanceScrapeDown` (`up{container="postgres",job=~".+/.+"} == 0`, `alerts.scrapeDown.jobRegex`), each with its own `enabled` and `for`, both following `alerts.selector`. They are on by default, so an install that renders the alerts gains two rules; set `alerts.exporterDown.enabled` / `alerts.scrapeDown.enabled` to `false` to keep the old set. Golden renders and the rulecheck gate cover them.

## v2.3.0

- **Feature:** `cnpg-cluster` can use an existing server TLS Secret and server CA Secret by name. New optional values `serverTLS.existingSecret` (a `kubernetes.io/tls` Secret) and `serverTLS.existingCASecret` (a Secret holding `ca.crt`), both empty by default: when set, the Cluster gets `spec.certificates.serverTLSSecret` / `serverCASecret` with those names and the chart renders no `Certificate`, `Issuer` or CA `Secret` for the server side, so a cluster whose clients already verify an existing CA keeps that CA. The render fails when only one of the two is set, or when they are combined with `serverTLS.issuerRef`. The chart does not own these Secrets: their producer must label them `cnpg.io/reload`, or CloudNativePG does not serve a renewed certificate until the instance restarts. A default render is byte-identical. See `docs/reference.md`.

## v2.2.1

- **Fix:** `CnpgBackupTooOld` in `cnpg-platform` read `cnpg_collector_last_available_backup_timestamp`, which CloudNativePG deprecated in 1.26 and which only moves for the in-core Barman Cloud backup or volume snapshots; with the barman-cloud plugin (what `examples/operator/` installs) it stays 0, so the rule fired permanently and could never detect a stale backup. It now reads `barman_cloud_cloudnative_pg_io_last_available_backup_timestamp`, the plugin sidecar's own metric (plugin-barman-cloud v0.13.0, chart 0.7.0). A cluster that backs up another way should set `alerts.backupAge.enabled=false`. Every other rule is unchanged, and each keeps its own `enabled`, threshold and `for` inputs.
- **Test:** golden renders for the PrometheusRule and VMRule variants (all rules on, moved thresholds, single rule), and a `rulecheck` recipe and CI job that parses every rendered VMRule expression with the real VictoriaMetrics binary (truvity/observability `rulecheck` v0.19.0).

## v2.2.0

- **Feature:** new command `cnpgctl` (`cmd/cnpgctl`, archives for linux and darwin, amd64 and arm64, attached to each release; also `go run github.com/truvity/cnpg/v2/cmd/cnpgctl@<tag>`). `cnpgctl verify` runs read-only assertions against a live cluster, each printed PASS/FAIL with a reason, non-zero exit on any FAIL, `--output json` for machines: phase, ready instances and primary; the ContinuousArchiving condition and recoverability window; last successful backup within `--max-backup-age`; server CA, client CA and replication certificates labelled `cnpg.io/reload` and valid for `--min-cert-validity`; the `cnpg.io/reload` label on every user-provided Secret the Cluster names; and pg_hba sanity (no `trust`, every `map=` has pg_ident rows, the catch-all is last). The logic is the reusable package `pkg/verify`. See `docs/cnpgctl.md`. Nothing changes for the charts except that `cnpg-cluster` and `cnpg-database` now commit `appVersion: 0.0.0` like the other two, because the repository now builds a binary and the release stamps `appVersion` from the tag (the field was the upstream operator's version and no template reads it).
- **Test:** a kind conformance suite, `conformance/` (`just conformance`, and `.github/workflows/conformance.yaml` on changes to the charts, weekly, on tags and on demand). It installs cert-manager with its own approver disabled, approver-policy, trust-manager, the upstream CloudNativePG operator with `examples/operator/` values, then `cnpg-platform`, `cnpg-cluster` (trust objects on), `cnpg-database` (two roles with client certificates) and a pod using the `cnpg-client` helpers, and asserts: verify-full login with a role certificate; refusal of the same common name from another CA; approver-policy refusing another namespace and a common name outside the allow-list; the admission guard refusing a superuser `DatabaseRole`; healthy streaming replication with a key-less client CA; a renewed server certificate served without an instance restart; no password fallback for a certificate-only role; and, opt-in, a barman-cloud backup with a point-in-time restore. Gated by `CNPG_CONFORMANCE`, so `just check` and `go test ./...` stay free of docker and a cluster. See `docs/conformance.md`. No chart changes.
- **Feature:** new library chart `cnpg-client`, for application charts that connect to a cluster with a client certificate. Named templates `cnpg-client.volumes`, `cnpg-client.volumeMounts`, `cnpg-client.env` (libpq, plus an optional `postgresql://` or JDBC URL variable) and `cnpg-client.networkPolicy`; it refuses a missing cluster or role, a `pg_` role and any `sslmode` but `verify-full`. Contract: client certificate Secret `<cluster>-role-<role>`, server CA Secret `<cluster>-server-ca`. See `docs/connecting.md`. Nothing changes for existing charts.
- **Feature:** `cnpg-database` is the chart a product installs: besides `Database` objects it renders unprivileged `DatabaseRole` objects and a cert-manager client `Certificate` per role that asks for one (Secret `{cluster}-role-{role}`, `rotationPolicy: Always`, the `cnpg.io/reload` label, ECDSA P-256 by default, optional DER output, issuer `cnpg-{namespace}-{cluster}` unless overridden), behind ONE render-time guard that replaces per-product copies: cluster name, release-namespace match, database and owner shape, optional `expect.*` assertions, and refusal of `superuser`/`replication`/`bypassrls`/`createrole`/`createdb` and of any `pg_*` role name or membership. The default render is byte-identical to v2.1.1; `namespace` must now equal the release namespace and `databaseName`/`owner` are shape-checked (see `docs/adoption.md`).
- **Feature:** `cnpg-cluster` can give every database its own client CA. New optional values, all off by default (a default render is byte-identical): `trust.ca` (a self-signed root, a CA in cert-manager's namespace and ClusterIssuer `cnpg-<namespace>-<cluster>`), `trust.policy` (an approver-policy `CertificateRequestPolicy`, admitting only the database namespace, client-auth, the listed role CNs plus `streaming_replica`, ECDSA, with its RBAC), `trust.bundle` (a trust-manager `Bundle` delivering `<cluster>-client-ca`), `replication` (a `<cluster>-replication` certificate plus `certificates.clientCASecret` and `replicationTLSSecret` on the Cluster) and `people` (a `people` pg_ident map with an hba line before the catch-all). See `docs/authentication.md` and `docs/decisions/0004-per-database-ca.md`.
- **Fix:** `postgresql.extra_pg_hba` lines were appended after the catch-all `hostssl all all all cert` and so matched nothing (hba is first-match). The key is unchanged, for compatibility, and documented as reaching nothing; the new `postgresql.pgHba.beforeCatchAll` places lines where they can match. A `map=` with no pg_ident rows, and `pg_`-prefixed role names under `people` or `trust.policy.roles`, fail the render.
- **Feature:** new chart `cnpg-platform`, the cluster-wide platform add-ons that upstream's charts lack: optional StorageClasses, a metrics NetworkPolicy, baseline alert rules (PrometheusRule or VMRule) and a ValidatingAdmissionPolicy that refuses privileged `DatabaseRole`s. The CloudNativePG operator itself stays upstream's `cloudnative-pg` chart (and `plugin-barman-cloud`), installed directly and tracked by renovate; this repository ships only recommended values for them under `examples/operator/`. Everything is off by default and nothing is an estate default. The chart ships no subcharts. `cnpg-cluster` and `cnpg-database` render byte-identical to v2.1.1.
- **Change:** the repository was renamed from `truvity/cnpg-cluster` to `truvity/cnpg` (GitHub redirects the old name); the Go module path is `github.com/truvity/cnpg/v2` from the next release, and the GoReleaser `project_name` is now `cnpg`. No Go package imports it; chart OCI paths are chart-name based and do not change.
- First `docs/decisions/` records: `0001` (absorb and rename, charts by owner), `0002` (the operator chart), `0003` (the platform chart is `cnpg-platform`).

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

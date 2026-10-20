# cnpgctl

`cnpgctl` operates on CloudNativePG clusters built from these charts. It has
two commands, `verify` and `guard empty-bootstrap`, and neither writes: every
call they make to the Kubernetes API is a GET, and the guard's only call to
the object store is a list.

Install: the `cnpgctl_<version>_<os>_<arch>.tar.gz` archive of a release
(linux and darwin, amd64 and arm64; `checksums.txt` alongside), the image
`ghcr.io/truvity/cnpg/cnpgctl:<version>` (linux, amd64 and arm64), or

```sh
go run github.com/truvity/cnpg/v2/cmd/cnpgctl@<tag> verify --help
```

## verify

```sh
cnpgctl verify --namespace db --cluster pg [--context my-context] [--output json]
```

It reads the `Cluster`, and the Secrets the `Cluster` names, and prints one
line per assertion:

```
== cnpgctl verify: db/pg
  PASS  cluster phase healthy: phase "Cluster in healthy state"
  FAIL  last backup recent: last successful backup 41h0m0s ago, max 26h0m0s
== 14 passed, 1 failed, 2 skipped
```

The exit status is 1 if any assertion FAILs, 2 for a usage or connection
error, 0 otherwise. `SKIP` is an assertion that does not apply (an optional
Secret the cluster does not use) and never fails the run.

| flag | default | meaning |
|---|---|---|
| `--namespace` | required | namespace of the `Cluster` |
| `--cluster` | required | name of the `Cluster` |
| `--kubeconfig` | `$KUBECONFIG`, then `~/.kube/config` | kubeconfig file |
| `--context` | the current context | kubeconfig context |
| `--max-backup-age` | `26h` | how old the last successful backup may be |
| `--min-cert-validity` | `336h` (14 days) | how long each certificate must stay valid |
| `--pool-label` | off | `key=value`: assert every instance Pod runs on a node carrying this label (see below) |
| `--check-applied` | off | assert every `Database` and `DatabaseRole` of the cluster reports `status.applied: true` (see below) |
| `--check-identity` | off | assert the backup sidecar of every instance Pod has Pod Identity credentials (see below) |
| `--output` | `text` | `text` or `json` |

`--output json` prints `{namespace, cluster, results: [{name, status, message}]}`.

### Assertions

| assertion | passes when |
|---|---|
| cluster phase healthy | `status.phase` is `Cluster in healthy state` |
| instances ready | `status.readyInstances` equals `spec.instances` |
| primary known | `status.currentPrimary` is set |
| continuous archiving | the `ContinuousArchiving` condition is `True` |
| recoverability window | `status.firstRecoverabilityPoint` is set |
| last backup recent | `status.lastSuccessfulBackup` is within `--max-backup-age` |
| TLS server certificate | the Secret named by `spec.certificates.serverTLSSecret` is labelled `cnpg.io/reload: "true"` and its certificate is valid for `--min-cert-validity` |
| TLS server CA, client CA, replication certificate | same, for `spec.certificates.serverCASecret` / `clientCASecret` / `replicationTLSSecret`; when the spec does not name one, the contract name `<cluster>-server-ca`, `<cluster>-client-ca`, `<cluster>-replication` is checked (the chart names its replication Secret in the spec, so this fallback applies only to hand-written Clusters) if the Secret exists, and skipped if not |
| Secret `<name>` reload label | every user-provided Secret the `Cluster` references (the certificate Secrets above, `superuserSecret`, `bootstrap.initdb.secret`, managed roles' `passwordSecret`, `externalClusters` credentials) carries `cnpg.io/reload: "true"` |
| pg_hba has no trust | no `spec.postgresql.pg_hba` line uses `trust` |
| pg_hba maps have pg_ident rows | every `map=NAME` has at least one `spec.postgresql.pg_ident` row for `NAME` |
| pg_hba catch-all is last | no line follows a catch-all (`all all all`), where it could never match |

Three assertions are opt-in, because they read more than the `Cluster` and its
Secrets (a `get` of nodes, a `list` of Pods and of `databases` and
`databaseroles` in the namespace; still read-only):

| assertion | flag | passes when |
|---|---|---|
| instances on pool | `--pool-label key=value` | at least one Pod labelled `cnpg.io/cluster=<cluster>` exists, and every one is scheduled on a node whose label `key` equals `value`. The failure names each Pod and the node it is on. Use it where a database pool is tainted and labelled, to catch an instance that landed elsewhere |
| Database/DatabaseRole applied | `--check-applied` | every `Database` and `DatabaseRole` with `spec.cluster.name` equal to the cluster has `status.applied: true`. A missing `status.applied` fails ("not reported yet"); an `applied: false` fails with the object's `status.message`. SKIP when the cluster has none, or the CRDs are not installed |
| backup sidecar has Pod Identity | `--check-identity` | every instance Pod (labelled `cnpg.io/cluster=<cluster>`) has, on its `plugin-barman-cloud` container (the sidecar the barman-cloud plugin injects), both `AWS_CONTAINER_CREDENTIALS_FULL_URI` and `AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE` set. The failure names each Pod and the missing variable (names only; a value is never printed). FAIL when there are no instance Pods or when a Pod has no sidecar while another has one; SKIP when no Pod has the sidecar (the cluster does not archive through the plugin) |

`--check-identity` is the detective half of the Pod Identity check: EKS injects
the credentials when a pod is admitted, and a pod admitted before the
association was visible to the webhook has none for life (restarting the
container does not admit it again). The preventive half is the
`cnpg-projects` `identityCheck` hook ([backup-access.md](backup-access.md)).
It proves the environment is present, not which role it is bound to. After a
failure, delete the pod (replicas first, the primary by switchover) so it is
recreated and admitted afresh.

In Go, set `verify.Options.PoolLabel` (`"key=value"`, see `verify.ParsePoolLabel`)
`verify.Options.CheckApplied` and `verify.Options.CheckIdentity`. The data comes
from three optional interfaces, `verify.PlacementSource` (`InstanceNodes`,
`NodeLabels`), `verify.DeclarativeSource` (`DeclarativeObjects`) and
`verify.IdentitySource` (`InstancePods`), which `KubeSource`
implements; `verify.Source` is unchanged, so an existing implementation keeps
compiling, and with an option set a `Source` that lacks the interface gets a
FAIL result rather than a panic.

For a CA Secret holding a bundle, the validity check uses the longest-lived
certificate in it, so a retired root beside the live one does not fail it.

### What it does not do

- **Archive age.** The `Cluster` status carries the `ContinuousArchiving`
  condition but not the time of the last archived WAL segment (that is
  `pg_stat_archiver` inside the instance), and `verify` does not exec into
  pods. A stalled archive therefore shows as the condition turning `False`
  and, in time, as a stale `lastSuccessfulBackup`.
- **Reading object storage.** It does not list the bucket.

## guard empty-bootstrap

```sh
cnpgctl guard empty-bootstrap --namespace app --cluster pg \
  --destination s3://<bucket>/app/pg --server-name pg-g2 [--region <region>] \
  [--bootstrap initdb|recovery] [--endpoint <url>] \
  [--allow-non-empty-archive --reason "<why>"]
```

The check behind the `cnpg-cluster` chart's `guards.emptyBootstrap` hook
([reference.md](reference.md#guards-pre-sync-checks)). If a production
`Cluster` object is deleted and GitOps re-creates it with an `initdb`
bootstrap, it comes up as an EMPTY database while its archive still holds the
data; barman's own `check-wal-archive` notices only later, once the empty
database is already serving. The guard answers before anything is applied:

| state | verdict |
|---|---|
| `--bootstrap recovery` | PASS: a recovery continues an archive (nothing is read) |
| `--allow-non-empty-archive` with a `--reason` | PASS, the reason printed (nothing is read) |
| the `Cluster` exists and is not being deleted | PASS: an upgrade, nothing is bootstrapped (the archive is not read) |
| the `Cluster` does not exist (or is being deleted), and neither `<path>/<server-name>/base/` nor `<path>/<server-name>/wals/` holds an object | PASS: a new cluster, or a re-create under a new generation |
| the `Cluster` does not exist (or is being deleted), and either holds an object | FAIL (exit 1), naming the dir and what to do |
| the API or the store cannot be read, a flag is wrong | error (exit 2): it never passes when it cannot look |

`--destination` is the `ObjectStore`'s `destinationPath` and `--server-name`
the `Cluster` plugin's `serverName` (default: the cluster name), joined as
barman-cloud joins them. The Kubernetes client is the kubeconfig's, else the
in-cluster configuration; it reads the one `Cluster` (a `get`). The object
store is read with the AWS SDK's default credential chain (environment, EKS
Pod Identity, web identity, instance profile) with one `ListObjectsV2` of at
most one key per sub-directory, so `s3:ListBucket` on the bucket is all it
needs. `--region` defaults to the SDK's own (`AWS_REGION`), then `us-east-1`;
`--endpoint` addresses an S3-compatible store path-style.

The chart runs it as a hook `Job` under the cluster's read-only restore
ServiceAccount (`<cluster>-restore`, [backup-access.md](backup-access.md)).
What to do when it refuses: [runbooks.md](runbooks.md#the-empty-bootstrap-guard-refused-a-sync).

In Go: `guard.EmptyBootstrap` with a `guard.ClusterSource` (`verify.KubeSource`
implements it) and a `guard.ArchiveLister` (`guard.S3Lister`).

## Not covered: a restore drill

A `--restore` drill (create a rehearsal cluster from the backups, check it
leaves recovery, delete it) is deliberately not part of `verify`, which is
read-only. It would be a separate, explicitly mutating command.

# cnpgctl

`cnpgctl` operates on CloudNativePG clusters built from these charts. It has
one command, `verify`, and it never writes: every call it makes to the
Kubernetes API is a GET.

Install: the `cnpgctl_<version>_<os>_<arch>.tar.gz` archive of a release
(linux and darwin, amd64 and arm64; `checksums.txt` alongside), or

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
| TLS server CA, client CA, replication certificate | same, for `spec.certificates.serverCASecret` / `clientCASecret` / `replicationTLSSecret`; when the spec does not name one, the contract name `<cluster>-server-ca`, `<cluster>-client-ca`, `<cluster>-replication` is checked if the Secret exists, and skipped if not |
| Secret `<name>` reload label | every user-provided Secret the `Cluster` references (the certificate Secrets above, `superuserSecret`, `bootstrap.initdb.secret`, managed roles' `passwordSecret`, `externalClusters` credentials) carries `cnpg.io/reload: "true"` |
| pg_hba has no trust | no `spec.postgresql.pg_hba` line uses `trust` |
| pg_hba maps have pg_ident rows | every `map=NAME` has at least one `spec.postgresql.pg_ident` row for `NAME` |
| pg_hba catch-all is last | no line follows a catch-all (`all all all`), where it could never match |

For a CA Secret holding a bundle, the validity check uses the longest-lived
certificate in it, so a retired root beside the live one does not fail it.

### What it does not do

- **Archive age.** The `Cluster` status carries the `ContinuousArchiving`
  condition but not the time of the last archived WAL segment (that is
  `pg_stat_archiver` inside the instance), and `verify` does not exec into
  pods. A stalled archive therefore shows as the condition turning `False`
  and, in time, as a stale `lastSuccessfulBackup`.
- **Reading object storage.** It does not list the bucket.

## TODO: restore drill

A `--restore` drill (create a rehearsal cluster from the backups, check it
leaves recovery, delete it) is deliberately not part of `verify`, which is
read-only. It would be a separate, explicitly mutating command.

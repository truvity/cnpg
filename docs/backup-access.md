# Backup access (ACK)

`oci://ghcr.io/truvity/charts/cnpg-backup-access`

The source-account half of the per-cluster backup model in
[backupstore.md](backupstore.md). `backupstore` (Pulumi) makes the project's
bucket in the backup account and admits, per CNPG cluster, two roles by exact
ARN: `archive` and `restore`. This chart makes those roles and binds them to
ServiceAccounts, as AWS Controllers for Kubernetes objects, so a cluster's
access is declared next to the cluster:

| Object | Per | What |
| --- | --- | --- |
| `iam.services.k8s.aws/v1alpha1` `Role` `<rolePrefix>-<cluster>-archive` | cluster | Pod Identity trust (`sts:AssumeRole`, `sts:TagSession`, `aws:SourceAccount`), the archive inline policy, optional permissions boundary, tags |
| `Role` `<rolePrefix>-<cluster>-restore` | cluster with restore on | the same trust, the restore inline policy |
| `eks.services.k8s.aws/v1alpha1` `PodIdentityAssociation` (archive) | cluster | EKS cluster + namespace + the cluster's own ServiceAccount (named like the cluster) to the archive role |
| `PodIdentityAssociation` (restore) | cluster with restore on | EKS cluster + restore namespace + `<cluster>-restore` to the restore role |
| `v1` `ServiceAccount` `<cluster>-restore` | cluster with `restore.createServiceAccount` | the drill/restore ServiceAccount, in the restore namespace |

## Values

```yaml
global:
  accountID: "<source-account-id>"      # required: where the Roles live; aws:SourceAccount
  partition: aws
  region: ""                            # optional: services.k8s.aws/region on the associations
  eksClusterName: <eks-cluster>         # required
  rolePrefix: <prefix>                  # required: Role names <prefix>-<cluster>-archive|restore
  permissionsBoundaryARN: ""            # optional
  objectNamespace: ""                   # where the ACK objects live (default: the release namespace)
  useRoleRef: false                     # association by roleRef instead of the computed roleARN
  tags: {}                              # on every Role and association
bucket:
  name: <bucket>                        # required: the project's bucket in the backup account
clusters:
  - name: <cluster>                     # required; Role names derive from it, so unique
    namespace: <namespace>              # required
    prefix: <namespace>/<cluster>/      # default; must end in /
    archive:
      serviceAccount: <cluster>         # default: the cluster name
    restore:
      enabled: true                     # default
      serviceAccount: <cluster>-restore # default
      namespace: <namespace>            # default: the cluster's; a drill namespace is typical
      createServiceAccount: false       # render the ServiceAccount too
```

The render is refused (`templates/_validate.tpl`, fixtures in
`tests/invalid/cnpg-backup-access/`) for: an empty bucket, source account, EKS
cluster or role prefix; a cluster named twice; a prefix that does not end in
`/`, starts with `/` or holds a glob; a prefix used by two clusters; a
ServiceAccount bound to two roles; an IAM role name over 64 characters; an
unknown key (schema).

## The policies equal the library's

The inline policies are built in the template (bucket name, prefix, partition)
and are **byte for byte** the output of `backupstore.TenantRolePolicy` for an
SSE-S3, `SingleProject` bucket: the same Sids, statements, `AES256` condition
on `s3:PutObject`, the unprefixed `s3:ListBucket`, and no delete. The Go test
`charts/cnpg-backup-access/chart_test.go` renders the chart and compares each
Role's policy with the library's, so a change to one without the other fails
CI. The chart is SSE-S3 only (no KMS statement).

## The authority chain

The chart is hops 3 to 5 of the chain in [backupstore.md](backupstore.md#the-authority-chain):

1. the EKS cluster and namespace of the Postgres cluster, and its ServiceAccount (the cluster renders the ServiceAccount; `cnpg-cluster` names it like the cluster);
2. the **PodIdentityAssociation** maps (EKS cluster, namespace, ServiceAccount) to one role, and EKS stamps the session tags `eks-cluster-name`, `kubernetes-namespace` and `kubernetes-service-account`;
3. the **Role** carries the identity policy that allows only the cluster's prefix;
4. the bucket policy (from `backupstore` with the same role ARNs and, optionally, the session tags) admits exactly that role and denies it everything else.

Give `backupstore.Args.Tenants` the same role ARNs this chart creates:
`arn:<partition>:iam::<source-account-id>:role/<rolePrefix>-<cluster>-archive`
(`restore` for the other), prefix `<namespace>/<cluster>/`, and `SessionTags`
equal to the association (EKS cluster, namespace, ServiceAccount).

## With cnpg-cluster's ObjectStore

Point the barman-cloud `ObjectStore` (`cnpg-cluster`, `cnpg-project-platform`)
at `s3://<bucket>/<namespace>/<cluster>` with `s3Credentials.inheritFromIAMRole:
true` and `encryption: AES256` (the archive role's `s3:PutObject` is
conditioned on it), leave `retentionPolicy` unset (the role cannot delete; the
bucket lifecycle expires objects), and tag `cnpg-backup-retention: managed`
per backupstore.md. A restore or drill reads the archive prefix with the
`<cluster>-restore` ServiceAccount.

## ACK mechanics

- Both kinds carry `services.k8s.aws/adoption-policy: adopt-or-create` (the
  ACK controllers need the `ResourceAdoption` feature gate), as
  `cnpg-projects`' `backupAccess` does: an existing role or association with
  the same identity is adopted. `commonAnnotations` add to it.
- Create the objects in a namespace the controllers' own identity serves
  (`global.objectNamespace`), not a project namespace.
- `roleARN` is computed (`arn:<partition>:iam::<account>:role/<name>`), the
  pattern `cnpg-projects` uses, so the association does not wait for the Role to
  sync. `useRoleRef: true` writes `roleRef` (the ACK EKS CRD supports it)
  instead; ACK then resolves the ARN once the Role is ready.

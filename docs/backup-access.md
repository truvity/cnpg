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
| `iam.services.k8s.aws/v1alpha1` `Role` `<rolePrefix>-<cluster>-archive` | cluster | Pod Identity trust (`sts:AssumeRole`, `sts:TagSession`, pinned to the source account, the EKS cluster ARN and the binding's namespace and ServiceAccount), the archive inline policy, optional permissions boundary, tags |
| `Role` `<rolePrefix>-<cluster>-restore` | cluster with restore on | the same trust, the restore inline policy |
| `eks.services.k8s.aws/v1alpha1` `PodIdentityAssociation` (archive) | cluster | EKS cluster + namespace + the cluster's own ServiceAccount (named like the cluster) to the archive role |
| `PodIdentityAssociation` (restore) | cluster with restore on | EKS cluster + restore namespace + `<cluster>-restore` to the restore role |
| `v1` `ServiceAccount` `<cluster>-restore` | cluster with `restore.createServiceAccount` | the drill/restore ServiceAccount, in the restore namespace |

## Values

```yaml
global:
  accountID: "<source-account-id>"      # required: where the Roles live; aws:SourceAccount
  partition: aws
  region: <region>                      # required: EKS cluster region (aws:SourceArn); also services.k8s.aws/region
  eksClusterName: <eks-cluster>         # required
  rolePrefix: <prefix>                  # required: Role names <prefix>-<cluster>-archive|restore
  permissionsBoundaryARN: ""            # optional
  objectNamespace: ""                   # where the ACK objects live (default: the release namespace)
  useRoleRef: false                     # association by roleRef instead of the computed roleARN
  adoptAssociations: false              # true: adopt-or-create on the associations too (Roles always adopt)
  tags: {}                              # on every Role and association
bucket:
  name: <bucket>                        # required: the project's bucket in the backup account
clusters:
  - name: <cluster>                     # required; Role names derive from it, so unique
    namespace: <namespace>              # required
    prefix: <namespace>/<cluster>/      # default; must end in /
    archive:
      serviceAccount: <cluster>         # default: the cluster name
      association: true                 # false: the Role only, no PodIdentityAssociation (cutover period)
    restore:
      enabled: true                     # default
      serviceAccount: <cluster>-restore # default
      namespace: <namespace>            # default: the cluster's; a drill namespace is typical
      createServiceAccount: false       # render the ServiceAccount too
```

The render is refused (`templates/_validate.tpl`, fixtures in
`tests/invalid/cnpg-backup-access/`) for: an empty bucket, source account, region, EKS
cluster or role prefix; a cluster named twice; a prefix that does not end in
`/`, starts with `/` or holds a glob; a prefix used by two clusters; a
ServiceAccount bound to two roles; an IAM role name over 64 characters; an
unknown key (schema).

## The trust policy

Per role, as `cnpg-projects`' `backupAccess` pins it: `sts:AssumeRole` and
`sts:TagSession` for `pods.eks.amazonaws.com`, with `StringEquals`
`aws:SourceAccount`, `aws:RequestTag/kubernetes-namespace` and
`aws:RequestTag/kubernetes-service-account` (the namespace and ServiceAccount
of that role's association), and `ArnEquals` `aws:SourceArn`
`arn:<partition>:eks:<region>:<account>:cluster/<eksClusterName>`.

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

## Before the cutover: `archive.association: false`

EKS allows one Pod Identity association per (EKS cluster, namespace,
ServiceAccount). While a database's ServiceAccount is still bound to a legacy
role, `archive.association: false` renders the archive `Role` (so the bucket
and its policy can be prepared and the role named) but not the association.
Flip it to `true` in the sync that moves the ServiceAccount; the order is in
[moving-to-a-new-backup-bucket.md](moving-to-a-new-backup-bucket.md).

## With cnpg-cluster's ObjectStore

Point the barman-cloud `ObjectStore` (`cnpg-cluster`, `cnpg-project-platform`)
at `s3://<bucket>/<namespace>/<cluster>` with `s3Credentials.inheritFromIAMRole:
true` and `encryption: AES256` (the archive role's `s3:PutObject` is
conditioned on it), leave `retentionPolicy` unset (`backup.retentionDays: 0`;
the role cannot delete, the bucket lifecycle expires objects), and tag
`cnpg-backup-retention: managed` (`backup.tags`) and `keep` on the history files
(`backup.historyTags`), per backupstore.md. A restore or drill reads the archive prefix with the
`<cluster>-restore` ServiceAccount.

## ACK mechanics

- The Roles carry `services.k8s.aws/adoption-policy: adopt-or-create` (the
  ACK controllers need the `ResourceAdoption` feature gate): a role name is
  unique, so adopting an existing role of that name is safe. The
  PodIdentityAssociations carry **no** adoption policy by default. An
  association is identified in AWS by (EKS cluster, namespace, ServiceAccount),
  not by the object: with adoption, a new object adopts the existing
  association of that triple, and when the legacy object that also owns it is
  pruned its controller deletes the association in AWS, leaving the database
  with none while the new object still reports Synced. Without adoption the
  create is refused while another association holds the triple, which is the
  cue to remove that one first (docs/moving-to-a-new-backup-bucket.md).
  `global.adoptAssociations: true` stamps `adopt-or-create` on the
  associations too. `commonAnnotations` add to either.
- Create the objects in a namespace the controllers' own identity serves
  (`global.objectNamespace`), not a project namespace.
- `roleARN` is computed (`arn:<partition>:iam::<account>:role/<name>`), the
  pattern `cnpg-projects` uses, so the association does not wait for the Role to
  sync. `useRoleRef: true` writes `roleRef` (the ACK EKS CRD supports it)
  instead; ACK then resolves the ARN once the Role is ready.

## Proving the identity before the release (cnpg-projects)

A Pod Identity association is `Synced` seconds before EKS's pod-admission
webhook can see it, and credentials are injected only when a pod is admitted.
A database pod created in that gap archives nothing for its whole life, and
restarting its containers does not admit it again. `cnpg-projects` can run the
platform's standard check (`service-lib.identityCheck` from truvity/policy,
image `ghcr.io/truvity/k8s/identity-check`, pinned by digest in `images`) per
cluster row, as a `pre-install,pre-upgrade` hook Job that asks STS who it is,
as the Cluster's ServiceAccount, until the answer is the expected role:

```yaml
clusters:
  - values: {clusterName: example-pg, namespace: example, profile: prod}
    identityCheck:
      enabled: true                  # default false: nothing is rendered
      expectRole: <rolePrefix>-example-pg-archive   # required when enabled: the role NAME
      serviceAccount: example-pg     # default: values.clusterName
      expectAccount: "<account-id>"  # optional, see below
```

- **The ServiceAccount is the operator's.** CloudNativePG creates and owns the
  ServiceAccount named like the Cluster; the chart renders none. A hook runs
  before anything else in the release, so enable the check for a row only once
  its Cluster exists (a first install with the check on can never pass). The
  usual order is: apply the Cluster and the associations, wait for the
  Cluster, then set `identityCheck.enabled` on its row.
- **Network.** The check pod needs egress to the cloud API (STS). A namespace
  that denies egress by default needs a policy for it; the chart renders none.
  The pod is labelled `app.kubernetes.io/name: cnpg-projects-identity-check`
  and `app.kubernetes.io/component: <cluster>-identity-check`, which no CNPG
  (`cnpg.io/cluster`) or application selector matches.
- **`expectAccount`** (12 digits, quoted) is forwarded to service-lib, which
  passes `-expect-account` to the check once its release supports it (it needs
  `images.identity-check` from truvity/k8s v0.34.0 or later). The dependency
  pinned today does not, and the render then **fails** for a row that sets it
  rather than check nothing; bump `service-lib` and the image pin first.
- Running pods are checked by the other half: `cnpgctl verify
  --check-identity` ([cnpgctl.md](cnpgctl.md)).

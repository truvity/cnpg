# 0005. The per-project platform objects are a chart of their own

Status: accepted.

## Context

A platform that runs CloudNativePG for several projects sometimes does not
render a project's `Cluster` itself: the project's own chart does, and the
platform owns what sits around it, because the archive, its retention and its
credentials are the platform's, and so is the server certificate its policy
issues. Those objects are the `ObjectStore`, the `ScheduledBackup` and the
server `Certificate` with its CA `Secret`. They are per project, in the
project's namespace, and they are the same three shapes `cnpg-cluster`
renders for a cluster it owns. Until now the platform that needed them kept
templates for them in its own delivery repository, beside the facts that
choose which project gets which.

`cnpg-platform` is the wrong home: it is installed once per Kubernetes
cluster, has no per-project objects, and its meaning is "cluster-wide".
`cnpg-cluster` is the wrong home too: it renders the `Cluster`, and these are
exactly the objects used when it is NOT the one rendering it.

## Decision

A fifth chart, `cnpg-project-platform`, renders those three objects from
explicit entries (`objectStores`, `scheduledBackups`, `serverTLS`). It
computes no estate logic:

- which project gets which object, and whether a project has moved to this
  arrangement, is the caller's decision, passed as entries and as `enabled`;
- every name, namespace, bucket, prefix, issuer and root is an input (C13);
- it adds no `app.kubernetes.io` labels, and it lets the three server-TLS
  names be set, so a platform can take over objects another renderer already
  made without a rename. A rename of a `Cluster`, an `ObjectStore` or a backup
  object is a deletion of a database or its backups, and the chart is built so
  that it is never needed.

## Consequences

- A consumer's delivery repository keeps the decision and drops the
  templates; a render of the same inputs is held equal by the chart's own
  tests (parsed objects, not text).
- The chart is a fifth published OCI chart, versioned with the others.
- The credentials of an `ObjectStore` are the pod's ambient identity only. A
  store that needs a Secret, an endpoint or a CA is `cnpg-cluster`'s, which
  has those inputs; they are not duplicated here.

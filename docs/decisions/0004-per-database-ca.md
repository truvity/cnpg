# 0004. Every database verifies clients against its own CA

Status: accepted.

## Context

CloudNativePG can issue a client certificate for a `DatabaseRole`
(`spec.clientCertificate`, CNPG 1.30). Its documentation says the
certificate is "signed by the cluster's client CA" and stored in
`<role>-client-cert`. The operator signs it with the key it holds, so it
works only while the client CA is the operator's own CA (`<cluster>-ca`)
or a CA Secret that still contains `ca.key`.

The operator CA is one per cluster, but nothing ties a certificate to one
database: anything it signs is a valid client for every database that
trusts that CA, and a platform that shares the CA across clusters (to
avoid distributing one per database) makes a certificate for one database
valid on another.

## Decision

Each PostgreSQL cluster verifies its clients against its own CA, issued by
cert-manager, not against the operator's CA:

- `spec.certificates.clientCASecret` names a Secret without `ca.key`
  (`<cluster>-client-ca`, delivered by trust-manager), so the operator
  cannot sign client certificates at all.
- We supply `replicationTLSSecret` ourselves (a cert-manager leaf,
  CN `streaming_replica`). The operator accepts a client CA without its key
  only when it is also given the replication certificate.
- Client certificates come from a cert-manager `ClusterIssuer` named
  `cnpg-<namespace>-<cluster>`, whose CA key the product namespace cannot
  read, limited by an approver-policy `CertificateRequestPolicy` to the
  database namespace and the declared role names.

We therefore do not use `DatabaseRole.spec.clientCertificate`: with a
client CA that has no key the operator cannot sign it, and with the
operator CA it would undo the isolation above. The roles' certificates are
requested as cert-manager `Certificate`s instead (by `cnpg-database`).

## Consequences

- A certificate for one database does not work on another, and a leaked
  product namespace cannot mint one (it can only ask, and the policy
  bounds the ask).
- The platform installs `cnpg-cluster` once per cluster and owns the
  cluster-scoped objects; products install only `cnpg-database`.
- The platform must run cert-manager, approver-policy (with cert-manager's
  own approver disabled) and trust-manager with secret targets enabled.
- A trust-manager `Bundle` is cluster-scoped and named like its target
  Secret, so cluster names must be unique across namespaces where
  `trust.bundle` is used.
- Details of the model: `docs/authentication.md`.

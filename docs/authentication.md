# Authentication

How a client proves who it is to a cluster rendered by `charts/cnpg-cluster`,
and how the optional per-database trust chain is wired. Why it is shaped this
way: [`decisions/0004-per-database-ca.md`](decisions/0004-per-database-ca.md).

## Trust model

Every connection is `hostssl`. Clients authenticate with a certificate
(`cert`), apart from password roles, which get a `scram-sha-256` line.

By default the operator's per-cluster CA signs and verifies client
certificates. Turning on the `trust` values makes the database verify
clients against **its own** CA instead, so a certificate for one database
is not valid on another:

```
Issuer (self-signed)  ->  CA Certificate  (Secret in trust.namespace, holds the key)
                              |
                      ClusterIssuer cnpg-<namespace>-<cluster>
                              |  (only the database namespace, only the listed CNs:
                              |   CertificateRequestPolicy)
        Certificate <cluster>-replication-tls, and one per role (cnpg-database)
                              |
Bundle <cluster>-client-ca  ->  Secret <cluster>-client-ca  (ca.crt only, no key)
                              |
        Cluster spec.certificates.clientCASecret / replicationTLSSecret
```

- The CA key stays in `trust.namespace`; the product namespace can only ask
  the ClusterIssuer for a certificate, and the policy bounds the ask:
  `client auth` usage, a common name in `trust.policy.roles` or
  `streaming_replica`, ECDSA keys, no SANs.
- PostgreSQL checks the client certificate against `<cluster>-client-ca`,
  which has no key: the operator cannot mint client certificates from it.
  The operator accepts that only when it is also given
  `replicationTLSSecret`, which `replication.enabled` does.
- Platform prerequisites: cert-manager, approver-policy (cert-manager's own
  approver disabled) and trust-manager (secret targets enabled and
  authorized for the `<cluster>-client-ca` Secret).
- A `Bundle` is cluster-scoped and its Secret shares its name, so cluster
  names must be unique across namespaces when `trust.bundle` is on. The
  ClusterIssuer and CA names include the namespace and do not collide.

Everything is optional and off by default.

## hba ordering

`pg_hba.conf` is first-match, and a failed authentication is final: it does
not fall through to the next line. The chart emits, in order:

1. `hostssl all <owner> all scram-sha-256` and one scram line per password role;
2. the `people` line, when `people` is set;
3. `postgresql.pgHba.beforeCatchAll`;
4. `hostssl all all all cert`;
5. `postgresql.extra_pg_hba`.

`extra_pg_hba` (5) is after the catch-all and therefore matches nothing.
That was always so; the key is kept for compatibility. Use `beforeCatchAll`.

## People mapping

A person connects with a client certificate whose common name is their
email (issued by a root you add through `trust.bundle.extraCertificates`).
`people` lists `{email, role}`; the chart renders

```
pg_ident:  people ada@example.com readonly
pg_hba:    hostssl all readonly,editor all cert map=people
```

The hba line names only the roles people map to. A role certificate
(CN = role name) for another role is not affected by the map, which would
otherwise reject it, because a failed map does not fall through to the
catch-all. The consequence is that the people roles are for people: a
certificate with CN equal to such a role does not log in.

Refused at render time: a `map=` in any hba line with no rows behind it
(`map=people` with `people` empty, or any other map name), and role names
starting `pg_` in `people` or `trust.policy.roles`.

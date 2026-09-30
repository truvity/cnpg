# 0001. Absorb the operator, rename to `cnpg`, split charts by owner

Status: accepted

## Context

The repository began as one chart, `cnpg-cluster`, for a single PostgreSQL
cluster, with a second, `cnpg-database`, for a database inside a cluster
someone else owns. The operator itself, its storage classes, its metrics
policy and its alert rules lived in the platform's private repository, next
to the same estate's names. Products that declare databases carried their own
copies of the role and certificate plumbing.

## Decision

1. The repository becomes `truvity/cnpg` and owns everything about running
   CloudNativePG that is not an estate fact. The Go module is
   `github.com/truvity/cnpg/v2`: it stays on major 2, and breaking changes
   remain allowed until the charts are declared stable. GitHub redirects the
   old name while consumers move. Chart OCI paths are named for the chart,
   not the repository, and do not change.
2. Charts are split by who installs them:
   - `cnpg-operator`: the platform, once per Kubernetes cluster.
   - `cnpg-cluster`: the platform, once per PostgreSQL cluster, including that
     cluster's per-database trust objects.
   - `cnpg-database`: a product, once per database it owns. A product installs
     nothing else.
   - `cnpg-client`: a library chart an application includes to consume a
     database it was granted.
3. Client adapters for the four languages the estate uses come later, under
   `clients/`.
4. No live `Cluster` changes owner as part of this: the split is first a
   place to put things, then a migration a consumer takes on its own schedule
   under the zero-diff gate in `docs/adoption.md`.

## Consequences

- The estate's private repository shrinks to values: names, namespaces,
  selectors.
- Every chart obeys the component contract's C13: an estate fact is an input.
- The rename is an infrastructure change made where repositories are managed
  as code, not here.

// Package backupplan is the plan of a move from a shared backup bucket to
// per-project buckets: the phases a database passes through, and the rows of
// the cnpg-backup-access releases that follow from them.
//
// It has no cloud SDK and no estate name. The caller owns the facts (which
// projects exist, which phase each is in, the bucket prefix); the package
// owns the rules that turn them into rows, on top of pkg/backupnaming.
package backupplan

import (
	"fmt"
	"slices"
)

// Phase is where one database stands in the move from the legacy shared
// backup bucket to its per-project bucket. Phases only move forward, one
// merge each; docs/moving-to-a-new-backup-bucket.md is the runbook and
// explains why the order is what it is: EKS allows one Pod Identity
// association per (cluster, namespace, ServiceAccount), so the legacy
// association must be gone, and verified gone, before the new one is made.
type Phase string

const (
	// Legacy: the database archives to the shared bucket through the legacy
	// role. The default; the zero value of a caller's map lookup.
	Legacy Phase = "legacy"
	// Prepare: the cnpg-backup-access release mints the archive role WITHOUT
	// its association, and the restore role with its. Nothing the database
	// uses changes.
	Prepare Phase = "prepare"
	// Detach: as Prepare, and the legacy role drops the database's own
	// ServiceAccount, so the legacy association is deleted (the new one
	// cannot exist beside it: one association per ServiceAccount).
	Detach Phase = "detach"
	// Switched: the archive role is bound to the database's own
	// ServiceAccount and the cluster archives to the per-project bucket
	// under a new generation.
	Switched Phase = "switched"
)

// Phases lists the four phases in the order a database passes through them.
func Phases() []Phase { return []Phase{Legacy, Prepare, Detach, Switched} }

// ParsePhase returns the phase named s, or an error naming the valid ones.
func ParsePhase(s string) (Phase, error) {
	p := Phase(s)
	if !p.Valid() {
		return "", fmt.Errorf("backup phase %q: want one of %v", s, Phases())
	}

	return p, nil
}

// Valid reports whether p is one of the four phases.
func (p Phase) Valid() bool { return slices.Contains(Phases(), p) }

// OnNewPath reports whether the cnpg-backup-access release renders this
// database's roles: from Prepare on.
func (p Phase) OnNewPath() bool {
	return p == Prepare || p == Detach || p == Switched
}

// ArchiveAssociated reports whether the archive role is bound to the
// database's own ServiceAccount: only when Switched. Before that the legacy
// association holds the ServiceAccount, and EKS allows one association per
// (cluster, namespace, ServiceAccount), so a second one would be refused.
func (p Phase) ArchiveAssociated() bool { return p == Switched }

// LegacyBindsDatabase reports whether the legacy role still serves the
// database's own ServiceAccount: it does until Detach, when its association
// is deleted to make room for the archive role's.
func (p Phase) LegacyBindsDatabase() bool {
	return p != Detach && p != Switched
}

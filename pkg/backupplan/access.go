package backupplan

import (
	"errors"

	"github.com/truvity/cnpg/v2/pkg/backupnaming"
)

// Cluster is one CloudNativePG cluster that archives into a project bucket,
// under <Namespace>/<Name>/.
type Cluster struct {
	// Name is the Cluster resource's name; its ServiceAccount is named alike.
	Name string
	// Namespace is where the cluster runs.
	Namespace string
}

// Project is one project's database and its phase.
type Project struct {
	// Name is the project's name, the middle of its bucket name.
	Name     string
	Clusters []Cluster
	Phase    Phase
}

// AccessArgs describes the cnpg-backup-access releases of one environment.
type AccessArgs struct {
	// BucketPrefix is the stem of every bucket name. Required: the library
	// has no estate name of its own.
	BucketPrefix string
	// Env is the environment; it names the roles and the buckets. Required.
	Env string
	// Region is the buckets' region. Required.
	Region string
	// Projects are the databases of the environment, in the order the rows
	// should come out.
	Projects []Project
}

// AccessCluster is one cluster row of an AccessRow.
type AccessCluster struct {
	Name      string
	Namespace string
	// Association: the archive role is bound to the cluster's own
	// ServiceAccount (Phase.ArchiveAssociated).
	Association bool
}

// AccessRow is one release of the cnpg-backup-access chart: a bucket, the
// role prefix of its clusters' roles and the clusters, ready for the
// template to forward without building a name.
type AccessRow struct {
	Project    string
	Bucket     string
	RolePrefix string
	// Phase is the project's phase, for templates that vary by it.
	Phase    Phase
	Clusters []AccessCluster
}

// BuildAccess returns the cnpg-backup-access rows of an environment: one per
// project that is OnNewPath, in the order of a.Projects, with the bucket
// named by backupnaming.ProjectBucketName and the roles' prefix by
// backupnaming.RolePrefix. Projects still Legacy have no row; a phase that is
// not valid is an error. Nil when no project has a row.
func BuildAccess(a AccessArgs) ([]AccessRow, error) {
	var errs []error

	var out []AccessRow

	for _, p := range a.Projects {
		if !p.Phase.Valid() {
			errs = append(errs, errors.New("project "+p.Name+": backup phase "+string(p.Phase)+" is not valid"))

			continue
		}

		if !p.Phase.OnNewPath() {
			continue
		}

		name, err := backupnaming.ProjectBucketName(a.BucketPrefix, p.Name, a.Env, a.Region)
		if err != nil {
			errs = append(errs, err)

			continue
		}

		row := AccessRow{Project: p.Name, Bucket: name, RolePrefix: backupnaming.RolePrefix(a.Env), Phase: p.Phase}
		for _, c := range p.Clusters {
			row.Clusters = append(row.Clusters, AccessCluster{Name: c.Name, Namespace: c.Namespace, Association: p.Phase.ArchiveAssociated()})
		}

		out = append(out, row)
	}

	if err := errors.Join(errs...); err != nil {
		return nil, err
	}

	return out, nil
}

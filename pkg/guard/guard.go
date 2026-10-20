// Package guard holds the checks the cnpg-cluster chart runs before it lets a
// sync go ahead. Today there is one, EmptyBootstrap: a CloudNativePG Cluster
// that does not exist and would be bootstrapped with initdb must not come up
// as an empty database while its backup archive still holds the real data.
//
// The checks read through two small interfaces, ClusterSource (the
// Kubernetes API) and ArchiveLister (the object store), so they are tested
// against fakes; verify.KubeSource and S3Lister are the real ones. Nothing
// here writes anywhere.
package guard

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ClusterSource reads the postgresql.cnpg.io Cluster. verify.KubeSource
// implements it. A Cluster that does not exist is an error for which
// apierrors.IsNotFound is true.
type ClusterSource interface {
	Cluster(ctx context.Context, namespace, name string) (*unstructured.Unstructured, error)
}

// ArchiveLister reads the object store.
type ArchiveLister interface {
	// HasObjects reports whether at least one object exists under prefix
	// (a key prefix, not a directory) in bucket.
	HasObjects(ctx context.Context, bucket, prefix string) (bool, error)
}

// Bootstrap modes, as the cnpg-cluster chart spells bootstrap.mode.
const (
	BootstrapInitdb   = "initdb"
	BootstrapRecovery = "recovery"
)

// EmptyBootstrapOptions name the cluster and the archive it would write to.
type EmptyBootstrapOptions struct {
	Namespace string
	Cluster   string
	// Bootstrap is the Cluster's bootstrap method: initdb or recovery.
	Bootstrap string
	// Destination is the ObjectStore's destinationPath, s3://<bucket>[/<path>].
	Destination string
	// ServerName is the barman server directory under Destination (the
	// Cluster plugin's serverName; the chart defaults it to the cluster name).
	ServerName string
	// AllowNonEmptyArchive passes the check whatever the archive holds. It
	// needs a Reason.
	AllowNonEmptyArchive bool
	Reason               string
}

// Verdict is the outcome of a check. Pass false is a refusal; Message says
// why either way, in words meant for the person reading a failed sync.
type Verdict struct {
	Pass    bool   `json:"pass"`
	Message string `json:"message"`
}

// EmptyBootstrap decides whether a Cluster may be created with an initdb
// bootstrap. It passes when the bootstrap is recovery, when the Cluster
// already exists (an ordinary upgrade: nothing is bootstrapped), when the
// override is set, or when the archive directory <destination>/<serverName>/
// holds no base backup and no WAL. It refuses when the Cluster does not
// exist, the bootstrap is initdb and that directory already holds either:
// the new, empty database would serve while the data sits in the archive,
// and it would try to archive into another cluster's timeline.
//
// An error (an API or object-store failure, a bad option) is returned as
// such, never as a pass: a guard that cannot look must not wave the sync
// through.
func EmptyBootstrap(ctx context.Context, opt EmptyBootstrapOptions, clusters ClusterSource, archive ArchiveLister) (Verdict, error) {
	if opt.Namespace == "" || opt.Cluster == "" {
		return Verdict{}, errors.New("namespace and cluster are required")
	}

	switch opt.Bootstrap {
	case BootstrapRecovery:
		return Verdict{Pass: true, Message: "bootstrap is recovery: the cluster continues an archive, it does not start an empty one"}, nil
	case BootstrapInitdb:
	default:
		return Verdict{}, fmt.Errorf("bootstrap must be %s or %s, got %q", BootstrapInitdb, BootstrapRecovery, opt.Bootstrap)
	}

	if opt.AllowNonEmptyArchive {
		if strings.TrimSpace(opt.Reason) == "" {
			return Verdict{}, errors.New("the override needs a reason")
		}

		return Verdict{Pass: true, Message: "override set, the archive is not checked: " + opt.Reason}, nil
	}

	bucket, path, err := ParseDestination(opt.Destination)
	if err != nil {
		return Verdict{}, err
	}

	server := opt.ServerName
	if server == "" {
		server = opt.Cluster
	}

	obj, err := clusters.Cluster(ctx, opt.Namespace, opt.Cluster)

	switch {
	case apierrors.IsNotFound(err):
	case err != nil:
		return Verdict{}, fmt.Errorf("read Cluster %s/%s: %w", opt.Namespace, opt.Cluster, err)
	case obj.GetDeletionTimestamp() == nil:
		return Verdict{Pass: true, Message: fmt.Sprintf("Cluster %s/%s exists: an upgrade, nothing is bootstrapped", opt.Namespace, opt.Cluster)}, nil
	}
	// Not found, or being deleted: whatever is applied next creates it anew.

	dir := ArchiveDir(path, server)

	var found []string

	for _, sub := range []string{"base/", "wals/"} {
		has, err := archive.HasObjects(ctx, bucket, dir+sub)
		if err != nil {
			return Verdict{}, fmt.Errorf("list s3://%s/%s%s: %w", bucket, dir, sub, err)
		}

		if has {
			found = append(found, strings.TrimSuffix(sub, "/"))
		}
	}

	where := fmt.Sprintf("s3://%s/%s", bucket, dir)
	if len(found) == 0 {
		return Verdict{Pass: true, Message: fmt.Sprintf("Cluster %s/%s does not exist and %s is empty: a new cluster may start there", opt.Namespace, opt.Cluster, where)}, nil
	}

	return Verdict{Pass: false, Message: fmt.Sprintf(
		"REFUSED: Cluster %s/%s does not exist, its bootstrap is initdb, and its archive %s already holds %s. "+
			"Creating it would serve an EMPTY database while the data sits in that archive. "+
			"To get the data back, recover from it (bootstrap.mode recovery, source serverName %q) into a new serverName generation. "+
			"To start empty on purpose, bump the serverName generation (an empty directory passes). "+
			"Only if neither fits: guards.emptyBootstrap.allowNonEmptyArchive true with a reason.",
		opt.Namespace, opt.Cluster, where, strings.Join(found, " and "), server)}, nil
}

// ParseDestination splits an ObjectStore destinationPath, s3://<bucket>[/<path>],
// into the bucket and the path (no leading or trailing slash).
func ParseDestination(dest string) (bucket, path string, err error) {
	u, err := url.Parse(dest)
	if err != nil {
		return "", "", fmt.Errorf("destination %q: %w", dest, err)
	}

	if u.Scheme != "s3" || u.Host == "" {
		return "", "", fmt.Errorf("destination %q: want s3://<bucket>[/<path>]", dest)
	}

	return u.Host, strings.Trim(u.Path, "/"), nil
}

// ArchiveDir is the key prefix of a barman server directory, ending in "/":
// the path and the server name joined the way barman-cloud joins them
// (os.path.join, so a trailing slash on the path makes no difference).
func ArchiveDir(path, serverName string) string {
	path = strings.Trim(path, "/")
	if path == "" {
		return serverName + "/"
	}

	return path + "/" + serverName + "/"
}

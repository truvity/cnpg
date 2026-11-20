// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

// Package backupnaming holds the naming rules of the per-project backup
// model: the bucket of a project in an environment, the key prefix of a
// cluster inside it, the names of the archive and restore roles, and the
// server name of a backup generation. It imports no cloud SDK, so a chart
// renderer, a Pulumi program and a CLI can share one definition.
//
// Nothing here names an estate: the bucket-name prefix and every other
// component are inputs.
package backupnaming

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// BucketMaxLen and BucketMinLen are S3's bucket-name length limits.
const (
	BucketMaxLen = 63
	BucketMinLen = 3
)

// ProjectBucketName is the backup bucket of one project in one environment
// and region: <prefix>-<project>-<env>-<region>. One bucket per project per
// environment makes the project the blast radius, the retention boundary and
// the replica boundary. The prefix is the caller's own stem (it keeps the
// names apart from other buckets and, with the project and environment parts,
// makes them unique without an account id); it is required. The result is
// checked by ValidateBucketName.
func ProjectBucketName(prefix, project, env, region string) (string, error) {
	var errs []error

	for _, f := range []struct{ name, v string }{
		{"prefix", prefix}, {"project", project}, {"env", env}, {"region", region},
	} {
		if f.v == "" {
			errs = append(errs, fmt.Errorf("project bucket name: %s is empty", f.name))
		}
	}

	if err := errors.Join(errs...); err != nil {
		return "", err
	}

	name := fmt.Sprintf("%s-%s-%s-%s", prefix, project, env, region)

	if err := ValidateBucketName(name); err != nil {
		return "", err
	}

	return name, nil
}

// ReplicaBucketName is the replica of a project's bucket in the replica
// region: <prefix>-<project>-<env>-<region>, the same rule as
// ProjectBucketName with the replica's region. It differs from the primary's
// name by its region only, so the pair is easy to read; the prefix is a
// required input. The result is checked by ValidateBucketName.
func ReplicaBucketName(prefix, project, env, region string) (string, error) {
	return ProjectBucketName(prefix, project, env, region)
}

// ReplicationRoleName is the IAM role S3 assumes to replicate one project's
// bucket: <env>-pg-<project>-replication. IAM role names are account-wide,
// so the environment and the project keep them apart.
func ReplicationRoleName(env, project string) string {
	return fmt.Sprintf("%s-%s-replication", RolePrefix(env), project)
}

// ValidateBucketName reports a bucket name S3 refuses: not 3 to 63
// characters, outside [a-z0-9-], or starting or ending with a hyphen.
func ValidateBucketName(name string) error {
	if len(name) > BucketMaxLen {
		return fmt.Errorf("bucket name %q is %d characters, S3 allows %d", name, len(name), BucketMaxLen)
	}

	if len(name) < BucketMinLen {
		return fmt.Errorf("bucket name %q is %d characters, S3 needs at least %d", name, len(name), BucketMinLen)
	}

	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return fmt.Errorf("bucket name %q: %q is not in [a-z0-9-]", name, r)
		}
	}

	if name[0] == '-' || name[len(name)-1] == '-' {
		return fmt.Errorf("bucket name %q must not start or end with a hyphen", name)
	}

	return nil
}

// ClusterPrefix is the key prefix one cluster owns inside its project's
// bucket: <namespace>/<cluster>/. It is the write scope of its archive role.
func ClusterPrefix(namespace, cluster string) string {
	return namespace + "/" + cluster + "/"
}

// RolePrefix is the name prefix of the per-cluster archive and restore
// roles: <env>-pg. It is the cnpg-backup-access chart's global.rolePrefix.
func RolePrefix(env string) string {
	return env + "-pg"
}

// ArchiveRoleName is the archive role of a cluster: <env>-pg-<cluster>-archive.
func ArchiveRoleName(env, cluster string) string {
	return fmt.Sprintf("%s-%s-archive", RolePrefix(env), cluster)
}

// RestoreRoleName is the restore role of a cluster: <env>-pg-<cluster>-restore.
func RestoreRoleName(env, cluster string) string {
	return fmt.Sprintf("%s-%s-restore", RolePrefix(env), cluster)
}

// RoleNameMaxLen is IAM's role-name length limit.
const RoleNameMaxLen = 64

// RoleClass is the dotted suffix that says what a tenant role does.
type RoleClass string

const (
	// RoleClassArchive is the suffix of an archive role: ".pg-archive".
	RoleClassArchive RoleClass = "pg-archive"
	// RoleClassRestore is the suffix of a restore role: ".pg-restore".
	RoleClassRestore RoleClass = "pg-restore"
)

var dnsLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// TenantRoleName is the dotted name of a backup tenant role:
// <env>-<vendor>-<project>-<customer>[-<part>].<class>, with class
// RoleClassArchive ("pg-archive") or RoleClassRestore ("pg-restore"). It is
// the second shape beside ArchiveRoleName and RestoreRoleName
// (<env>-pg-<cluster>-archive|restore); during an overlap a tenant holds both
// and the bucket must admit both. Part is optional (empty leaves it out).
//
// Every segment is a DNS label (lower case letters, digits and hyphens, not
// starting or ending with a hyphen, at most 63 characters), so the name is
// also a valid Kubernetes object name; the whole name is at most
// RoleNameMaxLen (IAM's limit).
func TenantRoleName(env, vendor, project, customer, part string, class RoleClass) (string, error) {
	var errs []error

	if class != RoleClassArchive && class != RoleClassRestore {
		errs = append(errs, fmt.Errorf("tenant role name: class %q is not %q or %q", class, RoleClassArchive, RoleClassRestore))
	}

	segments := []string{env, vendor, project, customer}

	for i, field := range []string{"env", "vendor", "project", "customer"} {
		if err := validateSegment(field, segments[i], false); err != nil {
			errs = append(errs, err)
		}
	}

	if part != "" {
		if err := validateSegment("part", part, true); err != nil {
			errs = append(errs, err)
		}

		segments = append(segments, part)
	}

	if err := errors.Join(errs...); err != nil {
		return "", err
	}

	name := strings.Join(segments, "-") + "." + string(class)

	if len(name) > RoleNameMaxLen {
		return "", fmt.Errorf("tenant role name %q is %d characters, IAM allows %d", name, len(name), RoleNameMaxLen)
	}

	return name, nil
}

func validateSegment(field, v string, optional bool) error {
	switch {
	case v == "" && optional:
		return nil
	case v == "":
		return fmt.Errorf("tenant role name: %s is empty", field)
	case len(v) > 63 || !dnsLabel.MatchString(v):
		return fmt.Errorf("tenant role name: %s %q is not a DNS label (a-z, 0-9 and -, not starting or ending with -, at most 63 characters)", field, v)
	}

	return nil
}

// RestoreServiceAccount is the ServiceAccount the restore role is bound to:
// <cluster>-restore.
func RestoreServiceAccount(cluster string) string {
	return cluster + "-restore"
}

// ServerName is the barman server name of one backup generation of a
// project's database: <project>-pg-<generation>. A new generation writes
// under a new server name, so an old one stays restorable beside it.
func ServerName(project, generation string) string {
	return fmt.Sprintf("%s-pg-%s", project, generation)
}

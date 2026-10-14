package backupstore

import (
	"errors"
	"fmt"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/s3"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// The object tags the lifecycle rules select on. Put them in the
// barman-cloud plugin's ObjectStore:
//
//	spec.configuration.tags:        {cnpg-backup-retention: managed}
//	spec.configuration.historyTags: {cnpg-backup-retention: keep}
//
// The plugin applies `tags` to base backups AND WAL files, and `historyTags`
// to timeline history files only (they override `tags` there). So "managed"
// marks what expires, and "keep" marks the small history files that must
// outlive every backup that spans a timeline switch: no rule matches them.
// An object with no tag (anything written outside the plugin) is also never
// expired by these rules.
const (
	LifecycleTagKey    = "cnpg-backup-retention"
	LifecycleTagManage = "managed"
	LifecycleTagKeep   = "keep"
)

// Defaults of Lifecycle.
const (
	DefaultWALMarginDays      = 7
	DefaultNoncurrentDays     = 7
	DefaultAbortMultipartDays = 7
)

// Lifecycle parameterizes the bucket's lifecycle configuration. Base backups
// expire after BaseBackupDays, WAL after BaseBackupDays+WALMarginDays.
//
// Why the margin: a base backup's first WAL segment is written BEFORE its
// backup.info (the segment that was current when the backup started), and
// S3 lifecycle works in whole days (an object expires at midnight UTC after
// the day count has elapsed). With one equal age for both, the oldest backup
// that still exists could have lost the WAL it needs to be consistent.
// WAL kept WALMarginDays longer guarantees that every backup that still
// exists has all the WAL from its start on.
//
// Rules, by id:
//   - "base-backups/<prefix>" and "wal/<prefix>", per archive tenant
//     (prefix = <tenant prefix><serverName>/base/ and /wals/), each also
//     filtered by the tag LifecycleTagKey=LifecycleTagManage. Without archive
//     tenants one rule "managed" covers the whole bucket by the tag and uses
//     the longer (WAL) age, because base backups and WAL cannot be told apart
//     without a prefix;
//   - "housekeeping": noncurrent versions expire after NoncurrentDays, an
//     incomplete multipart upload is aborted after AbortMultipartDays, and an
//     expired object delete marker is removed;
//   - "tier-storage-class", only when Transitions is set.
type Lifecycle struct {
	// BaseBackupDays is N: the retention of base backups. Required, above zero.
	BaseBackupDays int
	// WALMarginDays is added to N for WAL. Zero means DefaultWALMarginDays.
	WALMarginDays int
	// NoncurrentDays expires noncurrent versions. Zero means
	// DefaultNoncurrentDays.
	NoncurrentDays int
	// AbortMultipartDays aborts incomplete multipart uploads. Zero means
	// DefaultAbortMultipartDays.
	AbortMultipartDays int
	// Transitions are the optional storage-class tiering of the primary
	// bucket (IA, Glacier-IR). Not applied to the replica.
	Transitions []Transition
}

func orDefault(v, d int) int {
	if v == 0 {
		return d
	}

	return v
}

func (l Lifecycle) walDays() int {
	return l.BaseBackupDays + orDefault(l.WALMarginDays, DefaultWALMarginDays)
}

func (l Lifecycle) validate() []error {
	var errs []error

	if l.BaseBackupDays <= 0 {
		errs = append(errs, fmt.Errorf("args: Lifecycle.BaseBackupDays is %d, want above 0", l.BaseBackupDays))
	}

	for field, v := range map[string]int{
		"WALMarginDays": l.WALMarginDays, "NoncurrentDays": l.NoncurrentDays, "AbortMultipartDays": l.AbortMultipartDays,
	} {
		if v < 0 {
			errs = append(errs, fmt.Errorf("args: Lifecycle.%s is %d, want 0 (the default) or more", field, v))
		}
	}

	return errs
}

func tagged() pulumi.StringMap {
	return pulumi.StringMap{LifecycleTagKey: pulumi.String(LifecycleTagManage)}
}

func expireRule(id, prefix string, days int) *s3.BucketLifecycleConfigurationV2RuleArgs {
	rule := &s3.BucketLifecycleConfigurationV2RuleArgs{
		Id:         pulumi.String(id),
		Status:     pulumi.String("Enabled"),
		Expiration: &s3.BucketLifecycleConfigurationV2RuleExpirationArgs{Days: pulumi.Int(days)},
	}

	if prefix == "" {
		rule.Filter = &s3.BucketLifecycleConfigurationV2RuleFilterArgs{
			Tag: &s3.BucketLifecycleConfigurationV2RuleFilterTagArgs{
				Key: pulumi.String(LifecycleTagKey), Value: pulumi.String(LifecycleTagManage),
			},
		}
	} else {
		rule.Filter = &s3.BucketLifecycleConfigurationV2RuleFilterArgs{
			And: &s3.BucketLifecycleConfigurationV2RuleFilterAndArgs{Prefix: pulumi.String(prefix), Tags: tagged()},
		}
	}

	return rule
}

// LifecycleRules builds the rules of l for the given tenants. withTiering
// adds the Transitions rule (the primary only).
func LifecycleRules(l Lifecycle, tenants []Tenant, withTiering bool) (s3.BucketLifecycleConfigurationV2RuleArray, error) {
	if err := errors.Join(l.validate()...); err != nil {
		return nil, err
	}

	var rules s3.BucketLifecycleConfigurationV2RuleArray

	archivers := 0

	for _, t := range tenants {
		if t.Purpose != PurposeArchive {
			continue
		}

		archivers++
		server := t.Prefix + t.serverName() + "/"
		rules = append(rules,
			expireRule("base-backups/"+server, server+"base/", l.BaseBackupDays),
			expireRule("wal/"+server, server+"wals/", l.walDays()),
		)
	}

	if archivers == 0 {
		rules = append(rules, expireRule("managed", "", l.walDays()))
	}

	rules = append(rules, &s3.BucketLifecycleConfigurationV2RuleArgs{
		Id:     pulumi.String("housekeeping"),
		Status: pulumi.String("Enabled"),
		Filter: &s3.BucketLifecycleConfigurationV2RuleFilterArgs{Prefix: pulumi.String("")},
		NoncurrentVersionExpiration: &s3.BucketLifecycleConfigurationV2RuleNoncurrentVersionExpirationArgs{
			NoncurrentDays: pulumi.Int(orDefault(l.NoncurrentDays, DefaultNoncurrentDays)),
		},
		AbortIncompleteMultipartUpload: &s3.BucketLifecycleConfigurationV2RuleAbortIncompleteMultipartUploadArgs{
			DaysAfterInitiation: pulumi.Int(orDefault(l.AbortMultipartDays, DefaultAbortMultipartDays)),
		},
		Expiration: &s3.BucketLifecycleConfigurationV2RuleExpirationArgs{ExpiredObjectDeleteMarker: pulumi.Bool(true)},
	})

	if withTiering && len(l.Transitions) > 0 {
		var ts s3.BucketLifecycleConfigurationV2RuleTransitionArray
		for _, t := range l.Transitions {
			ts = append(ts, &s3.BucketLifecycleConfigurationV2RuleTransitionArgs{
				Days: pulumi.Int(t.Days), StorageClass: pulumi.String(t.StorageClass),
			})
		}

		rules = append(rules, &s3.BucketLifecycleConfigurationV2RuleArgs{
			Id:          pulumi.String("tier-storage-class"),
			Status:      pulumi.String("Enabled"),
			Filter:      &s3.BucketLifecycleConfigurationV2RuleFilterArgs{Prefix: pulumi.String("")},
			Transitions: ts,
		})
	}

	return rules, nil
}

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
	DefaultNoncurrentDays     = 7
	DefaultAbortMultipartDays = 7
)

// Lifecycle parameterizes the bucket's lifecycle configuration: ONE expiry
// age, RetentionDays, for every object the plugin tags "managed" (base
// backups and WAL alike), with no per-path rule and no WAL margin.
//
// Why one age is enough: the WAL a base backup needs is archived AFTER the
// backup starts, so it is never older than the backup. S3 lifecycle counts
// whole days and expires an object at midnight UTC after the day count has
// elapsed, so two objects created on the same UTC day expire together. With
// backups scheduled away from midnight UTC (02:00, say), a backup and the
// WAL it needs are created on the same UTC day and expire in the same
// midnight sweep; the oldest backup that still exists never loses WAL.
// THE BACKUP SCHEDULE MUST THEREFORE STAY AWAY FROM MIDNIGHT UTC: a backup
// that starts shortly before 00:00 UTC could have its WAL written the next
// day and outlive it, and one that spans midnight could lose its first
// segment a day early.
//
// Rules, by id:
//   - "managed": expiry at RetentionDays of objects tagged
//     LifecycleTagKey=LifecycleTagManage, bucket-wide. Use one bucket per
//     project so the age is the project's;
//   - "housekeeping": noncurrent versions expire after NoncurrentDays, an
//     incomplete multipart upload is aborted after AbortMultipartDays, and an
//     expired object delete marker is removed;
//   - "tier-storage-class", only when Transitions is set.
type Lifecycle struct {
	// RetentionDays is the age at which managed objects expire. Required,
	// above zero. The replica's lifecycle and the Object Lock governance
	// default retention use the same number.
	RetentionDays int
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

func (l Lifecycle) validate() []error {
	var errs []error

	if l.RetentionDays <= 0 {
		errs = append(errs, fmt.Errorf("args: Lifecycle.RetentionDays is %d, want above 0", l.RetentionDays))
	}

	for field, v := range map[string]int{
		"NoncurrentDays": l.NoncurrentDays, "AbortMultipartDays": l.AbortMultipartDays,
	} {
		if v < 0 {
			errs = append(errs, fmt.Errorf("args: Lifecycle.%s is %d, want 0 (the default) or more", field, v))
		}
	}

	return errs
}

// LifecycleRules builds the rules of l. withTiering adds the Transitions
// rule (the primary only).
func LifecycleRules(l Lifecycle, withTiering bool) (s3.BucketLifecycleConfigurationV2RuleArray, error) {
	if err := errors.Join(l.validate()...); err != nil {
		return nil, err
	}

	rules := s3.BucketLifecycleConfigurationV2RuleArray{
		&s3.BucketLifecycleConfigurationV2RuleArgs{
			Id:         pulumi.String("managed"),
			Status:     pulumi.String("Enabled"),
			Expiration: &s3.BucketLifecycleConfigurationV2RuleExpirationArgs{Days: pulumi.Int(l.RetentionDays)},
			Filter: &s3.BucketLifecycleConfigurationV2RuleFilterArgs{
				Tag: &s3.BucketLifecycleConfigurationV2RuleFilterTagArgs{
					Key: pulumi.String(LifecycleTagKey), Value: pulumi.String(LifecycleTagManage),
				},
			},
		},
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

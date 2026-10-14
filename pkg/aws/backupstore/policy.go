package backupstore

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	principalAWS = "AWS"

	polVersion   = "Version"
	polStatement = "Statement"
	polSid       = "Sid"
	polEffect    = "Effect"
	polAllow     = "Allow"
	polAction    = "Action"
	polResource  = "Resource"
	polPrincipal = "Principal"
	polCondition = "Condition"
	polDocDate   = "2012-10-17"

	condPrincipalARN = "aws:PrincipalArn"
	condStringEquals = "StringEquals"
	actListBucket    = "s3:ListBucket"
	actKMSDecrypt    = "kms:Decrypt"

	denySid = "DenyDeleteObjectVersionFromExternalAccount"

	replicationTrust = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow",` +
		`"Principal":{"Service":"s3.amazonaws.com"},"Action":"sts:AssumeRole"}]}`
)

type (
	// bucketPolicyParams is what the bucket's resource policy is built from.
	bucketPolicyParams struct {
		Partition       string
		BucketName      string
		SourceAccountID string
		BackupAccountID string
		WriterRole      string
		ReaderRole      string
		ListerRoles     []string
		ObjectActions   []string
		// Tenants replace the writer glob when set.
		Tenants []Tenant
		// GlobalDenies adds the denies that bind every principal outside the
		// backup account.
		GlobalDenies bool
		// SingleProject lists the whole bucket for every tenant.
		SingleProject bool
		// SSES3 adds the upload deny that requires the AES256 header.
		SSES3 bool
	}

	policyDocument struct {
		Version   string            `json:"Version"`
		Statement []policyStatement `json:"Statement"`
	}

	policyStatement struct {
		Sid       string         `json:"Sid"`
		Effect    string         `json:"Effect"`
		Principal map[string]any `json:"Principal"`
		Action    any            `json:"Action"`
		Resource  any            `json:"Resource,omitempty"`
		// NotResource is used by the tenant deny outside a prefix.
		NotResource any            `json:"NotResource,omitempty"`
		Condition   map[string]any `json:"Condition,omitempty"`
	}
)

// marshal renders a policy document without HTML escaping: IAM names and
// ARNs never hold <, > or &, so for them the bytes equal json.MarshalIndent's;
// a placeholder in a doc or a test stays readable. An empty indent is compact.
func marshal(v any, indent string) (string, error) {
	var buf strings.Builder

	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", indent)

	if err := enc.Encode(v); err != nil {
		return "", err
	}

	return strings.TrimSuffix(buf.String(), "\n"), nil
}

func roleARN(partition, account, role string) string {
	return fmt.Sprintf("arn:%s:iam::%s:role/%s", partition, account, role)
}

func rootARN(partition, account string) string {
	return fmt.Sprintf("arn:%s:iam::%s:root", partition, account)
}

func bucketARN(partition, bucket string) string {
	return fmt.Sprintf("arn:%s:s3:::%s", partition, bucket)
}

func exactRole(partition, account, role string) map[string]any {
	return map[string]any{
		condStringEquals: map[string]any{condPrincipalARN: roleARN(partition, account, role)},
	}
}

// buildBucketPolicy constructs the bucket resource policy: the source
// account's writer, reader and listers, and the deny that keeps any other
// account from destroying a version. Only the named roles pass, not the
// whole account: trust principals cannot hold wildcards, so the account root
// is narrowed by an aws:PrincipalArn condition.
func buildBucketPolicy(p bucketPolicyParams) string {
	bucket := bucketARN(p.Partition, p.BucketName)
	source := rootARN(p.Partition, p.SourceAccountID)
	roleCondition := map[string]any{
		"StringLike": map[string]any{condPrincipalARN: roleARN(p.Partition, p.SourceAccountID, p.WriterRole)},
	}

	var statements []policyStatement

	// With tenants the writer glob is not admitted: each tenant carries its
	// own rights on its own prefix.
	if len(p.Tenants) == 0 {
		statements = append(statements,
			policyStatement{
				Sid:       "AllowSourceAccountObjectAccess",
				Effect:    polAllow,
				Principal: map[string]any{principalAWS: source},
				Action:    writerObjectActions(p.ObjectActions),
				Resource:  bucket + "/*",
				Condition: roleCondition,
			},
			policyStatement{
				Sid:       "AllowSourceAccountListBucket",
				Effect:    polAllow,
				Principal: map[string]any{principalAWS: source},
				Action:    []string{actListBucket, "s3:ListBucketMultipartUploads"},
				Resource:  bucket,
				Condition: roleCondition,
			})
	}

	statements = append(statements, policyStatement{
		// Protects immutability: no external account can permanently
		// delete a version.
		Sid:       denySid,
		Effect:    "Deny",
		Principal: map[string]any{principalAWS: "*"},
		Action:    "s3:DeleteObjectVersion",
		Resource:  bucket + "/*",
		Condition: map[string]any{
			"StringNotEquals": map[string]string{"aws:PrincipalAccount": p.BackupAccountID},
		},
	})

	// After the deny, so adding a reader only appends statements.
	if p.ReaderRole != "" {
		cond := exactRole(p.Partition, p.SourceAccountID, p.ReaderRole)
		statements = append(statements,
			policyStatement{
				Sid: "AllowReaderGetObject", Effect: polAllow, Principal: map[string]any{principalAWS: source},
				Action: "s3:GetObject", Resource: bucket + "/*", Condition: cond,
			},
			policyStatement{
				Sid: "AllowReaderListBucket", Effect: polAllow, Principal: map[string]any{principalAWS: source},
				Action: actListBucket, Resource: bucket, Condition: cond,
			},
		)
	}

	statements = append(statements, listerStatements(p.Partition, bucket, p.SourceAccountID, p.ListerRoles)...)

	if p.GlobalDenies {
		statements = append(statements, globalDenyStatements(bucket, p.BackupAccountID)...)
	}

	if p.SSES3 {
		statements = append(statements, sseS3Deny(bucket))
	}

	statements = append(statements, tenantStatements(p.Partition, bucket, p.Tenants, p.SingleProject)...)

	data, _ := marshal(policyDocument{Version: polDocDate, Statement: statements}, "  ")

	return data
}

// buildReplicaPolicy is the replica's resource policy: the delete-version
// deny and one list-only grant per lister.
func buildReplicaPolicy(partition, replica, sourceAccountID, backupAccountID string, listers []string, globalDenies bool) (string, error) {
	arn := bucketARN(partition, replica)
	statements := []policyStatement{{
		Sid:       denySid,
		Effect:    "Deny",
		Principal: map[string]any{principalAWS: "*"},
		Action:    "s3:DeleteObjectVersion",
		Resource:  arn + "/*",
		Condition: map[string]any{
			"StringNotEquals": map[string]string{"aws:PrincipalAccount": backupAccountID},
		},
	}}
	statements = append(statements, listerStatements(partition, arn, sourceAccountID, listers)...)

	if globalDenies {
		statements = append(statements, globalDenyStatements(arn, backupAccountID)...)
	}

	return marshal(policyDocument{Version: polDocDate, Statement: statements}, "  ")
}

// listerStatements is one list-only grant per lister role. The bucket is the
// resource and s3:ListBucket the only action, so no statement here can reach
// an object; the role's own identity policy narrows the prefix.
func listerStatements(partition, bucket, sourceAccountID string, listers []string) []policyStatement {
	statements := make([]policyStatement, 0, len(listers))

	for _, role := range listers {
		statements = append(statements, policyStatement{
			Sid:       "AllowListerListBucket" + sidSuffix(role),
			Effect:    polAllow,
			Principal: map[string]any{principalAWS: rootARN(partition, sourceAccountID)},
			Action:    actListBucket,
			Resource:  bucket,
			Condition: exactRole(partition, sourceAccountID, role),
		})
	}

	return statements
}

// sidSuffix turns a role name into the CamelCase tail of a Sid, which may
// hold letters and digits only.
func sidSuffix(role string) string {
	var b strings.Builder

	upper := true

	for _, r := range role {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			if upper {
				b.WriteString(strings.ToUpper(string(r)))
				upper = false
			} else {
				b.WriteRune(r)
			}
		default:
			upper = true
		}
	}

	return b.String()
}

// writerObjectActions is the writer's object grant: the default set unless
// the caller names its own.
func writerObjectActions(override []string) []string {
	if override != nil {
		return override
	}

	return []string{
		"s3:PutObject",
		"s3:GetObject",
		"s3:DeleteObject",
		"s3:AbortMultipartUpload",
		"s3:ListMultipartUploadParts",
	}
}

// buildKeyPolicy admits the backup account's administrators (key
// management), the source account's writer roles (encrypt and decrypt) and,
// when named, one reader role (decrypt only): the second gate on top of the
// bucket policy.
func buildKeyPolicy(partition, sourceAccountID, backupAccountID, writerRole, readerRole string, tenants []Tenant) string {
	source := rootARN(partition, sourceAccountID)
	statements := []map[string]any{
		{
			polSid:       "BackupAccountKeyAdmin",
			polEffect:    polAllow,
			polPrincipal: map[string]any{principalAWS: rootARN(partition, backupAccountID)},
			polAction:    "kms:*",
			polResource:  "*",
		},
	}

	if len(tenants) == 0 {
		statements = append(statements, map[string]any{polSid: "WorkloadBackupRolesUseKey",
			polEffect:    polAllow,
			polPrincipal: map[string]any{principalAWS: source},
			polAction: []string{
				"kms:Encrypt", actKMSDecrypt, "kms:ReEncrypt*",
				"kms:GenerateDataKey*", "kms:DescribeKey",
			},
			polResource: "*",
			polCondition: map[string]any{
				"StringLike": map[string]any{condPrincipalARN: roleARN(partition, sourceAccountID, writerRole)},
			},
		})
	}

	statements = append(statements, tenantKeyStatements(partition, tenants)...)

	if readerRole != "" {
		statements = append(statements, map[string]any{
			polSid:       "WorkloadReaderRoleDecrypts",
			polEffect:    polAllow,
			polPrincipal: map[string]any{principalAWS: source},
			polAction:    []string{actKMSDecrypt, "kms:DescribeKey"},
			polResource:  "*",
			polCondition: exactRole(partition, sourceAccountID, readerRole),
		})
	}

	raw, err := json.MarshalIndent(map[string]any{polVersion: polDocDate, polStatement: statements}, "", "  ")
	if err != nil {
		panic(err)
	}

	return string(raw)
}

// buildReplicationPolicy is the replication role's policy: replicate from
// the primary to the replica and use both keys, never IAM writes.
func buildReplicationPolicy(src, dst, srcKey, dstKey string, objectLock, withKMS bool) (string, error) {
	doc := map[string]any{
		polVersion: polDocDate,
		polStatement: []map[string]any{
			{polEffect: polAllow, polAction: []string{"s3:GetReplicationConfiguration", actListBucket}, polResource: src},
			{polEffect: polAllow, polAction: replicationSourceActions(objectLock), polResource: src + "/*"},
			{polEffect: polAllow, polAction: []string{"s3:ReplicateObject", "s3:ReplicateDelete", "s3:ReplicateTags"}, polResource: dst + "/*"},
			{polEffect: polAllow, polAction: actKMSDecrypt, polResource: srcKey},
			{polEffect: polAllow, polAction: []string{"kms:Encrypt", "kms:GenerateDataKey*"}, polResource: dstKey},
		},
	}
	if !withKMS {
		// SSE-S3: no key on either side.
		st := doc[polStatement].([]map[string]any)
		doc[polStatement] = st[:3]
	}
	raw, err := json.Marshal(doc)

	return string(raw), err
}

// replicationSourceActions adds the retention reads S3 needs to replicate
// Object-Locked objects; without a lock the list is unchanged.
func replicationSourceActions(objectLock bool) []string {
	actions := []string{
		"s3:GetObjectVersionForReplication",
		"s3:GetObjectVersionAcl", "s3:GetObjectVersionTagging",
	}
	if objectLock {
		actions = append(actions, "s3:GetObjectRetention", "s3:GetObjectLegalHold")
	}

	return actions
}

// tenantKeyStatements admits the tenant roles to the KMS key (KMS mode
// only): archive roles encrypt and decrypt, restore-only roles decrypt.
func tenantKeyStatements(partition string, tenants []Tenant) []map[string]any {
	var out []map[string]any

	for _, group := range []struct {
		sid     string
		purpose Purpose
		actions []string
	}{
		{"TenantArchiveRolesUseKey", PurposeArchive, []string{
			"kms:Encrypt", actKMSDecrypt, "kms:ReEncrypt*", "kms:GenerateDataKey*", "kms:DescribeKey",
		}},
		{"TenantRestoreRolesDecrypt", PurposeRestore, []string{actKMSDecrypt, "kms:DescribeKey"}},
	} {
		var (
			roles, accounts []string
			seenRole        = map[string]bool{}
			seenAcct        = map[string]bool{}
		)

		for _, t := range tenants {
			if t.Purpose != group.purpose || seenRole[t.RoleARN] {
				continue
			}

			seenRole[t.RoleARN] = true
			roles = append(roles, t.RoleARN)

			if a := t.account(); !seenAcct[a] {
				seenAcct[a] = true
				accounts = append(accounts, rootARN(partition, a))
			}
		}

		if len(roles) == 0 {
			continue
		}

		out = append(out, map[string]any{
			polSid:       group.sid,
			polEffect:    polAllow,
			polPrincipal: map[string]any{principalAWS: accounts},
			polAction:    group.actions,
			polResource:  "*",
			polCondition: map[string]any{condStringEquals: map[string]any{condPrincipalARN: roles}},
		})
	}

	return out
}

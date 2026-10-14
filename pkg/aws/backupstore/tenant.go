package backupstore

import (
	"errors"
	"fmt"
	"strings"
)

// Purpose is what a tenant's role does with its prefix.
type Purpose string

const (
	// PurposeArchive writes a cluster's backups and WAL: put (with the
	// SSE-S3 header when the bucket enforces it), the multipart pair, tag
	// and read-back on the prefix. It can never delete.
	PurposeArchive Purpose = "archive"
	// PurposeRestore reads a prefix: get on it and list of it.
	PurposeRestore Purpose = "restore"
)

// Pod Identity session tags. EKS Pod Identity sets these on the session of
// every pod that assumes a role through an association, so a bucket policy
// can condition on the pod's cluster, namespace and ServiceAccount.
const (
	SessionTagCluster        = "eks-cluster-name"
	SessionTagNamespace      = "kubernetes-namespace"
	SessionTagServiceAccount = "kubernetes-service-account"
)

// SessionTags are the Pod Identity session tags a tenant's bucket-policy
// statements require (aws:PrincipalTag/...). An empty field requires nothing.
type SessionTags struct {
	// ClusterName is the EKS cluster ("eks-cluster-name").
	ClusterName string
	// Namespace is the pod's namespace ("kubernetes-namespace").
	Namespace string
	// ServiceAccount is the pod's ServiceAccount ("kubernetes-service-account").
	ServiceAccount string
}

// Tenant is one (role, prefix, purpose) the bucket admits: the unit of
// separation, per project and per Postgres cluster. A role may appear in
// several tenants (for example archive on its own prefix and restore on an
// old cluster's); the explicit deny outside prefixes then names all of them.
type Tenant struct {
	// RoleARN is the ARN of the role in the source account:
	// arn:<partition>:iam::<account-id>:role/<name>. An exact ARN, never a glob.
	RoleARN string
	// Prefix is the key prefix the role owns: "<namespace>/<cluster>/".
	// It must end in "/" and hold no glob.
	Prefix string
	// Purpose is PurposeArchive or PurposeRestore.
	Purpose Purpose
	// SessionTags, when set, are required of the session as well as the role
	// ARN, so a role shared by two ServiceAccounts is narrowed to one.
	SessionTags *SessionTags
}

// roleParts splits arn:<partition>:iam::<account>:role/<name>.
func roleParts(arn string) (partition, account, name string, ok bool) {
	p := strings.SplitN(arn, ":", 6)
	if len(p) != 6 || p[0] != "arn" || p[2] != "iam" || p[3] != "" || p[4] == "" || !strings.HasPrefix(p[5], "role/") {
		return "", "", "", false
	}

	name = strings.TrimPrefix(p[5], "role/")
	if name == "" {
		return "", "", "", false
	}

	return p[1], p[4], name, true
}

func (t Tenant) account() string {
	_, acct, _, _ := roleParts(t.RoleARN)

	return acct
}

func (t Tenant) validate(i int) []error {
	var errs []error

	field := func(f string) string { return fmt.Sprintf("args: Tenants[%d].%s", i, f) }

	if _, _, _, ok := roleParts(t.RoleARN); !ok || strings.ContainsAny(t.RoleARN, "*?") {
		errs = append(errs, fmt.Errorf("%s %q is not an exact role ARN (arn:<partition>:iam::<account>:role/<name>)", field("RoleARN"), t.RoleARN))
	}

	switch {
	case t.Prefix == "" || t.Prefix == "/":
		errs = append(errs, fmt.Errorf("%s is empty", field("Prefix")))
	case !strings.HasSuffix(t.Prefix, "/") || strings.HasPrefix(t.Prefix, "/") || strings.ContainsAny(t.Prefix, "*?"):
		errs = append(errs, fmt.Errorf("%s %q must be <namespace>/<cluster>/: no leading slash, a trailing slash, no glob", field("Prefix"), t.Prefix))
	}

	if t.Purpose != PurposeArchive && t.Purpose != PurposeRestore {
		errs = append(errs, fmt.Errorf("%s %q must be %q or %q", field("Purpose"), t.Purpose, PurposeArchive, PurposeRestore))
	}

	return errs
}

func validateTenants(tenants []Tenant) []error {
	var errs []error

	seen := map[string]bool{}
	archive := map[string]bool{}

	for i, t := range tenants {
		errs = append(errs, t.validate(i)...)

		key := t.RoleARN + "|" + t.Prefix + "|" + string(t.Purpose)
		if seen[key] {
			errs = append(errs, fmt.Errorf("args: Tenants[%d] repeats role %q, prefix %q, purpose %q", i, t.RoleARN, t.Prefix, t.Purpose))
		}

		seen[key] = true

		// Two archivers on one prefix would interleave one cluster's WAL.
		if t.Purpose == PurposeArchive {
			if archive[t.Prefix] {
				errs = append(errs, fmt.Errorf("args: Tenants[%d] is a second archive tenant of prefix %q", i, t.Prefix))
			}

			archive[t.Prefix] = true
		}
	}

	return errs
}

// identity is the condition that selects the tenant's sessions: the exact
// role ARN, and the session tags when given.
func (t Tenant) identity() map[string]any {
	eq := map[string]any{condPrincipalARN: t.RoleARN}

	if s := t.SessionTags; s != nil {
		for k, v := range map[string]string{
			SessionTagCluster:        s.ClusterName,
			SessionTagNamespace:      s.Namespace,
			SessionTagServiceAccount: s.ServiceAccount,
		} {
			if v != "" {
				eq["aws:PrincipalTag/"+k] = v
			}
		}
	}

	return map[string]any{condStringEquals: eq}
}

func (t Tenant) objectActions() []string {
	if t.Purpose == PurposeArchive {
		return []string{
			"s3:PutObject", "s3:AbortMultipartUpload", "s3:ListMultipartUploadParts",
			"s3:GetObject", "s3:PutObjectTagging",
		}
	}

	return []string{"s3:GetObject"}
}

func prefixResource(bucket, prefix string) string { return bucket + "/" + prefix + "*" }

func titled(p Purpose) string { return strings.ToUpper(string(p)[:1]) + string(p)[1:] }

// tenantStatements is the bucket-policy side of the tenants: per tenant an
// allow on its prefix and a prefix-limited list, per role one explicit deny
// outside all of its prefixes.
//
// s3:PutObjectTagging is part of archive because barman-cloud sends its
// --tags with the upload, and S3 authorizes that tagging as its own action.
// Tagging is not deletion; a retag can only keep an object longer.
func tenantStatements(partition, bucket string, tenants []Tenant, singleProject bool) []policyStatement {
	var out []policyStatement

	for i, t := range tenants {
		n := fmt.Sprintf("Tenant%d%s", i+1, titled(t.Purpose))
		principal := map[string]any{principalAWS: rootARN(partition, t.account())}

		out = append(out, policyStatement{
			Sid: "Allow" + n + "Objects", Effect: polAllow, Principal: principal,
			Action: t.objectActions(), Resource: prefixResource(bucket, t.Prefix), Condition: t.identity(),
		})

		// A per-project bucket lists whole: barman-cloud's HeadBucket carries
		// no s3:prefix, and the bucket holds only this project's keys. Else
		// the list is limited to the tenant's prefix.
		list := t.identity()
		if !singleProject {
			list["StringLike"] = map[string]any{"s3:prefix": []string{t.Prefix + "*"}}
		}

		out = append(out, policyStatement{
			Sid: "Allow" + n + "ListBucket", Effect: polAllow, Principal: principal,
			Action: actListBucket, Resource: bucket, Condition: list,
		})
	}

	// One pair of denies per distinct role ARN, in first-seen order: the
	// session tags narrow an allow, but the deny is about the role.
	var order []string

	byRole := map[string][]Tenant{}

	for _, t := range tenants {
		if _, ok := byRole[t.RoleARN]; !ok {
			order = append(order, t.RoleARN)
		}

		byRole[t.RoleARN] = append(byRole[t.RoleARN], t)
	}

	for k, arn := range order {
		group := byRole[arn]
		resources := []string{bucket}
		prefixes := make([]string, 0, len(group))

		for _, t := range group {
			resources = append(resources, prefixResource(bucket, t.Prefix))
			prefixes = append(prefixes, t.Prefix+"*")
		}

		role := map[string]any{condStringEquals: map[string]any{condPrincipalARN: arn}}
		anyone := map[string]any{principalAWS: "*"}

		out = append(out, policyStatement{
			Sid: fmt.Sprintf("DenyTenantRole%dOutsideItsPrefix", k+1), Effect: "Deny", Principal: anyone,
			Action: "s3:*", NotResource: resources, Condition: role,
		})

		if singleProject {
			continue
		}

		out = append(out, policyStatement{
			Sid: fmt.Sprintf("DenyTenantRole%dListOutsideItsPrefix", k+1), Effect: "Deny", Principal: anyone,
			Action: actListBucket, Resource: bucket, Condition: map[string]any{
				condStringEquals: map[string]any{condPrincipalARN: arn},
				"StringNotLike":  map[string]any{"s3:prefix": prefixes},
			},
		})
	}

	return out
}

// globalDenyStatements are the denies that bind every principal outside the
// backup account, on any object of the bucket.
func globalDenyStatements(bucket, backupAccountID string) []policyStatement {
	external := map[string]any{
		"StringNotEquals": map[string]string{"aws:PrincipalAccount": backupAccountID},
	}
	anyone := map[string]any{principalAWS: "*"}

	return []policyStatement{
		{
			Sid: "DenyDeleteObjectsFromExternalAccount", Effect: "Deny", Principal: anyone,
			Action:   []string{"s3:DeleteObject", "s3:DeleteObjectVersion", "s3:BypassGovernanceRetention"},
			Resource: bucket + "/*", Condition: external,
		},
		{
			Sid: "DenyBucketConfigurationFromExternalAccount", Effect: "Deny", Principal: anyone,
			Action: []string{
				"s3:PutLifecycleConfiguration", "s3:PutBucketPolicy", "s3:DeleteBucketPolicy",
				"s3:PutBucketVersioning", "s3:PutReplicationConfiguration", "s3:PutBucketObjectLockConfiguration",
			},
			Resource: bucket, Condition: external,
		},
	}
}

// sseS3Deny refuses any upload that does not state SSE-S3. A request with no
// header has no value for the key, which StringNotEquals counts as a
// mismatch: a client must send the header (barman-cloud: encryption AES256).
func sseS3Deny(bucket string) policyStatement {
	return policyStatement{
		Sid: "DenyPutObjectWithoutSSES3", Effect: "Deny", Principal: map[string]any{principalAWS: "*"},
		Action: "s3:PutObject", Resource: bucket + "/*",
		Condition: map[string]any{
			"StringNotEquals": map[string]string{"s3:x-amz-server-side-encryption": "AES256"},
		},
	}
}

// TenantRolePolicyArgs is what the identity policy of a tenant role needs.
type TenantRolePolicyArgs struct {
	// BucketARN is the ARN of the backup bucket.
	BucketARN string
	// Encryption is the bucket's mode; empty is EncryptionKMS.
	Encryption Encryption
	// KMSKeyARN is the bucket's key. Required in KMS mode, ignored in SSE-S3.
	KMSKeyARN string
	// Tenants are every tenant of ONE role (usually one).
	Tenants []Tenant
	// SingleProject lists the whole bucket instead of the tenant's prefix;
	// it must equal Args.SingleProject.
	SingleProject bool
}

// TenantRolePolicy renders the identity policy a tenant role carries in the
// source account, as JSON, for the caller to attach (a Pulumi RolePolicy, or
// an ACK Role's inline policy). It never grants delete of any kind.
//
//   - archive: s3:PutObject (with the AES256 condition in SSE-S3 mode),
//     s3:AbortMultipartUpload, s3:ListMultipartUploadParts, s3:GetObject and
//     s3:PutObjectTagging on its prefix; s3:ListBucket limited to the prefix;
//   - restore: s3:GetObject on its prefix and s3:ListBucket limited to it;
//   - in KMS mode only, the key (encrypt and decrypt for archive, decrypt
//     for restore).
func TenantRolePolicy(a TenantRolePolicyArgs) (string, error) {
	var errs []error

	if a.BucketARN == "" {
		errs = append(errs, errors.New("tenant role policy: BucketARN is empty"))
	}

	if len(a.Tenants) == 0 {
		errs = append(errs, errors.New("tenant role policy: no tenants"))
	}

	if !a.Encryption.valid() {
		errs = append(errs, fmt.Errorf("tenant role policy: unknown Encryption %q", a.Encryption))
	}

	if a.Encryption.kms() && a.KMSKeyARN == "" {
		errs = append(errs, errors.New("tenant role policy: KMSKeyARN is empty in KMS mode"))
	}

	for i, t := range a.Tenants {
		errs = append(errs, t.validate(i)...)

		if t.RoleARN != a.Tenants[0].RoleARN {
			errs = append(errs, fmt.Errorf("tenant role policy: Tenants[%d] is another role than Tenants[0]", i))
		}
	}

	if err := errors.Join(errs...); err != nil {
		return "", err
	}

	var (
		statements []map[string]any
		archives   bool
	)

	for i, t := range a.Tenants {
		n := fmt.Sprintf("%d%s", i+1, titled(t.Purpose))
		res := prefixResource(a.BucketARN, t.Prefix)

		if t.Purpose == PurposeArchive {
			archives = true
			put := map[string]any{
				polSid: "Put" + n, polEffect: polAllow, polAction: []string{"s3:PutObject"}, polResource: res,
			}
			if !a.Encryption.kms() {
				put[polCondition] = map[string]any{
					condStringEquals: map[string]any{"s3:x-amz-server-side-encryption": "AES256"},
				}
			}

			statements = append(statements, put, map[string]any{
				polSid: "Object" + n, polEffect: polAllow, polResource: res,
				polAction: []string{"s3:AbortMultipartUpload", "s3:ListMultipartUploadParts", "s3:GetObject", "s3:PutObjectTagging"},
			})
		} else {
			statements = append(statements, map[string]any{
				polSid: "Object" + n, polEffect: polAllow, polAction: []string{"s3:GetObject"}, polResource: res,
			})
		}

		list := map[string]any{
			polSid: "List" + n, polEffect: polAllow, polAction: []string{actListBucket}, polResource: a.BucketARN,
		}
		if !a.SingleProject {
			list[polCondition] = map[string]any{"StringLike": map[string]any{"s3:prefix": []string{t.Prefix + "*"}}}
		}

		statements = append(statements, list)
	}

	if a.Encryption.kms() {
		actions := []string{actKMSDecrypt, "kms:DescribeKey"}
		if archives {
			actions = []string{"kms:Encrypt", actKMSDecrypt, "kms:ReEncrypt*", "kms:GenerateDataKey*", "kms:DescribeKey"}
		}

		statements = append(statements, map[string]any{
			polSid: "UseBackupKey", polEffect: polAllow, polAction: actions, polResource: a.KMSKeyARN,
		})
	}

	raw, err := marshal(map[string]any{polVersion: polDocDate, polStatement: statements}, "")
	if err != nil {
		return "", fmt.Errorf("render tenant role policy: %w", err)
	}

	return raw, nil
}

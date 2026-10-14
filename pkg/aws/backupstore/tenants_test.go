package backupstore_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/truvity/cnpg/v2/pkg/aws/backupstore"
)

const (
	bucketPolicyType = "aws:s3/bucketPolicy:BucketPolicy"
	lifecycleType    = "aws:s3/bucketLifecycleConfigurationV2:BucketLifecycleConfigurationV2"
	lockType         = "aws:s3/bucketObjectLockConfigurationV2:BucketObjectLockConfigurationV2"
	replicaType      = "aws:s3/bucketV2:BucketV2"
)

func role(name string) string { return fmt.Sprintf("arn:%s:iam::%s:role/%s", partition, src, name) }

func archiveTenant() backupstore.Tenant {
	return backupstore.Tenant{
		RoleARN: role("pg-archive"),
		Prefix:  "<namespace-a>/<cluster-a>/",
		Purpose: backupstore.PurposeArchive,
		SessionTags: &backupstore.SessionTags{
			ClusterName: "<eks-cluster>", Namespace: "<namespace-a>", ServiceAccount: "<cluster-a>",
		},
	}
}

func restoreTenant() backupstore.Tenant {
	return backupstore.Tenant{
		RoleARN: role("pg-restore"),
		Prefix:  "<namespace-b>/<cluster-b>/",
		Purpose: backupstore.PurposeRestore,
	}
}

func tenantArgs() *backupstore.Args {
	a := base()
	a.WriterRoleName = ""
	a.KeyDescription = ""
	a.LegacyTopLevel = false
	a.Encryption = backupstore.EncryptionSSES3
	a.LifecycleRules = nil
	a.Lifecycle = &backupstore.Lifecycle{BaseBackupDays: 30}
	a.Tenants = []backupstore.Tenant{archiveTenant(), restoreTenant()}
	a.ListerRoleNames = nil
	a.Replica = &backupstore.Replica{
		BucketName: replica, RoleName: bucket + "-replication", ObjectLockGovernance: true,
	}

	return a
}

func indent(t *testing.T, raw string) []byte {
	t.Helper()

	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(raw), "", "  "); err != nil {
		t.Fatal(err)
	}

	return append(buf.Bytes(), '\n')
}

func TestTenantGoldenPolicies(t *testing.T) {
	regs, err := run(t, tenantArgs())
	if err != nil {
		t.Fatal(err)
	}

	primary, _ := policyOf(t, regs, bucketPolicyType, bucket+"-policy")
	checkGolden(t, "tenants/primary-policy.json", []byte(primary+"\n"))

	replicaPolicy, _ := policyOf(t, regs, bucketPolicyType, replica+"-policy")
	checkGolden(t, "tenants/replica-policy.json", []byte(replicaPolicy+"\n"))

	docs, err := tenantArgs().TenantRolePolicies("")
	if err != nil {
		t.Fatal(err)
	}

	for name, arn := range map[string]string{"archive": role("pg-archive"), "restore": role("pg-restore")} {
		checkGolden(t, "tenants/role-policy-"+name+".json", indent(t, docs[arn]))
	}

	checkGolden(t, "tenants/lifecycle-primary.json", dump(t, []registration{find(t, regs, lifecycleType, bucket+"-lifecycle")}))
	checkGolden(t, "tenants/lifecycle-replica.json", dump(t, []registration{find(t, regs, lifecycleType, replica+"-lifecycle")}))
}

type statement struct {
	Sid         string
	Effect      string
	Action      any
	Resource    any
	NotResource any
	Condition   map[string]map[string]any
}

func statementsOf(t *testing.T, raw string) []statement {
	t.Helper()

	var doc struct{ Statement []statement }
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}

	return doc.Statement
}

func actionsOf(s statement) []string {
	switch v := s.Action.(type) {
	case string:
		return []string{v}
	case []any:
		var out []string
		for _, a := range v {
			out = append(out, a.(string))
		}

		return out
	}

	return nil
}

func TestTenantStatementsNeverGrantDelete(t *testing.T) {
	regs, err := run(t, tenantArgs())
	if err != nil {
		t.Fatal(err)
	}

	primary, _ := policyOf(t, regs, bucketPolicyType, bucket+"-policy")
	docs, err := tenantArgs().TenantRolePolicies("")
	if err != nil {
		t.Fatal(err)
	}

	check := func(where string, st []statement) {
		allows := 0

		for _, s := range st {
			if s.Effect != "Allow" {
				continue
			}

			allows++

			for _, a := range actionsOf(s) {
				if strings.Contains(a, "Delete") || strings.HasSuffix(a, "*") || strings.Contains(a, "Lifecycle") ||
					strings.Contains(a, "BucketPolicy") || strings.Contains(a, "Versioning") || strings.Contains(a, "Bypass") {
					t.Errorf("%s: %s allows %s", where, s.Sid, a)
				}
			}
		}

		if allows == 0 {
			t.Errorf("%s: no allow statements", where)
		}
	}

	check("bucket policy", statementsOf(t, primary))

	for arn, doc := range docs {
		check(arn, statementsOf(t, doc))
	}
}

func TestTenantBucketPolicyShape(t *testing.T) {
	regs, err := run(t, tenantArgs())
	if err != nil {
		t.Fatal(err)
	}

	raw, _ := policyOf(t, regs, bucketPolicyType, bucket+"-policy")
	st := statementsOf(t, raw)

	byID := map[string]statement{}
	for _, s := range st {
		byID[s.Sid] = s
	}

	for _, sid := range []string{
		"AllowTenant1ArchiveObjects", "AllowTenant1ArchiveListBucket", "AllowTenant2RestoreObjects", "AllowTenant2RestoreListBucket",
		"DenyTenantRole1OutsideItsPrefix", "DenyTenantRole1ListOutsideItsPrefix", "DenyTenantRole2OutsideItsPrefix",
		"DenyDeleteObjectsFromExternalAccount", "DenyBucketConfigurationFromExternalAccount", "DenyPutObjectWithoutSSES3",
		"DenyDeleteObjectVersionFromExternalAccount",
	} {
		if _, ok := byID[sid]; !ok {
			t.Errorf("statement %s missing", sid)
		}
	}

	if _, ok := byID["AllowSourceAccountObjectAccess"]; ok {
		t.Error("the writer glob is still admitted next to tenants")
	}

	// Session tags condition the archive allow, and only it.
	tags := byID["AllowTenant1ArchiveObjects"].Condition["StringEquals"]
	for k, v := range map[string]string{
		"aws:PrincipalTag/eks-cluster-name":           "<eks-cluster>",
		"aws:PrincipalTag/kubernetes-namespace":       "<namespace-a>",
		"aws:PrincipalTag/kubernetes-service-account": "<cluster-a>",
		"aws:PrincipalArn":                            role("pg-archive"),
	} {
		if tags[k] != v {
			t.Errorf("archive allow condition %s = %v, want %s", k, tags[k], v)
		}
	}

	if len(byID["AllowTenant2RestoreObjects"].Condition["StringEquals"]) != 1 {
		t.Error("the restore tenant has no tags, yet its condition holds more than the role ARN")
	}

	// A strict list: a prefix, no IfExists, no unprefixed head.
	list := byID["AllowTenant1ArchiveListBucket"]
	if _, ok := list.Condition["StringLike"]["s3:prefix"]; !ok || strings.Contains(raw, "IfExists") {
		t.Errorf("list is not strictly prefix-limited: %v", list.Condition)
	}

	if strings.Contains(raw, "HeadBucket") || strings.Contains(raw, `"Null"`) {
		t.Error("an unprefixed list is admitted without AllowHeadBucket")
	}

	// The deny outside the prefix keeps the bucket and the prefix.
	deny := byID["DenyTenantRole1OutsideItsPrefix"]
	if got := fmt.Sprint(deny.NotResource); !strings.Contains(got, "<namespace-a>/<cluster-a>/*") || !strings.Contains(got, bucket+" ") && !strings.Contains(got, bucket+"]") {
		t.Errorf("deny NotResource = %v", got)
	}

	// The SSE deny.
	if sse := byID["DenyPutObjectWithoutSSES3"]; fmt.Sprint(sse.Condition) != "map[StringNotEquals:map[s3:x-amz-server-side-encryption:AES256]]" {
		t.Errorf("SSE deny condition = %v", sse.Condition)
	}
}

func TestAllowHeadBucketIsOptIn(t *testing.T) {
	a := tenantArgs()
	a.Tenants[0].AllowHeadBucket = true

	regs, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	raw, _ := policyOf(t, regs, bucketPolicyType, bucket+"-policy")
	for _, w := range []string{"AllowTenant1ArchiveHeadBucket", `"s3:prefix": "true"`, `"s3:prefix": "false"`} {
		if !strings.Contains(raw, w) {
			t.Errorf("policy lacks %s", w)
		}
	}

	docs, err := a.TenantRolePolicies("")
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(docs[role("pg-archive")], `"Null":{"s3:prefix":"true"}`) {
		t.Error("role policy lacks the unprefixed list")
	}
}

func TestSSES3ModeCreatesNoKey(t *testing.T) {
	regs, err := run(t, tenantArgs())
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range regs {
		if strings.HasPrefix(r.typ, "aws:kms/") {
			t.Errorf("SSE-S3 mode registered %s %s", r.typ, r.name)
		}
	}

	enc := find(t, regs, "aws:s3/bucketServerSideEncryptionConfigurationV2:BucketServerSideEncryptionConfigurationV2", bucket+"-encryption")
	got := fmt.Sprint(enc.inputs["rules"])

	if !strings.Contains(got, "AES256") || strings.Contains(got, "kms") || strings.Contains(got, "bucketKeyEnabled") {
		t.Errorf("encryption rule = %s", got)
	}

	repl := find(t, regs, "aws:iam/rolePolicy:RolePolicy", bucket+"-replication-policy").inputs["policy"].StringValue()
	if strings.Contains(repl, "kms:") {
		t.Errorf("replication policy has KMS statements: %s", repl)
	}

	cfg := fmt.Sprint(find(t, regs, "aws:s3/bucketReplicationConfig:BucketReplicationConfig", bucket+"-replication").inputs)
	if strings.Contains(cfg, "kms") || strings.Contains(cfg, "Kms") {
		t.Errorf("replication config names a key: %s", cfg)
	}

	for arn, doc := range mustPolicies(t, tenantArgs()) {
		if strings.Contains(doc, "kms:") {
			t.Errorf("%s: role policy carries KMS statements", arn)
		}
	}
}

func mustPolicies(t *testing.T, a *backupstore.Args) map[string]string {
	t.Helper()

	m, err := a.TenantRolePolicies("")
	if err != nil {
		t.Fatal(err)
	}

	return m
}

func TestRolePoliciesByPurpose(t *testing.T) {
	docs := mustPolicies(t, tenantArgs())

	archive := docs[role("pg-archive")]
	for _, w := range []string{
		"s3:PutObject", "s3:AbortMultipartUpload", "s3:ListMultipartUploadParts", "s3:GetObject",
		`"s3:x-amz-server-side-encryption":"AES256"`, `"s3:prefix":["<namespace-a>/<cluster-a>/*"]`,
	} {
		if !strings.Contains(archive, w) {
			t.Errorf("archive policy lacks %s", w)
		}
	}

	restore := docs[role("pg-restore")]
	if strings.Contains(restore, "s3:PutObject") || strings.Contains(restore, "Multipart") || !strings.Contains(restore, "s3:GetObject") {
		t.Errorf("restore policy = %s", restore)
	}
}

func TestKMSModeTenants(t *testing.T) {
	a := tenantArgs()
	a.Encryption = backupstore.EncryptionKMS
	a.KeyDescription = "backups"
	a.Replica.KeyDescription = "replica"

	regs, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	key := find(t, regs, "aws:kms/key:Key", bucket+"-kms").inputs["policy"].StringValue()
	for _, w := range []string{"TenantArchiveRolesUseKey", "TenantRestoreRolesDecrypt", role("pg-archive"), role("pg-restore")} {
		if !strings.Contains(key, w) {
			t.Errorf("key policy lacks %s", w)
		}
	}

	if strings.Contains(key, "WorkloadBackupRolesUseKey") {
		t.Error("the writer glob reaches the key next to tenants")
	}

	raw, _ := policyOf(t, regs, bucketPolicyType, bucket+"-policy")
	if strings.Contains(raw, "DenyPutObjectWithoutSSES3") {
		t.Error("the SSE-S3 upload deny is in a KMS-mode policy")
	}

	doc, err := backupstore.TenantRolePolicy(backupstore.TenantRolePolicyArgs{
		BucketARN: "<bucket-arn>", KMSKeyARN: "<key-arn>", Tenants: []backupstore.Tenant{archiveTenant()},
	})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(doc, "kms:GenerateDataKey*") || strings.Contains(doc, "x-amz-server-side-encryption") {
		t.Errorf("KMS role policy = %s", doc)
	}
}

func TestOneRoleTwoTenantsDenyKeepsBothPrefixes(t *testing.T) {
	a := tenantArgs()
	a.Tenants = []backupstore.Tenant{
		archiveTenant(),
		{RoleARN: role("pg-archive"), Prefix: "<namespace-b>/<cluster-b>/", Purpose: backupstore.PurposeRestore},
	}

	regs, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	raw, _ := policyOf(t, regs, bucketPolicyType, bucket+"-policy")
	if strings.Contains(raw, "DenyTenantRole2") {
		t.Error("one role got two deny pairs")
	}

	for _, s := range statementsOf(t, raw) {
		if s.Sid == "DenyTenantRole1OutsideItsPrefix" {
			if got := fmt.Sprint(s.NotResource); !strings.Contains(got, "<cluster-a>/*") || !strings.Contains(got, "<cluster-b>/*") {
				t.Errorf("deny NotResource = %v", got)
			}
		}
	}

	if got := mustPolicies(t, a); len(got) != 1 || strings.Count(got[role("pg-archive")], "s3:GetObject") != 2 {
		t.Errorf("role policies = %v", got)
	}
}

func TestGlobalDeniesAreOptInWithoutTenants(t *testing.T) {
	regs, err := run(t, base())
	if err != nil {
		t.Fatal(err)
	}

	raw, _ := policyOf(t, regs, bucketPolicyType, bucket+"-policy")
	if strings.Contains(raw, "DenyBucketConfiguration") {
		t.Error("global denies on by default")
	}

	a := base()
	a.DenyDestructive = true

	regs, err = run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	raw, _ = policyOf(t, regs, bucketPolicyType, bucket+"-policy")

	for _, w := range []string{
		"s3:DeleteObject\"", "s3:DeleteObjectVersion", "s3:PutLifecycleConfiguration", "s3:PutBucketPolicy",
		"s3:DeleteBucketPolicy", "s3:PutBucketVersioning", "s3:PutReplicationConfiguration",
	} {
		if !strings.Contains(raw, w) {
			t.Errorf("global denies lack %s", w)
		}
	}
}

func lifecycleRules(t *testing.T, regs []registration, name string) map[string]map[string]any {
	t.Helper()

	out := map[string]map[string]any{}

	for _, r := range find(t, regs, lifecycleType, name).inputs["rules"].ArrayValue() {
		m := r.Mappable().(map[string]any)
		out[m["id"].(string)] = m
	}

	return out
}

func ids(m map[string]map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}

	sort.Strings(out)

	return out
}

func TestLifecycleRulesPerTenantAndMargin(t *testing.T) {
	a := tenantArgs()
	a.Lifecycle = &backupstore.Lifecycle{
		BaseBackupDays: 30,
		Transitions:    []backupstore.Transition{{Days: 30, StorageClass: "STANDARD_IA"}},
	}

	regs, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	rules := lifecycleRules(t, regs, bucket+"-lifecycle")
	want := []string{
		"base-backups/<namespace-a>/<cluster-a>/<cluster-a>/", "housekeeping", "tier-storage-class",
		"wal/<namespace-a>/<cluster-a>/<cluster-a>/",
	}

	if got := ids(rules); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("rule ids = %q, want %q", got, want)
	}

	days := func(id string) float64 { return rules[id]["expiration"].(map[string]any)["days"].(float64) }

	if d := days(want[0]); d != 30 {
		t.Errorf("base backups expire after %v days, want 30", d)
	}

	if d := days(want[3]); d != 37 {
		t.Errorf("WAL expires after %v days, want 37 (N+7)", d)
	}

	and := rules[want[0]]["filter"].(map[string]any)["and"].(map[string]any)
	if and["prefix"] != "<namespace-a>/<cluster-a>/<cluster-a>/base/" || fmt.Sprint(and["tags"]) != "map[cnpg-backup-retention:managed]" {
		t.Errorf("base rule filter = %v", and)
	}

	if p := rules[want[3]]["filter"].(map[string]any)["and"].(map[string]any)["prefix"]; p != "<namespace-a>/<cluster-a>/<cluster-a>/wals/" {
		t.Errorf("WAL rule prefix = %v", p)
	}

	hk := rules["housekeeping"]
	if hk["noncurrentVersionExpiration"].(map[string]any)["noncurrentDays"].(float64) != 7 ||
		hk["abortIncompleteMultipartUpload"].(map[string]any)["daysAfterInitiation"].(float64) != 7 ||
		hk["expiration"].(map[string]any)["expiredObjectDeleteMarker"] != true {
		t.Errorf("housekeeping = %v", hk)
	}

	// The replica has the same expiry and housekeeping, and no tiering.
	rep := lifecycleRules(t, regs, replica+"-lifecycle")
	if got := ids(rep); strings.Contains(strings.Join(got, "|"), "tier-storage-class") || len(got) != 3 {
		t.Errorf("replica rule ids = %q", got)
	}
}

func TestLifecycleParametersAndFallback(t *testing.T) {
	a := tenantArgs()
	a.Tenants = nil
	a.WriterRoleName = "writer-*"
	a.Lifecycle = &backupstore.Lifecycle{BaseBackupDays: 14, WALMarginDays: 3, NoncurrentDays: 2, AbortMultipartDays: 1}
	a.Replica = nil

	regs, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	rules := lifecycleRules(t, regs, bucket+"-lifecycle")
	if got := ids(rules); strings.Join(got, "|") != "housekeeping|managed" {
		t.Fatalf("rule ids = %q", got)
	}

	if d := rules["managed"]["expiration"].(map[string]any)["days"].(float64); d != 17 {
		t.Errorf("the bucket-wide tag rule expires after %v days, want N+margin = 17", d)
	}

	if tag := fmt.Sprint(rules["managed"]["filter"].(map[string]any)["tag"]); tag != "map[key:cnpg-backup-retention value:managed]" {
		t.Errorf("tag filter = %s", tag)
	}

	hk := rules["housekeeping"]
	if hk["noncurrentVersionExpiration"].(map[string]any)["noncurrentDays"].(float64) != 2 ||
		hk["abortIncompleteMultipartUpload"].(map[string]any)["daysAfterInitiation"].(float64) != 1 {
		t.Errorf("housekeeping = %v", hk)
	}
}

func TestLifecycleRulesAddToCallerRules(t *testing.T) {
	a := base()
	a.Lifecycle = &backupstore.Lifecycle{BaseBackupDays: 10}

	regs, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	if got := ids(lifecycleRules(t, regs, bucket+"-lifecycle")); strings.Join(got, "|") != "housekeeping|managed|tiering" {
		t.Errorf("rule ids = %q", got)
	}
}

func TestReplicaObjectLockGovernance(t *testing.T) {
	regs, err := run(t, tenantArgs())
	if err != nil {
		t.Fatal(err)
	}

	if v := find(t, regs, replicaType, replica).inputs["objectLockEnabled"]; !v.IsBool() || !v.BoolValue() {
		t.Error("replica not created with object lock")
	}

	if v := find(t, regs, "aws:s3/bucket:Bucket", bucket).inputs["objectLockEnabled"]; v.HasValue() && v.IsBool() && v.BoolValue() {
		t.Error("the primary got object lock from the replica option")
	}

	cfg := fmt.Sprint(find(t, regs, lockType, replica+"-object-lock").inputs["rule"])
	if !strings.Contains(cfg, "GOVERNANCE") || !strings.Contains(cfg, "30") {
		t.Errorf("default retention = %s", cfg)
	}

	for _, r := range regs {
		if r.typ == lockType && r.name == bucket+"-object-lock" {
			t.Error("the primary has an object lock configuration")
		}
	}

	raw, _ := policyOf(t, regs, bucketPolicyType, replica+"-policy")
	if !strings.Contains(raw, "DenyDeleteObjectsFromExternalAccount") || !strings.Contains(raw, "DenyBucketConfigurationFromExternalAccount") {
		t.Error("replica policy lacks the global denies")
	}

	// An explicit retention wins over the lifecycle.
	a := tenantArgs()
	a.Replica.ObjectLockDays = 45

	regs, err = run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	if cfg := fmt.Sprint(find(t, regs, lockType, replica+"-object-lock").inputs["rule"]); !strings.Contains(cfg, "45") {
		t.Errorf("default retention = %s", cfg)
	}
}

func TestReplicaWithoutGovernanceStaysAsBefore(t *testing.T) {
	a := tenantArgs()
	a.Replica.ObjectLockGovernance = false

	regs, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	// Tenants imply the global denies on the replica too, but no lock and no
	// lifecycle are created behind the caller's back.
	for _, r := range regs {
		if r.name == replica+"-object-lock" || r.name == replica+"-lifecycle" {
			t.Errorf("unexpected %s", r.name)
		}
	}

	if v := find(t, regs, replicaType, replica).inputs["objectLockEnabled"]; v.HasValue() && v.IsBool() && v.BoolValue() {
		t.Error("object lock enabled without the option")
	}
}

func TestRefusesTenantAndLifecycleMistakes(t *testing.T) {
	cases := map[string]struct {
		mutate func(*backupstore.Args)
		want   string
	}{
		"unknown encryption": {func(a *backupstore.Args) { a.Encryption = "rot13" }, "Encryption"},
		"writer and tenants": {func(a *backupstore.Args) { a.WriterRoleName = "w" }, "Tenants replace the writer"},
		"role glob":          {func(a *backupstore.Args) { a.Tenants[0].RoleARN = role("pg-*") }, "Tenants[0].RoleARN"},
		"role not an arn":    {func(a *backupstore.Args) { a.Tenants[1].RoleARN = "pg" }, "Tenants[1].RoleARN"},
		"prefix no slash":    {func(a *backupstore.Args) { a.Tenants[0].Prefix = "a/b" }, "Tenants[0].Prefix"},
		"prefix glob":        {func(a *backupstore.Args) { a.Tenants[0].Prefix = "a/*/" }, "Tenants[0].Prefix"},
		"prefix leading":     {func(a *backupstore.Args) { a.Tenants[0].Prefix = "/a/b/" }, "Tenants[0].Prefix"},
		"purpose":            {func(a *backupstore.Args) { a.Tenants[0].Purpose = "delete" }, "Tenants[0].Purpose"},
		"server name":        {func(a *backupstore.Args) { a.Tenants[0].ServerName = "a/b" }, "Tenants[0].ServerName"},
		"duplicate":          {func(a *backupstore.Args) { a.Tenants[1] = a.Tenants[0] }, "repeats"},
		"two archivers": {
			func(a *backupstore.Args) {
				a.Tenants[1] = a.Tenants[0]
				a.Tenants[1].RoleARN = role("other")
			}, "second archive tenant",
		},
		"no retention":      {func(a *backupstore.Args) { a.Lifecycle.BaseBackupDays = 0 }, "BaseBackupDays"},
		"negative margin":   {func(a *backupstore.Args) { a.Lifecycle.WALMarginDays = -1 }, "WALMarginDays"},
		"no rules at all":   {func(a *backupstore.Args) { a.Lifecycle = nil }, "LifecycleRules is empty"},
		"lock without days": {func(a *backupstore.Args) { a.Lifecycle = &backupstore.Lifecycle{BaseBackupDays: 0} }, "BaseBackupDays"},
		"lock days unknown": {
			func(a *backupstore.Args) {
				a.Lifecycle = nil
				a.LifecycleRules = backupstore.Backstop(backupstore.BackstopArgs{NoncurrentDays: 7})
			}, "Replica.ObjectLockGovernance needs",
		},
		"compliance and governance": {func(a *backupstore.Args) { a.ObjectLockDays = 30 }, "exclusive"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			a := tenantArgs()
			tc.mutate(a)

			err := a.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v, want it to mention %q", err, tc.want)
			}
		})
	}

	t.Run("a valid tenant model passes", func(t *testing.T) {
		if _, err := run(t, tenantArgs()); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("SSE-S3 needs no key text", func(t *testing.T) {
		a := tenantArgs()
		a.KeyDescription = ""
		a.Replica.KeyDescription = ""

		if _, err := run(t, a); err != nil {
			t.Fatal(err)
		}
	})
}

func TestTenantRolePolicyRefuses(t *testing.T) {
	_, err := backupstore.TenantRolePolicy(backupstore.TenantRolePolicyArgs{
		Tenants: []backupstore.Tenant{archiveTenant(), restoreTenant()},
	})
	if err == nil {
		t.Fatal("two roles, no bucket and no key accepted")
	}

	for _, w := range []string{"BucketARN", "another role", "KMSKeyARN"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error lacks %s: %v", w, err)
		}
	}
}

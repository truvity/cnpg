package backupstore_test

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	pulumiaws "github.com/pulumi/pulumi-aws/sdk/v7/go/aws"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/truvity/cnpg/v2/pkg/aws/backupstore"
)

const (
	bucket  = "example-backup"
	replica = "example-backup-replica"
	src     = "src-account"
	dst     = "dst-account"
	// partition is not the default one, so no expectation below can be met
	// by a hard-coded one.
	partition = "testpart"
)

type alias struct {
	typ      string
	noParent bool
}

type registration struct {
	typ, name, parent, provider string
	aliases                     []alias
	protect, retain             bool
	inputs                      resource.PropertyMap
}

type recorder struct {
	mu   sync.Mutex
	regs []registration
}

func (r *recorder) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	reg := registration{typ: args.TypeToken, name: args.Name, inputs: args.Inputs}

	if rpc := args.RegisterRPC; rpc != nil {
		reg.parent = rpc.GetParent()
		reg.provider = rpc.GetProvider()
		reg.protect = rpc.GetProtect()
		reg.retain = rpc.GetRetainOnDelete()

		for _, a := range rpc.GetAliases() {
			if spec := a.GetSpec(); spec != nil && spec.GetName() == "" {
				reg.aliases = append(reg.aliases, alias{typ: spec.GetType(), noParent: spec.GetNoParent()})
			}
		}
	}

	r.mu.Lock()
	r.regs = append(r.regs, reg)
	r.mu.Unlock()

	return args.Name + "-id", args.Inputs.Copy(), nil
}

func (*recorder) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return args.Args, nil
}

func ptr[T any](v T) *T { return &v }

func base() *backupstore.Args {
	return &backupstore.Args{
		BucketName:      bucket,
		SourceAccountID: src,
		BackupAccountID: dst,
		Partition:       partition,
		WriterRoleName:  "writer-*",
		KeyDescription:  "backups " + bucket,
		Tags:            map[string]string{"Owner": "someone"},
		LifecycleRules:  backupstore.Backstop(backupstore.BackstopArgs{ExpireDays: 30, NoncurrentDays: 7}),
		LegacyTopLevel:  true,
	}
}

func full() *backupstore.Args {
	a := base()
	a.ReaderRoleName = "reader"
	a.ListerRoleNames = []string{"lister-one", "lister_two"}
	a.ObjectLockDays = 30
	a.Replica = &backupstore.Replica{
		BucketName:     replica,
		KeyDescription: "replica " + replica,
		RoleName:       bucket + "-replication",
	}

	return a
}

func run(t *testing.T, args *backupstore.Args) ([]registration, error) {
	t.Helper()

	rec := &recorder{}

	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		p, err := pulumiaws.NewProvider(ctx, "primary", &pulumiaws.ProviderArgs{})
		if err != nil {
			return err
		}

		if args.Replica != nil {
			rp, err := pulumiaws.NewProvider(ctx, "replica", &pulumiaws.ProviderArgs{})
			if err != nil {
				return err
			}

			args.Replica.Provider = rp
		}

		_, err = backupstore.New(ctx, "backup", args, pulumi.Providers(p))

		return err
	}, pulumi.WithMocks("example", "dev", rec))

	return rec.regs, err
}

func children(regs []registration) []registration {
	var out []registration

	for _, r := range regs {
		if r.typ != backupstore.TypeToken && !strings.HasPrefix(r.typ, "pulumi:providers:") {
			out = append(out, r)
		}
	}

	return out
}

func find(t *testing.T, regs []registration, typ, name string) registration {
	t.Helper()

	for _, r := range regs {
		if r.typ == typ && r.name == name {
			return r
		}
	}

	t.Fatalf("no %s %s registered", typ, name)

	return registration{}
}

func TestChildrenKeepTheirNames(t *testing.T) {
	regs, err := run(t, full())
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, r := range children(regs) {
		got = append(got, r.typ+" "+r.name)
	}

	sort.Strings(got)

	want := []string{
		"aws:iam/role:Role " + bucket + "-replication-role",
		"aws:iam/rolePolicy:RolePolicy " + bucket + "-replication-policy",
		"aws:kms/alias:Alias " + bucket + "-kms-alias",
		"aws:kms/key:Key " + bucket + "-kms",
		"aws:kms/key:Key " + replica + "-kms",
		"aws:s3/bucket:Bucket " + bucket,
		"aws:s3/bucketLifecycleConfigurationV2:BucketLifecycleConfigurationV2 " + bucket + "-lifecycle",
		"aws:s3/bucketObjectLockConfigurationV2:BucketObjectLockConfigurationV2 " + bucket + "-object-lock",
		"aws:s3/bucketPolicy:BucketPolicy " + bucket + "-policy",
		"aws:s3/bucketPolicy:BucketPolicy " + replica + "-policy",
		"aws:s3/bucketPublicAccessBlock:BucketPublicAccessBlock " + bucket + "-public-access",
		"aws:s3/bucketPublicAccessBlock:BucketPublicAccessBlock " + replica + "-public-access",
		"aws:s3/bucketReplicationConfig:BucketReplicationConfig " + bucket + "-replication",
		"aws:s3/bucketServerSideEncryptionConfigurationV2:BucketServerSideEncryptionConfigurationV2 " + bucket + "-encryption",
		"aws:s3/bucketV2:BucketV2 " + replica,
		"aws:s3/bucketVersioningV2:BucketVersioningV2 " + replica + "-versioning",
		"aws:s3/bucketVersioningV2:BucketVersioningV2 " + bucket + "-versioning",
	}
	sort.Strings(want)

	if !slices.Equal(got, want) {
		t.Fatalf("children differ.\n got: %q\nwant: %q", got, want)
	}
}

func TestMinimalHasNoReplicaNoLockNoReplicaPolicy(t *testing.T) {
	regs, err := run(t, base())
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range children(regs) {
		for _, banned := range []string{"replica", "replication", "object-lock"} {
			if strings.Contains(r.name, banned) {
				t.Errorf("%s %s registered without a replica or a lock", r.typ, r.name)
			}
		}
	}

	if n := len(children(regs)); n != 8 {
		t.Errorf("want 8 children (key, alias, bucket, versioning, encryption, lifecycle, public access, policy), got %d", n)
	}

	b := find(t, regs, "aws:s3/bucket:Bucket", bucket)
	if v := b.inputs["objectLockEnabled"]; v.IsBool() && v.BoolValue() {
		t.Error("object lock enabled without ObjectLockDays")
	}
}

func TestEveryChildIsUnderTheComponent(t *testing.T) {
	regs, err := run(t, full())
	if err != nil {
		t.Fatal(err)
	}

	want := "::" + backupstore.TypeToken + "::backup"

	for _, r := range children(regs) {
		if !strings.Contains(r.parent, want) {
			t.Errorf("%s %s: parent %q is not the component", r.typ, r.name, r.parent)
		}
	}
}

// The AWS SDK declares aliases of its own on some of these types, from the
// type they had before the v2 split, without a parent. Under a component
// that resolves beneath the NEW parent, so a state still holding the former
// type at the top level would not be found. Whatever the SDK aliases, the
// legacy set must repeat it with no parent. This is the test that goes red
// when an SDK upgrade adds one.
func TestLegacyAliasSetCoversEverythingTheSDKAliases(t *testing.T) {
	regs, err := run(t, full())
	if err != nil {
		t.Fatal(err)
	}

	sdkAliased := 0

	for _, r := range children(regs) {
		var plain, sdk, legacyTyped []string

		for _, a := range r.aliases {
			switch {
			case a.noParent && a.typ == "":
				plain = append(plain, "noParent")
			case a.noParent:
				legacyTyped = append(legacyTyped, a.typ)
			default:
				sdk = append(sdk, a.typ)
			}
		}

		if len(plain) != 1 {
			t.Errorf("%s %s: want exactly one plain NoParent alias, got %d", r.typ, r.name, len(plain))
		}

		for _, typ := range sdk {
			sdkAliased++

			if !slices.Contains(legacyTyped, typ) {
				t.Errorf("%s %s: the SDK aliases from %s but no NoParent alias does", r.typ, r.name, typ)
			}
		}

		// One legacy type alias per distinct former type, never a duplicate.
		seen := map[string]bool{}
		for _, typ := range legacyTyped {
			if seen[typ] {
				t.Errorf("%s %s: duplicate NoParent alias from %s", r.typ, r.name, typ)
			}

			seen[typ] = true

			if !slices.Contains(sdk, typ) {
				t.Errorf("%s %s: NoParent alias from %s that the SDK does not declare", r.typ, r.name, typ)
			}
		}
	}

	if sdkAliased == 0 {
		t.Fatal("the mock saw no SDK aliases at all; this test is not testing anything")
	}

	b := find(t, regs, "aws:s3/bucket:Bucket", bucket)

	var former []string

	for _, a := range b.aliases {
		if a.noParent && a.typ != "" {
			former = append(former, a.typ)
		}
	}

	if !slices.Equal(former, []string{"aws:s3/bucketV2:BucketV2"}) {
		t.Errorf("bucket legacy type aliases = %q, want the one BucketV2 alias", former)
	}
}

func TestWithoutLegacyTopLevelNoChildCarriesANoParentAlias(t *testing.T) {
	a := full()
	a.LegacyTopLevel = false

	regs, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range children(regs) {
		for _, al := range r.aliases {
			if al.noParent {
				t.Errorf("%s %s carries a NoParent alias without LegacyTopLevel", r.typ, r.name)
			}
		}
	}
}

func TestProtectsBucketsAndKeysByDefault(t *testing.T) {
	regs, err := run(t, full())
	if err != nil {
		t.Fatal(err)
	}

	protected := map[string]bool{
		"aws:s3/bucket:Bucket " + bucket:      true,
		"aws:s3/bucketV2:BucketV2 " + replica: true,
		"aws:kms/key:Key " + bucket + "-kms":  true,
		"aws:kms/key:Key " + replica + "-kms": true,
	}

	for _, r := range children(regs) {
		if want := protected[r.typ+" "+r.name]; r.protect != want {
			t.Errorf("%s %s: protect = %v, want %v", r.typ, r.name, r.protect, want)
		}
	}
}

func TestProtectCanBeSwitchedOff(t *testing.T) {
	a := full()
	a.Protect = ptr(false)

	regs, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range children(regs) {
		if r.protect {
			t.Errorf("%s %s protected with Protect=false", r.typ, r.name)
		}
	}
}

func TestKeysAreRetainedOnDelete(t *testing.T) {
	regs, err := run(t, full())
	if err != nil {
		t.Fatal(err)
	}

	keys := 0

	for _, r := range children(regs) {
		if r.typ == "aws:kms/key:Key" {
			keys++

			if !r.retain {
				t.Errorf("key %s is not retained on delete", r.name)
			}
		} else if r.retain {
			t.Errorf("%s %s is retained on delete; only keys are", r.typ, r.name)
		}
	}

	if keys != 2 {
		t.Errorf("want 2 keys, got %d", keys)
	}
}

func TestReplicaChildrenUseTheReplicaProvider(t *testing.T) {
	regs, err := run(t, full())
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range children(regs) {
		isReplica := r.name == replica || strings.HasPrefix(r.name, replica+"-")
		onReplicaProvider := strings.Contains(r.provider, "::replica::")

		if isReplica != onReplicaProvider {
			t.Errorf("%s %s: provider %q", r.typ, r.name, r.provider)
		}
	}
}

func TestObjectLockIsCompliance(t *testing.T) {
	regs, err := run(t, full())
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{bucket, replica} {
		typ := "aws:s3/bucket:Bucket"
		if name == replica {
			typ = "aws:s3/bucketV2:BucketV2"
		}

		if v := find(t, regs, typ, name).inputs["objectLockEnabled"]; !v.IsBool() || !v.BoolValue() {
			t.Errorf("%s: object lock not enabled", name)
		}
	}

	cfg := find(t, regs, "aws:s3/bucketObjectLockConfigurationV2:BucketObjectLockConfigurationV2", bucket+"-object-lock")
	got := fmt.Sprint(cfg.inputs["rule"].ObjectValue()["defaultRetention"].ObjectValue())
	if !strings.Contains(got, "COMPLIANCE") || !strings.Contains(got, "30") {
		t.Errorf("default retention = %s", got)
	}
}

func policyOf(t *testing.T, regs []registration, typ, name string) (string, []map[string]any) {
	t.Helper()

	raw := find(t, regs, typ, name).inputs["policy"].StringValue()

	var doc struct{ Statement []map[string]any }
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("%s: %v", name, err)
	}

	return raw, doc.Statement
}

func sids(st []map[string]any) []string {
	var out []string
	for _, s := range st {
		out = append(out, s["Sid"].(string))
	}

	return out
}

func TestBucketPolicy(t *testing.T) {
	regs, err := run(t, full())
	if err != nil {
		t.Fatal(err)
	}

	raw, st := policyOf(t, regs, "aws:s3/bucketPolicy:BucketPolicy", bucket+"-policy")

	want := []string{
		"AllowSourceAccountObjectAccess", "AllowSourceAccountListBucket", "DenyDeleteObjectVersionFromExternalAccount",
		"AllowReaderGetObject", "AllowReaderListBucket", "AllowListerListBucketListerOne", "AllowListerListBucketListerTwo",
	}
	if !slices.Equal(sids(st), want) {
		t.Fatalf("statements = %q, want %q", sids(st), want)
	}

	writer := fmt.Sprintf("arn:%s:iam::%s:role/writer-*", partition, src)
	if !strings.Contains(raw, writer) {
		t.Errorf("writer glob %q missing from policy", writer)
	}

	if !strings.Contains(raw, `"aws:PrincipalAccount": "`+dst+`"`) {
		t.Error("the delete-version deny does not name the backup account")
	}

	if !strings.Contains(raw, fmt.Sprintf("arn:%s:s3:::%s/*", partition, bucket)) {
		t.Error("partition not used in the bucket ARN")
	}

	actions := st[0]["Action"].([]any)
	if len(actions) != 5 || !slices.Contains(actions, any("s3:DeleteObject")) {
		t.Errorf("default writer actions = %v", actions)
	}
}

func TestDefaultPartitionAndWriterActionOverride(t *testing.T) {
	a := base()
	a.Partition = ""
	a.WriterObjectActions = []string{"s3:PutObject", "s3:GetObject"}

	regs, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	raw, st := policyOf(t, regs, "aws:s3/bucketPolicy:BucketPolicy", bucket+"-policy")

	if !strings.Contains(raw, fmt.Sprintf("arn:%s:s3:::%s", backupstore.DefaultPartition, bucket)) {
		t.Error("default partition not applied")
	}

	if actions := st[0]["Action"].([]any); len(actions) != 2 || strings.Contains(raw, "s3:DeleteObject\"") {
		t.Errorf("append-only writer actions = %v", actions)
	}
}

func TestKeyPolicyAdmitsOnlyTheNamedRoles(t *testing.T) {
	regs, err := run(t, full())
	if err != nil {
		t.Fatal(err)
	}

	raw, st := policyOf(t, regs, "aws:kms/key:Key", bucket+"-kms")

	if want := []string{"BackupAccountKeyAdmin", "WorkloadBackupRolesUseKey", "WorkloadReaderRoleDecrypts"}; !slices.Equal(sids(st), want) {
		t.Fatalf("key statements = %q, want %q", sids(st), want)
	}

	if strings.Contains(raw, "lister") {
		t.Error("a lister role is admitted to the key; listers may not decrypt")
	}
}

func TestReplicaPolicyDeniesDeleteAndAdmitsListersOnly(t *testing.T) {
	regs, err := run(t, full())
	if err != nil {
		t.Fatal(err)
	}

	raw, st := policyOf(t, regs, "aws:s3/bucketPolicy:BucketPolicy", replica+"-policy")

	want := []string{"DenyDeleteObjectVersionFromExternalAccount", "AllowListerListBucketListerOne", "AllowListerListBucketListerTwo"}
	if !slices.Equal(sids(st), want) {
		t.Fatalf("replica statements = %q, want %q", sids(st), want)
	}

	if strings.Contains(raw, "GetObject") {
		t.Error("the replica policy grants an object read")
	}
}

func TestNoReplicaPolicyWithoutListers(t *testing.T) {
	a := full()
	a.ListerRoleNames = nil

	regs, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range children(regs) {
		if r.typ == "aws:s3/bucketPolicy:BucketPolicy" && r.name == replica+"-policy" {
			t.Error("replica policy registered with no lister")
		}
	}
}

func TestReplicationRoleBoundaryIsAnInput(t *testing.T) {
	a := full()
	a.Replica.RolePermissionsBoundary = pulumi.String("boundary-arn")

	regs, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	role := find(t, regs, "aws:iam/role:Role", bucket+"-replication-role")
	if got := role.inputs["permissionsBoundary"].StringValue(); got != "boundary-arn" {
		t.Errorf("permissionsBoundary = %q", got)
	}

	if got := role.inputs["name"].StringValue(); got != bucket+"-replication" {
		t.Errorf("role name = %q", got)
	}

	regs, err = run(t, full())
	if err != nil {
		t.Fatal(err)
	}

	if v := find(t, regs, "aws:iam/role:Role", bucket+"-replication-role").inputs["permissionsBoundary"]; v.HasValue() && !v.IsNull() {
		t.Errorf("a boundary was set without being asked: %v", v)
	}
}

func TestBackstop(t *testing.T) {
	regs, err := run(t, func() *backupstore.Args {
		a := base()
		a.LifecycleRules = backupstore.Backstop(backupstore.BackstopArgs{
			ExpireDays: 180, NoncurrentDays: 180,
			Transitions: []backupstore.Transition{{Days: 30, StorageClass: "STANDARD_IA"}},
		})

		return a
	}())
	if err != nil {
		t.Fatal(err)
	}

	cfg := find(t, regs, "aws:s3/bucketLifecycleConfigurationV2:BucketLifecycleConfigurationV2", bucket+"-lifecycle")

	rules := cfg.inputs["rules"].ArrayValue()
	if len(rules) != 1 {
		t.Fatalf("rules = %d", len(rules))
	}

	rule := rules[0].ObjectValue()
	if rule["id"].StringValue() != "tiering" || len(rule["transitions"].ArrayValue()) != 1 || !rule["expiration"].IsObject() {
		t.Errorf("rule = %v", rule)
	}

	noExpire := backupstore.Backstop(backupstore.BackstopArgs{NoncurrentDays: 7})
	if len(noExpire) != 1 {
		t.Fatal("want one rule")
	}
}

func TestRefuses(t *testing.T) {
	cases := map[string]struct {
		mutate func(*backupstore.Args)
		want   string
	}{
		"no bucket name":  {func(a *backupstore.Args) { a.BucketName = "" }, "BucketName is empty"},
		"no source":       {func(a *backupstore.Args) { a.SourceAccountID = "" }, "SourceAccountID is empty"},
		"no backup":       {func(a *backupstore.Args) { a.BackupAccountID = "" }, "BackupAccountID is empty"},
		"no writer":       {func(a *backupstore.Args) { a.WriterRoleName = "" }, "WriterRoleName is empty"},
		"no key text":     {func(a *backupstore.Args) { a.KeyDescription = "" }, "KeyDescription is empty"},
		"no lifecycle":    {func(a *backupstore.Args) { a.LifecycleRules = nil }, "LifecycleRules is empty"},
		"negative lock":   {func(a *backupstore.Args) { a.ObjectLockDays = -1 }, "ObjectLockDays"},
		"reader glob":     {func(a *backupstore.Args) { a.ReaderRoleName = "read-*" }, "ReaderRoleName"},
		"lister glob":     {func(a *backupstore.Args) { a.ListerRoleNames = []string{"ok", "x?"} }, "ListerRoleNames[1]"},
		"empty lister":    {func(a *backupstore.Args) { a.ListerRoleNames = []string{""} }, "ListerRoleNames[0] is empty"},
		"empty actions":   {func(a *backupstore.Args) { a.WriterObjectActions = []string{} }, "WriterObjectActions is empty"},
		"replica no name": {func(a *backupstore.Args) { a.Replica = &backupstore.Replica{} }, "Replica.BucketName is empty"},
		"replica no role": {func(a *backupstore.Args) { a.Replica = &backupstore.Replica{BucketName: "r"} }, "Replica.RoleName is empty"},
		"replica no prov": {func(a *backupstore.Args) { a.Replica = &backupstore.Replica{BucketName: "r"} }, "Replica.Provider is nil"},
		"replica = primary": {
			func(a *backupstore.Args) { a.Replica = &backupstore.Replica{BucketName: bucket} },
			"equals BucketName",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			a := base()
			tc.mutate(a)

			err := a.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v, want it to mention %q", err, tc.want)
			}
		})
	}

	t.Run("reports every problem at once", func(t *testing.T) {
		err := (&backupstore.Args{}).Validate()
		if err == nil {
			t.Fatal("empty args accepted")
		}

		for _, w := range []string{"BucketName", "SourceAccountID", "BackupAccountID", "WriterRoleName", "KeyDescription", "LifecycleRules"} {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("error does not mention %s: %v", w, err)
			}
		}
	})

	t.Run("a refused argument registers nothing", func(t *testing.T) {
		a := base()
		a.WriterRoleName = ""

		regs, err := run(t, a)
		if err == nil {
			t.Fatal("want an error")
		}

		if len(children(regs)) != 0 {
			t.Errorf("registered %d children before refusing", len(children(regs)))
		}
	})

	t.Run("nil args", func(t *testing.T) {
		err := pulumi.RunErr(func(ctx *pulumi.Context) error {
			_, err := backupstore.New(ctx, "x", nil)
			return err
		}, pulumi.WithMocks("example", "dev", &recorder{}))
		if err == nil {
			t.Fatal("nil args accepted")
		}
	})
}

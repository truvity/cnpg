package backupstore_test

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	pulumiaws "github.com/pulumi/pulumi-aws/sdk/v7/go/aws"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/truvity/cnpg/v2/pkg/aws/backupstore"
)

func bucketSetArgs() *backupstore.ProjectBucketsArgs {
	return &backupstore.ProjectBucketsArgs{
		BucketPrefix:       "acme-pg",
		Env:                "staging",
		Region:             "eu-west-1",
		SessionClusterName: "staging",
		SourceAccountID:    src,
		BackupAccountID:    dst,
		Partition:          partition,
		Lifecycle:          backupstore.Lifecycle{RetentionDays: 30},
		Tags: func(project string) map[string]string {
			return map[string]string{"Scope": "pitr." + project}
		},
		Projects: []backupstore.Project{
			{Name: "billing", Clusters: []backupstore.ProjectCluster{{Name: "billing-infra-pg", Namespace: "billing"}}},
			{Name: "ledger", Clusters: []backupstore.ProjectCluster{
				{Name: "ledger-pg", Namespace: "ledger"},
				{Name: "audit-pg", Namespace: "ledger", ServiceAccount: "audit"},
			}},
		},
	}
}

func TestBuildProjectBucketsShape(t *testing.T) {
	built, err := backupstore.BuildProjectBuckets(bucketSetArgs())
	if err != nil {
		t.Fatal(err)
	}

	if len(built) != 2 {
		t.Fatalf("got %d buckets", len(built))
	}

	b := built[0].Args

	if b.BucketName != "acme-pg-billing-staging-eu-west-1" {
		t.Errorf("bucket name %q", b.BucketName)
	}

	if !b.SingleProject || !b.DenyDestructive || b.Encryption != backupstore.EncryptionSSES3 || b.Replica != nil {
		t.Errorf("not the standard shape: %+v", b)
	}

	if b.Lifecycle == nil || b.Lifecycle.RetentionDays != 30 || b.Tags["Scope"] != "pitr.billing" {
		t.Errorf("lifecycle or tags: %+v %v", b.Lifecycle, b.Tags)
	}

	if len(b.Tenants) != 2 {
		t.Fatalf("tenants: %+v", b.Tenants)
	}

	arch, rest := b.Tenants[0], b.Tenants[1]

	if arch.RoleARN != "arn:"+partition+":iam::"+src+":role/staging-pg-billing-infra-pg-archive" ||
		arch.Prefix != "billing/billing-infra-pg/" || arch.Purpose != backupstore.PurposeArchive {
		t.Errorf("archive tenant: %+v", arch)
	}

	if st := arch.SessionTags; st == nil || st.ClusterName != "staging" || st.Namespace != "billing" || st.ServiceAccount != "billing-infra-pg" {
		t.Errorf("archive session tags: %+v", st)
	}

	if rest.RoleARN != "arn:"+partition+":iam::"+src+":role/staging-pg-billing-infra-pg-restore" ||
		rest.Purpose != backupstore.PurposeRestore || rest.SessionTags != nil {
		t.Errorf("restore tenant: %+v", rest)
	}

	// Several clusters in one bucket; an explicit ServiceAccount wins.
	l := built[1].Args
	if len(l.Tenants) != 4 || l.Tenants[2].SessionTags.ServiceAccount != "audit" {
		t.Errorf("ledger tenants: %+v", l.Tenants)
	}
}

func TestBuildProjectBucketsRefusals(t *testing.T) {
	a := bucketSetArgs()
	a.BucketPrefix, a.Env, a.SessionClusterName = "", "", ""
	a.Projects = append(a.Projects, backupstore.Project{Name: "billing"}, backupstore.Project{Name: strings.Repeat("x", 50), Clusters: a.Projects[0].Clusters})

	_, err := backupstore.BuildProjectBuckets(a)
	if err == nil {
		t.Fatal("want an error")
	}

	for _, s := range []string{"BucketPrefix", "Env", "SessionClusterName", "listed twice", "no cluster"} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error does not mention %q: %v", s, err)
		}
	}

	a = bucketSetArgs()
	a.Lifecycle = backupstore.Lifecycle{}

	if _, err := backupstore.BuildProjectBuckets(a); err == nil {
		t.Error("a zero retention was accepted")
	}

	a = bucketSetArgs()
	a.Projects[0].Name = strings.Repeat("x", 50)

	if _, err := backupstore.BuildProjectBuckets(a); err == nil || !strings.Contains(err.Error(), "allows 63") {
		t.Errorf("a too long name: %v", err)
	}
}

// urns lists the component of every project and its parent: the identity a
// caller's Pulumi state holds.
func runProjects(t *testing.T, a *backupstore.ProjectBucketsArgs) []registration {
	t.Helper()

	rec := &recorder{}

	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		p, err := pulumiaws.NewProvider(ctx, "primary", &pulumiaws.ProviderArgs{})
		if err != nil {
			return err
		}

		a.ResourceOptions = append(a.ResourceOptions, pulumi.Providers(p))

		_, err = backupstore.NewProjectBuckets(ctx, a)

		return err
	}, pulumi.WithMocks("example", "dev", rec))
	if err != nil {
		t.Fatal(err)
	}

	return rec.regs
}

func dumpURNs(t *testing.T, regs []registration) []byte {
	t.Helper()

	type entry struct {
		Type    string   `json:"type"`
		Name    string   `json:"name"`
		Parent  string   `json:"parent,omitempty"`
		Aliases []string `json:"aliasTypes,omitempty"`
	}

	var out []entry

	for _, r := range regs {
		if strings.HasPrefix(r.typ, "pulumi:providers:") {
			continue
		}

		e := entry{Type: r.typ, Name: r.name, Parent: r.parent}
		for _, al := range r.aliases {
			e.Aliases = append(e.Aliases, al.typ)
		}

		out = append(out, e)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}

		return out[i].Name < out[j].Name
	})

	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	return append(raw, '\n')
}

// TestProjectBucketsKeepURNs pins the Pulumi identity of the default
// registration: a component named after the bucket, directly under the stack,
// as New(ctx, bucketName, args, pulumi.Providers(p)) makes it. A caller that
// registered its buckets that way keeps every URN; the golden lists the
// children as well.
func TestProjectBucketsKeepURNs(t *testing.T) {
	regs := runProjects(t, bucketSetArgs())

	comps := map[string]string{}

	for _, r := range regs {
		if r.typ == backupstore.TypeToken {
			comps[r.name] = r.parent
		}
	}

	for _, want := range []string{"acme-pg-billing-staging-eu-west-1", "acme-pg-ledger-staging-eu-west-1"} {
		parent, ok := comps[want]
		if !ok || !strings.Contains(parent, "::pulumi:pulumi:Stack::") {
			t.Errorf("component %q: registered=%v parent=%q (want a component named after the bucket, directly under the stack)", want, ok, parent)
		}
	}

	checkGolden(t, "projects/urns.json", dumpURNs(t, regs))
}

// TestProjectBucketsNameAndOptionHooks: a caller whose state has other names
// or parents passes them, never a rename of the library.
func TestProjectBucketsNameAndOptionHooks(t *testing.T) {
	a := bucketSetArgs()
	a.ResourceName = func(bucket string) string { return "pg-" + bucket }
	a.ProjectOptions = func(p backupstore.Project, bucket string) []pulumi.ResourceOption {
		return []pulumi.ResourceOption{pulumi.Aliases([]pulumi.Alias{{Name: pulumi.String(bucket)}})}
	}

	comps := map[string]registration{}

	for _, r := range runProjects(t, a) {
		if r.typ == backupstore.TypeToken {
			comps[r.name] = r
		}
	}

	if _, ok := comps["pg-acme-pg-billing-staging-eu-west-1"]; !ok || len(comps) != 2 {
		t.Errorf("components: %v", comps)
	}
}

// The registrations equal, one for one, those of a caller that builds the
// Args itself and calls New per bucket with the bucket name: the way the
// buckets were made before this builder existed.
func TestProjectBucketsEqualPerBucketNew(t *testing.T) {
	want := func() []registration {
		built, err := backupstore.BuildProjectBuckets(bucketSetArgs())
		if err != nil {
			t.Fatal(err)
		}

		rec := &recorder{}

		err = pulumi.RunErr(func(ctx *pulumi.Context) error {
			p, err := pulumiaws.NewProvider(ctx, "primary", &pulumiaws.ProviderArgs{})
			if err != nil {
				return err
			}

			for _, b := range built {
				if _, err := backupstore.New(ctx, b.Args.BucketName, b.Args, pulumi.Providers(p)); err != nil {
					return err
				}
			}

			return nil
		}, pulumi.WithMocks("example", "dev", rec))
		if err != nil {
			t.Fatal(err)
		}

		return rec.regs
	}()

	got := runProjects(t, bucketSetArgs())

	if g, w := string(dumpURNs(t, got)), string(dumpURNs(t, want)); g != w {
		t.Errorf("URNs differ from per-bucket New:\n%s\nwant\n%s", g, w)
	}

	if g, w := string(dump(t, got)), string(dump(t, want)); g != w {
		t.Error("child inputs differ from per-bucket New")
	}
}

func runProjectsReplica(t *testing.T, a *backupstore.ProjectBucketsArgs) []registration {
	t.Helper()

	rec := &recorder{}

	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		p, err := pulumiaws.NewProvider(ctx, "primary", &pulumiaws.ProviderArgs{})
		if err != nil {
			return err
		}

		rp, err := pulumiaws.NewProvider(ctx, "replica", &pulumiaws.ProviderArgs{})
		if err != nil {
			return err
		}

		a.ResourceOptions = append(a.ResourceOptions, pulumi.Providers(p))
		a.Replica.Provider = rp

		_, err = backupstore.NewProjectBuckets(ctx, a)

		return err
	}, pulumi.WithMocks("example", "dev", rec))
	if err != nil {
		t.Fatal(err)
	}

	return rec.regs
}

func replicaSetArgs() *backupstore.ProjectBucketsArgs {
	a := bucketSetArgs()
	a.Replica = &backupstore.ProjectReplica{Region: "eu-north-1", ObjectLockGovernance: true}

	return a
}

func TestBuildProjectBucketsReplica(t *testing.T) {
	a := replicaSetArgs()
	a.Replica.Provider = nil

	if _, err := backupstore.BuildProjectBuckets(a); err == nil || !strings.Contains(err.Error(), "Provider") {
		t.Errorf("a replica without a provider: %v", err)
	}

	a = replicaSetArgs()
	a.Replica.Region = a.Region

	if _, err := backupstore.BuildProjectBuckets(a); err == nil || !strings.Contains(err.Error(), "equals Region") {
		t.Errorf("a replica in the primary region: %v", err)
	}
}

// A replica is the existing Args.Replica per project: derived names, the
// governance lock, retention from the lifecycle, and an untouched default.
func TestProjectBucketsReplicaMapping(t *testing.T) {
	regs := runProjectsReplica(t, replicaSetArgs())

	var lock *registration

	for i, r := range regs {
		if r.name == "acme-pg-billing-staging-eu-north-1-object-lock" {
			lock = &regs[i]
		}
	}

	if lock == nil {
		t.Fatal("no replica object lock registered")
	}

	raw, err := json.Marshal(lock.inputs.Mappable())
	if err != nil {
		t.Fatal(err)
	}

	for _, s := range []string{"GOVERNANCE", "30"} {
		if !strings.Contains(string(raw), s) {
			t.Errorf("object lock inputs lack %s: %s", s, raw)
		}
	}

	names := map[string]bool{}
	for _, r := range regs {
		names[r.name] = true
	}

	for _, n := range []string{
		"acme-pg-billing-staging-eu-north-1",
		"acme-pg-ledger-staging-eu-north-1-lifecycle",
		"acme-pg-ledger-staging-eu-north-1-policy",
		"acme-pg-billing-staging-eu-west-1-replication",
		"acme-pg-billing-staging-eu-west-1-replication-role",
	} {
		if !names[n] {
			t.Errorf("resource %s not registered", n)
		}
	}

	checkGolden(t, "projects/urns-replica.json", dumpURNs(t, regs))
}

// extraRoleArgs gives the first cluster the dotted names beside its defaults.
func extraRoleArgs() *backupstore.ProjectBucketsArgs {
	a := bucketSetArgs()
	a.Projects[0].Clusters[0].ExtraArchiveRoleNames = []string{"staging-acme-billing-eu.pg-archive"}
	a.Projects[0].Clusters[0].ExtraRestoreRoleNames = []string{"staging-acme-billing-eu.pg-restore"}

	return a
}

// TestExtraRoleNamesAreAdmittedBesideTheDefaults: the extra names become
// tenants of the same prefix, after both defaults of the cluster; the archive
// one carries the same session tags as the default; nothing else moves.
func TestExtraRoleNamesAreAdmittedBesideTheDefaults(t *testing.T) {
	plain, err := backupstore.BuildProjectBuckets(bucketSetArgs())
	if err != nil {
		t.Fatal(err)
	}

	built, err := backupstore.BuildProjectBuckets(extraRoleArgs())
	if err != nil {
		t.Fatal(err)
	}

	tn := built[0].Args.Tenants
	if len(tn) != 4 {
		t.Fatalf("tenants: %+v", tn)
	}

	// The first two are the defaults, as before.
	if tn[0] != plain[0].Args.Tenants[0] && *tn[0].SessionTags != *plain[0].Args.Tenants[0].SessionTags {
		t.Errorf("default archive tenant moved: %+v", tn[0])
	}

	if tn[1] != plain[0].Args.Tenants[1] {
		t.Errorf("default restore tenant moved: %+v", tn[1])
	}

	arch, rest := tn[2], tn[3]
	pre := "arn:" + partition + ":iam::" + src + ":role/"

	if arch.RoleARN != pre+"staging-acme-billing-eu.pg-archive" || arch.Prefix != "billing/billing-infra-pg/" ||
		arch.Purpose != backupstore.PurposeArchive || arch.SessionTags == nil || *arch.SessionTags != *tn[0].SessionTags {
		t.Errorf("extra archive tenant: %+v", arch)
	}

	if rest.RoleARN != pre+"staging-acme-billing-eu.pg-restore" || rest.Prefix != "billing/billing-infra-pg/" ||
		rest.Purpose != backupstore.PurposeRestore || rest.SessionTags != nil {
		t.Errorf("extra restore tenant: %+v", rest)
	}

	// The other project is untouched.
	if len(built[1].Args.Tenants) != len(plain[1].Args.Tenants) {
		t.Errorf("another project changed: %+v", built[1].Args.Tenants)
	}
}

// TestUnsetExtraRoleNamesChangeNothing: nil and empty lists build exactly the
// tenants of a caller that never had the fields.
func TestUnsetExtraRoleNamesChangeNothing(t *testing.T) {
	a := bucketSetArgs()
	a.Projects[0].Clusters[0].ExtraArchiveRoleNames = []string{}
	a.Projects[0].Clusters[0].ExtraRestoreRoleNames = nil

	got, err := backupstore.BuildProjectBuckets(a)
	if err != nil {
		t.Fatal(err)
	}

	want, err := backupstore.BuildProjectBuckets(bucketSetArgs())
	if err != nil {
		t.Fatal(err)
	}

	for i := range want {
		g, w := dumpTenants(got[i].Args.Tenants), dumpTenants(want[i].Args.Tenants)
		if g != w {
			t.Errorf("project %d tenants differ:\n%s\nwant\n%s", i, g, w)
		}
	}
}

func dumpTenants(tn []backupstore.Tenant) string {
	raw, _ := json.Marshal(tn)

	return string(raw)
}

// TestExtraRoleNamesReachBucketAndKeyPolicy: both the default and the extra
// ARNs are principals of the allows and keep their own deny outside the
// prefix, and in KMS mode both reach the key policy.
func TestExtraRoleNamesReachBucketAndKeyPolicy(t *testing.T) {
	a := extraRoleArgs()
	a.Encryption = backupstore.EncryptionKMS
	a.KeyDescription = func(project, bucket string) string { return "backups of " + project }

	regs := runProjects(t, a)
	bucketName := "acme-pg-billing-staging-eu-west-1"

	raw, _ := policyOf(t, regs, bucketPolicyType, bucketName+"-policy")
	key := find(t, regs, "aws:kms/key:Key", bucketName+"-kms").inputs["policy"].StringValue()
	pre := "arn:" + partition + ":iam::" + src + ":role/"

	for _, n := range []string{
		"staging-pg-billing-infra-pg-archive", "staging-pg-billing-infra-pg-restore",
		"staging-acme-billing-eu.pg-archive", "staging-acme-billing-eu.pg-restore",
	} {
		if !strings.Contains(raw, pre+n) {
			t.Errorf("bucket policy lacks %s", n)
		}

		if !strings.Contains(key, pre+n) {
			t.Errorf("key policy lacks %s", n)
		}
	}

	byID := map[string]statement{}
	for _, s := range statementsOf(t, raw) {
		byID[s.Sid] = s
	}

	for _, sid := range []string{
		"AllowTenant3ArchiveObjects", "AllowTenant4RestoreObjects",
		"DenyTenantRole3OutsideItsPrefix", "DenyTenantRole4OutsideItsPrefix",
	} {
		if _, ok := byID[sid]; !ok {
			t.Errorf("statement %s missing", sid)
		}
	}

	tags := byID["AllowTenant3ArchiveObjects"].Condition["StringEquals"]
	if tags["aws:PrincipalTag/kubernetes-service-account"] != "billing-infra-pg" || tags["aws:PrincipalArn"] != pre+"staging-acme-billing-eu.pg-archive" {
		t.Errorf("extra archive allow condition: %v", tags)
	}
}

func TestExtraRoleNamesRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(*backupstore.ProjectCluster)
		want   string
	}{
		"empty":    {func(c *backupstore.ProjectCluster) { c.ExtraArchiveRoleNames = []string{""} }, "empty name"},
		"glob":     {func(c *backupstore.ProjectCluster) { c.ExtraRestoreRoleNames = []string{"staging-*"} }, "exact role name"},
		"arn":      {func(c *backupstore.ProjectCluster) { c.ExtraArchiveRoleNames = []string{"arn:aws:iam::1:role/x"} }, "exact role name"},
		"too long": {func(c *backupstore.ProjectCluster) { c.ExtraArchiveRoleNames = []string{strings.Repeat("r", 65)} }, "IAM allows 64"},
		"the default": {func(c *backupstore.ProjectCluster) {
			c.ExtraArchiveRoleNames = []string{"staging-pg-billing-infra-pg-archive"}
		}, "repeats a role"},
		"repeated": {func(c *backupstore.ProjectCluster) {
			c.ExtraArchiveRoleNames = []string{"a.pg-archive", "a.pg-archive"}
		}, "repeats a role"},
		"archive+restore": {func(c *backupstore.ProjectCluster) {
			c.ExtraArchiveRoleNames = []string{"a.pg"}
			c.ExtraRestoreRoleNames = []string{"a.pg"}
		}, "repeats a role"},
	} {
		a := bucketSetArgs()
		tc.mutate(&a.Projects[0].Clusters[0])

		if _, err := backupstore.BuildProjectBuckets(a); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %v, want one containing %q", name, err, tc.want)
		}
	}
}

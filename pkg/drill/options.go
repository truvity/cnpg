// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

package drill

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// The defaults of Options (and of the cnpgctl drill flags).
const (
	DefaultTimeout             = 2 * time.Hour
	DefaultTeardownTimeout     = 10 * time.Minute
	DefaultMaxRecoveryPointAge = 24 * time.Hour
	DefaultMaxRecoveryAttempts = 2
	DefaultPollInterval        = 10 * time.Second
	DefaultStatementTimeout    = 15 * time.Minute
	DefaultPGPort              = 5432

	// DrillSuffix is appended to the source cluster name to form the drill
	// Cluster's name, which is also the name of the restore ServiceAccount
	// (charts/cnpg-backup-access: restore.serviceAccount, <cluster>-restore).
	DrillSuffix = "-restore"

	// MaxClusterName is CloudNativePG's limit on a Cluster's name.
	MaxClusterName = 50

	// CountExact counts every row of every user table (a full read of the
	// data); CountEstimate sums the planner's reltuples instead.
	CountExact    = "exact"
	CountEstimate = "estimate"
)

// staticCredentialEnv are the environment variables that would give the
// barman-cloud sidecar credentials of their own instead of the pod's ambient
// (Pod Identity) identity.
var staticCredentialEnv = map[string]bool{
	"AWS_ACCESS_KEY_ID":           true,
	"AWS_SECRET_ACCESS_KEY":       true,
	"AWS_SESSION_TOKEN":           true,
	"AWS_PROFILE":                 true,
	"AWS_SHARED_CREDENTIALS_FILE": true,
	"AWS_CONFIG_FILE":             true,
}

// Options describe one drill. Namespace and Cluster are required; everything
// else has a default or is derived from the source Cluster.
type Options struct {
	// Namespace and Cluster name the SOURCE Cluster: the one whose archive is
	// drilled. It is only read.
	Namespace string
	Cluster   string

	// DrillNamespace is where the drill Cluster, its ObjectStore and the
	// result ConfigMap are created (default: Namespace).
	DrillNamespace string
	// DrillName is the drill Cluster's name (default <Cluster>-restore). It
	// must equal the restore ServiceAccount's name: CloudNativePG runs the
	// Cluster's pods as the ServiceAccount named after the Cluster.
	DrillName string

	// The archive coordinates. Each one left empty is taken from the source
	// Cluster's WAL-archiving plugin entry and its ObjectStore.
	DestinationPath string // s3://<bucket>/<prefix>
	ServerName      string // the barman server name (with any generation suffix)
	EndpointURL     string

	// Image is the PostgreSQL image (default: the source's status.image).
	Image string
	// StorageClass of the drill volumes (default: the source's). It must
	// delete its volumes: a Retain class is refused unless AllowRetainedVolumes.
	StorageClass         string
	AllowRetainedVolumes bool
	// StorageSize and WALStorageSize (default: the larger of the source's spec
	// and its PersistentVolumeClaims' capacity). WALStorageSize applies only
	// when the source has a separate WAL volume.
	StorageSize    string
	WALStorageSize string
	// Resources of the drill instance (default DefaultResources()).
	Resources *corev1.ResourceRequirements
	// NodeSelector and Tolerations of the drill pods. Both nil: copied from
	// the source's affinity. Either set: used as given, the other empty.
	NodeSelector map[string]string
	Tolerations  []corev1.Toleration
	// PodAnnotations go on every pod and volume of the drill Cluster
	// (inheritedMetadata), e.g. a node autoscaler's do-not-disrupt marker.
	PodAnnotations map[string]string
	// SidecarEnv is extra environment for the barman-cloud sidecar (the
	// ObjectStore's instanceSidecarConfiguration.env), e.g. AWS_REGION or a
	// proxy. Static credentials are refused unless AllowStaticCredentials:
	// the drill must read the archive with the restore identity only. That
	// switch exists for S3-compatible test endpoints and nothing else.
	SidecarEnv             []corev1.EnvVar
	AllowStaticCredentials bool

	// Databases expected in the restored instance (default: derived from the
	// source: its Database objects, bootstrap.initdb.database and
	// bootstrap.recovery.database; if none, every non-template database but
	// postgres, of which there must be at least one).
	Databases []string
	// MinRows: "<db>" is the minimum of rows over all user tables of a
	// database (default 1 for each expected one); "<db>/<schema>.<table>"
	// the minimum for one table.
	MinRows map[string]int64
	// CountMode is CountExact (default) or CountEstimate.
	CountMode string
	// MaxRecoveryPointAge is how old the recovered data may be: now minus the
	// later of the last replayed transaction and the base backup's start.
	MaxRecoveryPointAge time.Duration
	// StatementTimeout bounds each SQL statement (a row count on a big table).
	StatementTimeout time.Duration

	// Timeout bounds the drill from start to the end of the checks (default
	// 2h); the teardown has TeardownTimeout of its own after that.
	Timeout         time.Duration
	TeardownTimeout time.Duration
	// MaxRecoveryAttempts is how many failed recovery Job pods end the drill
	// early (the Job itself retries six times, with back-off).
	MaxRecoveryAttempts int
	PollInterval        time.Duration

	// PGHost and PGPort override where the checks connect (default
	// <DrillName>-rw.<DrillNamespace>.svc:5432). The server certificate is
	// verified against the drill's CA for <DrillName>-rw.<DrillNamespace>.svc
	// either way.
	PGHost string
	PGPort int

	// NoRecord skips writing the result ConfigMap.
	NoRecord bool

	// Now is the clock (default time.Now).
	Now func() time.Time

	// mutate, when set (tests), changes the built objects before the safety
	// check, to prove the check refuses what it must.
	mutate func(cluster, store *unstructured.Unstructured)
}

// DefaultResources is the drill instance's default: small, one instance.
func DefaultResources() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    mustQuantity("250m"),
			corev1.ResourceMemory: mustQuantity("512Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceMemory: mustQuantity("1Gi"),
		},
	}
}

// ResultName is the name of the result ConfigMap of a drill.
func ResultName(drillName string) string { return drillName + "-result" }

var dnsLabel = regexp.MustCompile(`^[a-z]([-a-z0-9]*[a-z0-9])?$`)

// withDefaults fills the defaults and checks what can be checked offline.
func (o Options) withDefaults() (Options, error) {
	if o.Namespace == "" || o.Cluster == "" {
		return o, errors.New("the source namespace and cluster are required")
	}
	if o.DrillNamespace == "" {
		o.DrillNamespace = o.Namespace
	}
	if o.DrillName == "" {
		o.DrillName = o.Cluster + DrillSuffix
	}
	if !dnsLabel.MatchString(o.DrillName) || len(o.DrillName) > MaxClusterName {
		return o, fmt.Errorf("drill name %q must be a DNS label of at most %d characters (a CloudNativePG Cluster name)", o.DrillName, MaxClusterName)
	}
	if o.DrillNamespace == o.Namespace && o.DrillName == o.Cluster {
		return o, fmt.Errorf("drill name %q is the source cluster itself", o.DrillName)
	}
	if o.DestinationPath != "" && !strings.HasPrefix(o.DestinationPath, "s3://") {
		return o, fmt.Errorf("destination path %q: only s3:// is supported", o.DestinationPath)
	}
	if o.CountMode == "" {
		o.CountMode = CountExact
	}
	if o.CountMode != CountExact && o.CountMode != CountEstimate {
		return o, fmt.Errorf("count mode must be %s or %s, got %q", CountExact, CountEstimate, o.CountMode)
	}
	if o.MaxRecoveryPointAge <= 0 {
		o.MaxRecoveryPointAge = DefaultMaxRecoveryPointAge
	}
	if o.StatementTimeout <= 0 {
		o.StatementTimeout = DefaultStatementTimeout
	}
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	if o.TeardownTimeout <= 0 {
		o.TeardownTimeout = DefaultTeardownTimeout
	}
	if o.MaxRecoveryAttempts <= 0 {
		o.MaxRecoveryAttempts = DefaultMaxRecoveryAttempts
	}
	if o.PollInterval <= 0 {
		o.PollInterval = DefaultPollInterval
	}
	if o.PGPort == 0 {
		o.PGPort = DefaultPGPort
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	for k, v := range o.MinRows {
		if v < 0 {
			return o, fmt.Errorf("min rows %s=%d: must not be negative", k, v)
		}
	}
	for _, e := range o.SidecarEnv {
		if staticCredentialEnv[e.Name] && !o.AllowStaticCredentials {
			return o, fmt.Errorf("sidecar environment %s would give the archive reader credentials of its own: the drill reads the archive with the restore identity only (AllowStaticCredentials is for S3-compatible test endpoints)", e.Name)
		}
	}
	return o, nil
}

// ParseMinRows parses "<db>=<n>" or "<db>/<schema>.<table>=<n>".
func ParseMinRows(s string) (key string, n int64, err error) {
	k, v, ok := strings.Cut(s, "=")
	if !ok || k == "" {
		return "", 0, fmt.Errorf("%q: want <db>=<n> or <db>/<schema>.<table>=<n>", s)
	}
	if db, table, isTable := strings.Cut(k, "/"); isTable {
		if db == "" || !strings.Contains(table, ".") || strings.HasPrefix(table, ".") || strings.HasSuffix(table, ".") {
			return "", 0, fmt.Errorf("%q: a table is <db>/<schema>.<table>", s)
		}
	}
	n, err = strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return "", 0, fmt.Errorf("%q: the minimum must be a non-negative integer", s)
	}
	return k, n, nil
}

// ParseKeyValue parses "key=value" (the value may be empty).
func ParseKeyValue(s string) (string, string, error) {
	k, v, ok := strings.Cut(s, "=")
	if !ok || k == "" {
		return "", "", fmt.Errorf("%q: want key=value", s)
	}
	return k, v, nil
}

// ParseToleration parses "key[=value]:Effect" (no value: operator Exists).
func ParseToleration(s string) (corev1.Toleration, error) {
	kv, effect, ok := strings.Cut(s, ":")
	if !ok || kv == "" {
		return corev1.Toleration{}, fmt.Errorf("%q: want key[=value]:Effect", s)
	}
	switch corev1.TaintEffect(effect) {
	case corev1.TaintEffectNoSchedule, corev1.TaintEffectPreferNoSchedule, corev1.TaintEffectNoExecute:
	default:
		return corev1.Toleration{}, fmt.Errorf("%q: effect must be NoSchedule, PreferNoSchedule or NoExecute", s)
	}
	t := corev1.Toleration{Effect: corev1.TaintEffect(effect)}
	if k, v, hasValue := strings.Cut(kv, "="); hasValue {
		t.Key, t.Operator, t.Value = k, corev1.TolerationOpEqual, v
	} else {
		t.Key, t.Operator = kv, corev1.TolerationOpExists
	}
	return t, nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

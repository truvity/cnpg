// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

package backupnaming_test

import (
	"strings"
	"testing"

	"github.com/truvity/cnpg/v2/pkg/backupnaming"
)

func TestProjectBucketName(t *testing.T) {
	for _, tc := range []struct {
		prefix, project, env, region string
		want                         string
		wantErr                      string
	}{
		{"acme-pg", "billing", "staging", "eu-west-1", "acme-pg-billing-staging-eu-west-1", ""},
		{"acme-pg", "ledger", "live", "us-east-2", "acme-pg-ledger-live-us-east-2", ""},
		{"x", "a", "b", "c", "x-a-b-c", ""},
		{"", "billing", "staging", "eu-west-1", "", "prefix is empty"},
		{"acme-pg", "", "staging", "eu-west-1", "", "project is empty"},
		{"acme-pg", "billing", "", "eu-west-1", "", "env is empty"},
		{"acme-pg", "billing", "staging", "", "", "region is empty"},
		{"acme-pg", "Billing", "staging", "eu-west-1", "", "not in [a-z0-9-]"},
		{"acme-pg", "bill_ing", "staging", "eu-west-1", "", "not in [a-z0-9-]"},
		{"acme-pg", strings.Repeat("p", 40), "staging", "eu-west-1", "", "allows 63"},
	} {
		got, err := backupnaming.ProjectBucketName(tc.prefix, tc.project, tc.env, tc.region)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("%+v: error %v, want one containing %q", tc, err, tc.wantErr)
			}

			continue
		}

		if err != nil || got != tc.want {
			t.Errorf("%+v: got %q, %v; want %q", tc, got, err, tc.want)
		}
	}
}

func TestValidateBucketName(t *testing.T) {
	for name, ok := range map[string]bool{
		"abc":                      true,
		"ab":                       false,
		"-abc":                     false,
		"abc-":                     false,
		"a.b.c":                    false,
		strings.Repeat("a", 63):    true,
		strings.Repeat("a", 64):    false,
		"example-backup-eu-west-1": true,
		"Example":                  false,
	} {
		if err := backupnaming.ValidateBucketName(name); (err == nil) != ok {
			t.Errorf("%q: error %v, want ok=%v", name, err, ok)
		}
	}
}

func TestRolesPrefixesAndServers(t *testing.T) {
	for _, tc := range []struct{ name, got, want string }{
		{"prefix", backupnaming.ClusterPrefix("shop", "shop-pg"), "shop/shop-pg/"},
		{"role prefix", backupnaming.RolePrefix("staging"), "staging-pg"},
		{"archive", backupnaming.ArchiveRoleName("staging", "shop-infra-pg"), "staging-pg-shop-infra-pg-archive"},
		{"restore", backupnaming.RestoreRoleName("staging", "shop-infra-pg"), "staging-pg-shop-infra-pg-restore"},
		{"restore SA", backupnaming.RestoreServiceAccount("shop-infra-pg"), "shop-infra-pg-restore"},
		{"server", backupnaming.ServerName("shop", "g2"), "shop-pg-g2"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

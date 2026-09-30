// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

package conformance_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type conditions struct {
	Items []struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Status struct {
			Conditions []struct {
				Type    string `json:"type"`
				Status  string `json:"status"`
				Message string `json:"message"`
			} `json:"conditions"`
		} `json:"status"`
	} `json:"items"`
}

// requestOutcome returns, for the CertificateRequests in a namespace, whether
// any was denied (with its message) and whether any was approved.
func (s *suite) requestOutcome(t *testing.T, ns string) (denied string, approved bool) {
	t.Helper()

	var list conditions
	require.NoError(t, json.Unmarshal([]byte(s.kubectl(t, "get", "certificaterequests", "-n", ns, "-o", "json")), &list))

	for _, item := range list.Items {
		for _, c := range item.Status.Conditions {
			switch {
			case c.Type == "Denied" && c.Status == "True":
				denied = c.Message
			case c.Type == "Approved" && c.Status == "True":
				approved = true
			}
		}
	}

	return denied, approved
}

func certificate(ns, name, commonName string) string {
	return fmt.Sprintf(`apiVersion: cert-manager.io/v1
kind: Certificate
metadata: {name: %s, namespace: %s}
spec:
  secretName: %s
  commonName: %s
  usages: [client auth]
  privateKey: {algorithm: ECDSA, size: 256}
  issuerRef: {name: cnpg-%s-%s, kind: ClusterIssuer, group: cert-manager.io}
`, name, ns, name, commonName, appNS, pgName)
}

// TestApproval covers the two refusals the platform's policy makes, and the
// admission guard the product cannot talk its way past.
func TestApproval(t *testing.T) {
	s := gate(t)
	t.Cleanup(func() { s.diagnostics(t) })

	t.Run("3a the policy refuses a request from another namespace", func(t *testing.T) {
		// A valid ask in every respect but its namespace: the role's own CN,
		// client-auth, ECDSA. Only the database namespace may ask this issuer.
		_, err := s.apply(certificate(otherNS, "intruder-ns", roleRW))
		require.NoError(t, err)

		// approver-policy answers a request no policy selects with an event
		// and leaves it unapproved (it cannot deny what is none of its
		// policies' business); a Denied condition would serve as well. Either
		// way nothing approves it, and so nothing signs it.
		eventually(t, 2*time.Minute, 3*time.Second, "approver-policy has looked at the request", func() error {
			events := s.kubectl(t, "get", "events", "-n", otherNS, "-o", "jsonpath={range .items[*]}{.reason}: {.message}{\"\\n\"}{end}")
			denied, approved := s.requestOutcome(t, otherNS)
			require.False(t, approved, "a request from another namespace was approved")

			if denied == "" && !strings.Contains(events, "not applicable for any policy") {
				return fmt.Errorf("no verdict yet:\n%s", events)
			}

			return nil
		})

		// Give cert-manager time to act on a wrongly approved request.
		time.Sleep(15 * time.Second)

		_, approved := s.requestOutcome(t, otherNS)
		assert.False(t, approved)
		assert.NotContains(t, s.kubectl(t, "get", "secrets", "-n", otherNS, "-o", "name"), "intruder-ns", "no certificate was issued")
		assert.Equal(t, "False", strings.TrimSpace(s.kubectl(t, "get", "certificate", "intruder-ns", "-n", otherNS, "-o", `jsonpath={.status.conditions[?(@.type=="Ready")].status}`)))
	})

	t.Run("3b the policy refuses a common name outside the allow-list", func(t *testing.T) {
		// The right namespace, the right usage; a name nobody declared.
		_, err := s.apply(certificate(appNS, "intruder-cn", "intruder"))
		require.NoError(t, err)

		eventually(t, 2*time.Minute, 3*time.Second, "the request is denied", func() error {
			denied, _ := s.requestOutcome(t, appNS)
			if !strings.Contains(denied, "common name") {
				return fmt.Errorf("no denial naming the common name yet (%q)", denied)
			}

			return nil
		})

		secrets := s.kubectl(t, "get", "secrets", "-n", appNS, "-o", "name")
		assert.NotContains(t, secrets, "intruder-cn", "no certificate was issued")
	})

	t.Run("4 the admission policy refuses a superuser DatabaseRole in a product namespace", func(t *testing.T) {
		role := func(ns, name, extra string) string {
			return fmt.Sprintf(`apiVersion: postgresql.cnpg.io/v1
kind: DatabaseRole
metadata: {name: %s, namespace: %s}
spec:
  cluster: {name: %s}
  name: %s
  ensure: present
  login: true
%s`, name, ns, pgName, strings.ReplaceAll(name, "-", "_"), extra)
		}

		out, err := s.apply(role(appNS, "evil-role", "  superuser: true\n"))
		if err == nil {
			s.kubectl(t, "delete", "databaserole", "evil-role", "-n", appNS)
		}

		require.Error(t, err, "a superuser DatabaseRole was admitted:\n%s", out)
		assert.Contains(t, err.Error(), "superuser")
		assert.Contains(t, err.Error(), "cnpg-database-role-guard")

		// The guard is about the attribute, not about DatabaseRoles: the
		// same object without it is admitted.
		_, err = s.apply(role(appNS, "fine-role", ""))
		require.NoError(t, err)
		s.kubectl(t, "delete", "databaserole", "fine-role", "-n", appNS)
	})
}

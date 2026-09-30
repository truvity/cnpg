// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

package conformance_test

import (
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClientAuthentication covers what a role certificate can and cannot do
// against a database that verifies clients against its own CA.
func TestClientAuthentication(t *testing.T) {
	s := gate(t)
	t.Cleanup(func() { s.diagnostics(t) })

	t.Run("1 a role certificate from the per-database CA authenticates with verify-full", func(t *testing.T) {
		// The libpq environment is the cnpg-client helper's: PGHOST is the
		// fully qualified Service name, PGSSLMODE=verify-full, and the
		// certificate, key and server CA are the projected files.
		for pod, role := range map[string]string{"client-rw": roleRW, "client-ro": roleRO} {
			who, err := s.psql(pod, "select current_user")
			require.NoError(t, err, pod)
			assert.Equal(t, role, who)

			ssl, err := s.psql(pod, "select ssl::text || ' ' || coalesce(client_dn, '') from pg_stat_ssl where pid = pg_backend_pid()")
			require.NoError(t, err, pod)
			assert.Contains(t, ssl, "true")
			assert.Contains(t, ssl, "CN="+role, "the server saw this role's certificate")
		}
	})

	t.Run("2 the same common name from another CA is refused", func(t *testing.T) {
		rogue, err := newAuthority("another CA")
		require.NoError(t, err)

		crt, key, err := rogue.clientCertificate(roleRW)
		require.NoError(t, err)

		// The certificate is perfect except for its issuer: same CN as a
		// role the database has, client-auth usage, in date.
		s.mustIn(t, 30*time.Second, crt, "kubectl", "exec", "-i", "-n", appNS, "client-rw", "-c", "psql", "--", "sh", "-c", "cat > /tmp/rogue.crt")
		s.mustIn(t, 30*time.Second, key, "kubectl", "exec", "-i", "-n", appNS, "client-rw", "-c", "psql", "--", "sh", "-c", "umask 077 && cat > /tmp/rogue.key")

		out, err := s.psql("client-rw", "select current_user", "PGSSLCERT=/tmp/rogue.crt", "PGSSLKEY=/tmp/rogue.key")
		require.Error(t, err, "a certificate from another CA logged in:\n%s", out)
		assert.Regexp(t, regexp.MustCompile(`(?i)unknown ca|certificate verify failed|authentication failed|bad certificate`), err.Error())
	})

	t.Run("7 a password login for a certificate-only role fails", func(t *testing.T) {
		primary := s.primaryPod(t, appNS, pgName)

		// Give the role a password on the server, so that a scram fallback
		// would have something to accept.
		s.superuserSQL(t, primary, "alter role "+roleRW+" password 'conformance-password'")

		out, err := s.psql("client-rw", "select current_user",
			"PGPASSWORD=conformance-password",
			"PGSSLCERT=", "PGSSLKEY=",
			"PGCONNECT_TIMEOUT=10")
		require.Error(t, err, "a password login was accepted for a certificate-only role:\n%s", out)
		assert.Regexp(t, regexp.MustCompile(`(?i)valid client certificate|password authentication failed|no pg_hba.conf entry|connection requires`), err.Error())
	})
}

// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

package conformance_test

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReplication covers the two things the operator does with the
// certificates the platform hands it: stream replication over a client CA it
// holds no key of, and serve a renewed server certificate without a restart.
func TestReplication(t *testing.T) {
	s := gate(t)
	t.Cleanup(func() { s.diagnostics(t) })

	t.Run("5 replication stays healthy with a client CA without its key", func(t *testing.T) {
		// What the operator was given: a CA certificate with no key, and our
		// own replication certificate.
		keys := s.kubectl(t, "get", "secret", pgName+"-client-ca", "-n", appNS, "-o", "jsonpath={.data}")

		var data map[string]string
		require.NoError(t, json.Unmarshal([]byte(keys), &data))
		assert.Contains(t, data, "ca.crt")
		assert.NotContains(t, data, "ca.key", "the client CA Secret carries the CA's key")
		assert.Equal(t, pgName+"-client-ca",
			s.kubectl(t, "get", "cluster.postgresql.cnpg.io", pgName, "-n", appNS, "-o", "jsonpath={.spec.certificates.clientCASecret}"))
		assert.Equal(t, pgName+"-replication",
			s.kubectl(t, "get", "cluster.postgresql.cnpg.io", pgName, "-n", appNS, "-o", "jsonpath={.spec.certificates.replicationTLSSecret}"))

		assert.Equal(t, "2", s.kubectl(t, "get", "cluster.postgresql.cnpg.io", pgName, "-n", appNS, "-o", "jsonpath={.status.readyInstances}"))

		primary := s.primaryPod(t, appNS, pgName)

		// The replica is connected and streaming, as the replication role.
		eventually(t, 2*time.Minute, 3*time.Second, "a streaming replica", func() error {
			out := s.superuserSQL(t, primary, "select usename || ' ' || state from pg_stat_replication")
			if !strings.Contains(out, "streaming_replica streaming") {
				return fmt.Errorf("pg_stat_replication: %q", out)
			}

			return nil
		})

		// Data written on the primary reaches the replica.
		s.superuserSQL(t, primary, "create table if not exists conformance_marker (n int); insert into conformance_marker values (42)")

		replica := strings.TrimSpace(s.kubectl(t, "get", "pods", "-n", appNS,
			"-l", "cnpg.io/cluster="+pgName+",cnpg.io/instanceRole=replica", "-o", "jsonpath={.items[0].metadata.name}"))
		require.NotEmpty(t, replica)

		eventually(t, 2*time.Minute, 2*time.Second, "the row on the replica", func() error {
			out := s.superuserSQL(t, replica, "select n from conformance_marker")
			if out != "42" {
				return fmt.Errorf("replica has %q", out)
			}

			return nil
		})
	})

	t.Run("6 a renewed server certificate is served without an instance restart", func(t *testing.T) {
		pods := s.instanceState(t)
		require.Len(t, pods, 2)

		primary := s.primaryPod(t, appNS, pgName)
		startedBefore := s.superuserSQL(t, primary, "select pg_postmaster_start_time()")

		before := s.servedSerial(t, primary)
		secretBefore := s.secretSerial(t)
		require.Equal(t, secretBefore.String(), before.String(), "the instance serves the Secret's certificate")

		// `cmctl renew`, without the binary: what it does is add an Issuing
		// condition to the Certificate's status.
		s.kubectl(t, "patch", "certificate", pgName+"-server-tls", "-n", appNS, "--subresource=status", "--type=json",
			"-p", `[{"op":"add","path":"/status/conditions/-","value":{"type":"Issuing","status":"True","reason":"ManuallyTriggered","message":"triggered by the conformance suite","lastTransitionTime":"`+time.Now().UTC().Format(time.RFC3339)+`"}}]`)

		eventually(t, 3*time.Minute, 3*time.Second, "cert-manager reissues the server certificate", func() error {
			if s.secretSerial(t).Cmp(before) == 0 {
				return fmt.Errorf("the Secret still holds serial %s", before)
			}

			return nil
		})

		want := s.secretSerial(t)

		// The operator reloads because the Secret carries cnpg.io/reload.
		eventually(t, 3*time.Minute, 3*time.Second, "the new certificate is served", func() error {
			if got := s.servedSerial(t, primary); got.Cmp(want) != 0 {
				return fmt.Errorf("serving %s, want %s", got, want)
			}

			return nil
		})

		// Not restarted: same pods, same containers, same postmaster.
		after := s.instanceState(t)
		assert.Equal(t, pods, after, "pod UIDs and container restart counts are unchanged")
		assert.Equal(t, startedBefore, s.superuserSQL(t, primary, "select pg_postmaster_start_time()"), "the postmaster was not restarted")

		// And clients keep working against the new certificate.
		who, err := s.psql("client-rw", "select ssl::text from pg_stat_ssl where pid = pg_backend_pid()")
		require.NoError(t, err, who)
		assert.Equal(t, "true", who)
	})
}

// instanceState is each instance pod's UID with its containers' restart
// counts: what a restart of any kind would change.
func (s *suite) instanceState(t *testing.T) map[string]string {
	t.Helper()

	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
				UID  string `json:"uid"`
			} `json:"metadata"`
			Status struct {
				ContainerStatuses []struct {
					RestartCount int `json:"restartCount"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}

	require.NoError(t, json.Unmarshal([]byte(s.kubectl(t, "get", "pods", "-n", appNS, "-l", "cnpg.io/cluster="+pgName, "-o", "json")), &list))

	out := map[string]string{}

	for _, item := range list.Items {
		restarts := 0
		for _, c := range item.Status.ContainerStatuses {
			restarts += c.RestartCount
		}

		out[item.Metadata.Name] = fmt.Sprintf("%s restarts=%d", item.Metadata.UID, restarts)
	}

	return out
}

// secretSerial is the serial number of the certificate in the server Secret.
func (s *suite) secretSerial(t *testing.T) *big.Int {
	t.Helper()

	encoded := s.kubectl(t, "get", "secret", pgName+"-server-tls", "-n", appNS, "-o", "jsonpath={.data.tls\\.crt}")

	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	require.NoError(t, err)

	block, _ := pem.Decode(raw)
	require.NotNil(t, block, "tls.crt is not PEM")

	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)

	return cert.SerialNumber
}

// servedSerial connects to an instance the way a PostgreSQL client does --
// an SSLRequest, then a TLS handshake -- through a port-forward, and returns
// the serial number of the certificate the server presents. pg_stat_ssl
// reports the CLIENT certificate's serial, not the server's, so this is the
// one way to see what the server is serving.
func (s *suite) servedSerial(t *testing.T, pod string) *big.Int {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	forward := exec.CommandContext(ctx, "kubectl", "port-forward", "-n", appNS, "pod/"+pod, ":5432")
	forward.Env = s.env

	stdout, err := forward.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, forward.Start())

	defer func() {
		cancel()
		_ = forward.Wait()
	}()

	line, err := bufio.NewReader(stdout).ReadString('\n')
	require.NoError(t, err)

	m := regexp.MustCompile(`127\.0\.0\.1:(\d+)`).FindStringSubmatch(line)
	require.NotNil(t, m, line)

	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+m[1], 10*time.Second)
	require.NoError(t, err)

	defer func() { _ = conn.Close() }()

	require.NoError(t, conn.SetDeadline(time.Now().Add(20*time.Second)))

	// SSLRequest: length 8, code 80877103.
	_, err = conn.Write([]byte{0, 0, 0, 8, 0x04, 0xd2, 0x16, 0x2f})
	require.NoError(t, err)

	reply := make([]byte, 1)
	_, err = conn.Read(reply)
	require.NoError(t, err)
	require.Equal(t, byte('S'), reply[0], "the server accepts SSL")

	client := tls.Client(conn, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // reading the certificate, not trusting it
	require.NoError(t, client.Handshake())

	return client.ConnectionState().PeerCertificates[0].SerialNumber
}

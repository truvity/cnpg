package com.truvity.cnpg

import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertFalse
import org.junit.jupiter.api.Assertions.assertThrows
import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.Test
import java.io.EOFException
import java.net.ConnectException
import java.net.SocketException
import java.net.UnknownHostException
import java.security.cert.CertificateException
import java.sql.SQLException
import java.sql.SQLTransientConnectionException
import javax.net.ssl.SSLHandshakeException

class RetryTest {
    private val fast = RetryPolicy(attempts = 4, initialDelayMs = 1, maxDelayMs = 5, budgetMs = 5_000)

    @Test
    fun `backoff is jittered under a doubling ceiling`() {
        val p = RetryPolicy(attempts = 9, initialDelayMs = 200, maxDelayMs = 5_000, budgetMs = 60_000)
        repeat(200) {
            assertTrue(backoffMs(p, 1) in 0..200)
            assertTrue(backoffMs(p, 3) in 0..800)
            assertTrue(backoffMs(p, 8) in 0..5_000)
            assertTrue(backoffMs(p, 500) in 0..5_000) // no overflow
        }
        assertTrue((1..200).map { backoffMs(p, 5) }.toSet().size > 1)
    }

    @Test
    fun `a connection error is retried until it passes`() {
        var n = 0
        val r = retry(fast) {
            if (++n < 3) throw SQLException("gone", "57P01")
            "ok"
        }
        assertEquals("ok", r)
        assertEquals(3, n)
    }

    @Test
    fun `it stops at the attempt count and rethrows the last error`() {
        var n = 0
        val e = assertThrows(SQLException::class.java) { retry(fast) { n++; throw SQLException("gone $n", "08006") } }
        assertEquals(4, n)
        assertEquals("gone 4", e.message)
    }

    @Test
    fun `a permanent error is not retried`() {
        var n = 0
        assertThrows(SQLException::class.java) { retry(fast) { n++; throw SQLException("dup", "23505") } }
        assertEquals(1, n)
    }

    @Test
    fun `the waiting budget bounds the retry`() {
        var n = 0
        val tight = RetryPolicy(attempts = 1_000, initialDelayMs = 50, maxDelayMs = 50, budgetMs = 120)
        assertThrows(SQLException::class.java) { retry(tight) { n++; throw SQLException("gone", "08006") } }
        assertTrue(n in 2..20, "tries: $n")
    }

    @Test
    fun `an interrupt stops the retry`() {
        var n = 0
        Thread.currentThread().interrupt()
        try {
            assertThrows(SQLException::class.java) { retry(fast) { n++; throw SQLException("gone", "08006") } }
            assertEquals(1, n)
        } finally {
            Thread.interrupted()
        }
    }

    @Test
    fun `classification by SQLSTATE`() {
        for (s in listOf("57P01", "57P02", "57P03", "25006", "53300", "08000", "08001", "08006", "08P01")) {
            assertTrue(isRetryable(SQLException("x", s)), s)
        }
        for (s in listOf("28P01", "28000", "42501", "42601", "23505", "23503", "57014", "40001", "40P01", "22P02")) {
            assertFalse(isRetryable(SQLException("x", s)), s)
        }
    }

    @Test
    fun `network errors are retried, certificate errors are not`() {
        assertTrue(isRetryable(ConnectException("refused")))
        assertTrue(isRetryable(SocketException("reset")))
        assertTrue(isRetryable(EOFException()))
        assertFalse(isRetryable(UnknownHostException("nope")))
        assertFalse(isRetryable(IllegalStateException("bug")))
        assertFalse(isRetryable(SSLHandshakeException("unknown ca")))
    }

    @Test
    fun `the whole cause chain is read`() {
        // pgjdbc: a refused certificate is SQLSTATE 08001 with the TLS error as the cause.
        val tls = SQLException("The connection attempt failed.", "08001", SSLHandshakeException("PKIX path building failed"))
        assertFalse(isRetryable(tls))
        val cert = SQLException("x", "08001", RuntimeException(CertificateException("expired")))
        assertFalse(isRetryable(cert))
        // pgjdbc: a host name the certificate does not carry has no TLS exception, only a message.
        assertFalse(isRetryable(SQLException("The hostname 10.0.0.1 could not be verified by hostnameverifier PgjdbcHostnameVerifier.", "08006")))
        // HikariCP: a timeout carrying the last failure.
        assertTrue(isRetryable(SQLTransientConnectionException("timed out", "08001", ConnectException("refused"))))
        assertFalse(isRetryable(SQLTransientConnectionException("timed out", "28P01")))
        assertTrue(isRetryable(SQLException("An I/O error occurred", null as String?, EOFException())))
        assertTrue(isRetryable(CnpgException("first connection", SQLException("x", "57P03"))))
    }
}

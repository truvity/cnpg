package com.truvity.cnpg

import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertFalse
import org.junit.jupiter.api.Assertions.assertThrows
import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.Test

class ConfigTest {
    private val base = mapOf(
        "PGHOST" to "db-rw.ns.svc",
        "PGDATABASE" to "app",
        "PGUSER" to "app_role",
        "PGSSLROOTCERT" to "/certs/ca.crt",
    )

    @Test
    fun `defaults follow the contract`() {
        val c = CnpgConfig.fromEnv(base)
        assertEquals(5432, c.port)
        assertEquals(5_000, c.connectTimeoutMs)
        assertEquals(30_000, c.statementTimeoutMs)
        assertEquals(60_000, c.idleInTxTimeoutMs)
        assertEquals(10, c.poolMax)
        assertEquals(0, c.poolMin)
        assertEquals(30 * 60_000L, c.maxConnLifetimeMs)
        assertEquals(5 * 60_000L, c.maxConnIdleMs)
        assertEquals(30_000, c.healthPeriodMs)
        assertEquals(RetryPolicy(5, 200, 5_000, 30_000), c.retry)
    }

    @Test
    fun `every CNPG_CLIENT variable is read`() {
        val c = CnpgConfig.fromEnv(
            base + mapOf(
                "PGPORT" to "6432",
                "PGAPPNAME" to "orders",
                "PGCONNECT_TIMEOUT" to "3",
                "PGSSLCERT" to "/certs/tls.crt",
                "PGSSLKEY" to "/certs/key.der",
                "CNPG_CLIENT_PASSWORD_FILE" to "/pw",
                "CNPG_CLIENT_STATEMENT_TIMEOUT" to "500ms",
                "CNPG_CLIENT_IDLE_TX_TIMEOUT" to "0",
                "CNPG_CLIENT_POOL_MAX" to "4",
                "CNPG_CLIENT_POOL_MIN" to "1",
                "CNPG_CLIENT_CONN_MAX_LIFETIME" to "10m",
                "CNPG_CLIENT_CONN_MAX_IDLE" to "1m",
                "CNPG_CLIENT_HEALTH_PERIOD" to "0",
                "CNPG_CLIENT_RETRY_ATTEMPTS" to "7",
                "CNPG_CLIENT_RETRY_MAX_DELAY" to "2s",
                "CNPG_CLIENT_RETRY_BUDGET" to "1m",
            ),
        )
        assertEquals(6432, c.port)
        assertEquals("orders", c.applicationName)
        assertEquals(3_000, c.connectTimeoutMs)
        assertEquals(500, c.statementTimeoutMs)
        assertEquals(0, c.idleInTxTimeoutMs)
        assertEquals(4, c.poolMax)
        assertEquals(1, c.poolMin)
        assertEquals(600_000, c.maxConnLifetimeMs)
        assertEquals(60_000, c.maxConnIdleMs)
        assertEquals(0, c.healthPeriodMs)
        assertEquals(RetryPolicy(7, 200, 2_000, 60_000), c.retry)
        assertEquals("/pw", c.passwordFile)
    }

    @Test
    fun `no CA, a weaker mode and a lone certificate are reported together`() {
        val e = assertThrows(ConfigError::class.java) {
            CnpgConfig.fromEnv(
                mapOf("PGHOST" to "h", "PGDATABASE" to "d", "PGUSER" to "u", "PGSSLMODE" to "require", "PGSSLCERT" to "/c"),
            )
        }
        assertEquals(3, e.problems.size, e.message)
        assertTrue(e.problems.any { "only verify-full is accepted" in it })
        assertTrue(e.problems.any { "server CA file is required" in it })
        assertTrue(e.problems.any { "go together" in it })
    }

    @Test
    fun `verify-full is the one accepted mode`() {
        CnpgConfig.fromEnv(base + ("PGSSLMODE" to "verify-full"))
        for (m in listOf("disable", "allow", "prefer", "require", "verify-ca")) {
            assertThrows(ConfigError::class.java) { CnpgConfig.fromEnv(base + ("PGSSLMODE" to m)) }
        }
    }

    @Test
    fun `a malformed value names its variable`() {
        val e = assertThrows(ConfigError::class.java) {
            CnpgConfig.fromEnv(base + mapOf("CNPG_CLIENT_POOL_MAX" to "ten", "CNPG_CLIENT_STATEMENT_TIMEOUT" to "30"))
        }
        assertTrue(e.problems.any { it.startsWith("CNPG_CLIENT_POOL_MAX=") })
        assertTrue(e.problems.any { it.startsWith("CNPG_CLIENT_STATEMENT_TIMEOUT=") })
    }

    @Test
    fun `a host that could carry a second host or a parameter is refused`() {
        for (h in listOf("a,b", "a b", "h/db", "h?x=1", "h&x", "u@h", "h#f")) {
            assertThrows(ConfigError::class.java) { CnpgConfig.fromEnv(base + ("PGHOST" to h)) }
        }
        CnpgConfig.fromEnv(base + ("PGHOST" to "::1"))
    }

    @Test
    fun `pool limits and HikariCP floors are validated`() {
        fun bad(c: CnpgConfig) = assertThrows(ConfigError::class.java) { c.validate() }
        val ok = CnpgConfig(host = "h", database = "d", user = "u", sslRootCert = "/ca")
        ok.validate()
        bad(ok.copy(poolMax = 0))
        bad(ok.copy(poolMin = 11))
        bad(ok.copy(maxConnLifetimeMs = 1_000))
        bad(ok.copy(maxConnIdleMs = 1_000))
        bad(ok.copy(healthPeriodMs = 1_000))
        bad(ok.copy(retry = RetryPolicy(attempts = 0)))
        bad(ok.copy(retry = RetryPolicy(initialDelayMs = 10_000, maxDelayMs = 100)))
    }

    @Test
    fun `durations`() {
        assertEquals(0, parseDuration("0"))
        assertEquals(500, parseDuration("500ms"))
        assertEquals(1_500, parseDuration("1.5s"))
        assertEquals(300_000, parseDuration("5m"))
        assertEquals(3_600_000, parseDuration("1h"))
        for (s in listOf("", "5", "5d", "-1s", "s")) {
            assertThrows(IllegalArgumentException::class.java) { parseDuration(s) }
        }
    }

    @Test
    fun `neither the password nor the key appears in a string form`() {
        val c = CnpgConfig(
            host = "h", database = "d", user = "u", sslRootCert = "/ca",
            password = "hunter2-secret", sslCert = "/c", sslKey = "/k",
        )
        val shown = c.toString()
        assertFalse("hunter2-secret" in shown, shown)
        assertTrue("<redacted>" in shown)
        assertTrue("sslmode=verify-full" in shown)
    }

    @Test
    fun `connection properties hold the paths and a fresh password`(@org.junit.jupiter.api.io.TempDir dir: java.nio.file.Path) {
        val file = dir.resolve("pw")
        java.nio.file.Files.writeString(file, "first\n")
        val ds = ReloadingDataSource(
            CnpgConfig(host = "h", database = "d", user = "u", sslRootCert = "/ca", passwordFile = file.toString()),
        )
        val p1 = ds.connectionProperties()
        assertEquals("first", p1["password"])
        assertEquals("verify-full", p1["sslmode"])
        assertEquals("/ca", p1["sslrootcert"])
        assertEquals("-c statement_timeout=30000 -c idle_in_transaction_session_timeout=60000", p1["options"])
        java.nio.file.Files.writeString(file, "second\n")
        assertEquals("second", ds.connectionProperties()["password"])
    }
}

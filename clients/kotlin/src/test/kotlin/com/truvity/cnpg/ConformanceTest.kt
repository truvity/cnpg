package com.truvity.cnpg

import io.opentelemetry.sdk.testing.exporter.InMemorySpanExporter
import io.opentelemetry.sdk.trace.SdkTracerProvider
import io.opentelemetry.sdk.trace.export.SimpleSpanProcessor
import org.junit.jupiter.api.AfterAll
import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertFalse
import org.junit.jupiter.api.Assertions.assertNotEquals
import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.Assumptions.assumeTrue
import org.junit.jupiter.api.BeforeAll
import org.junit.jupiter.api.DisplayName
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.TestInstance
import org.junit.jupiter.api.fail
import java.io.IOException
import java.net.InetAddress
import java.net.ServerSocket
import java.net.Socket
import java.nio.file.Files
import java.nio.file.Path
import java.nio.file.StandardCopyOption
import java.sql.SQLException
import java.util.Collections
import java.util.concurrent.CopyOnWriteArrayList
import javax.net.ssl.SSLException

/**
 * The conformance suite: every case in clients/conformance/cases.txt, against a real
 * PostgreSQL that serves TLS (clients/conformance/pg-tls.sh). The display names ARE the
 * case names; the CI guard fails unless each one ran and passed. Without
 * CNPG_CLIENTS_PG_HOST the suite is skipped, or, with CNPG_CLIENTS_PG=required, fails.
 */
@TestInstance(TestInstance.Lifecycle.PER_CLASS)
class ConformanceTest {
    private val env = System.getenv()
    private val host = env["CNPG_CLIENTS_PG_HOST"]
    private val dir = env["CNPG_CLIENTS_PG_DIR"] ?: ""
    private val addr = env["CNPG_CLIENTS_PG_ADDR"] ?: ""
    private val port = env["CNPG_CLIENTS_PG_PORT"]?.toInt() ?: 0
    private val pwRole = env["CNPG_CLIENTS_PG_PW_ROLE"] ?: ""
    private val pw = env["CNPG_CLIENTS_PG_PW_PASSWORD"] ?: ""
    private val database = env["CNPG_CLIENTS_PG_DATABASE"] ?: ""

    private val pools = CopyOnWriteArrayList<CnpgPool>()

    @BeforeAll
    fun requirePostgres() {
        if (host.isNullOrEmpty()) {
            if (env["CNPG_CLIENTS_PG"] == "required") {
                fail("CNPG_CLIENTS_PG=required but no TLS PostgreSQL is configured (clients/conformance/pg-tls.sh up)")
            }
            assumeTrue(false, "no TLS PostgreSQL configured")
        }
    }

    @AfterAll
    fun closePools() {
        pools.forEach { runCatching { it.close() } }
    }

    private fun copy(from: String, to: Path) {
        val tmp = Files.createTempFile(to.parent, ".swap", null)
        Files.copy(Path.of(dir, from), tmp, StandardCopyOption.REPLACE_EXISTING)
        Files.move(tmp, to, StandardCopyOption.ATOMIC_MOVE, StandardCopyOption.REPLACE_EXISTING)
    }

    /** Lays the files out as the cnpg-client chart does (keyDer: true): ca.crt, tls.crt, key.der. */
    private fun mount(cert: String): Path {
        val m = Files.createTempDirectory("cnpg-kt-")
        copy("ca.crt", m.resolve("ca.crt"))
        setCert(m, cert)
        return m
    }

    private fun setCert(m: Path, cert: String) {
        copy("$cert.crt", m.resolve("tls.crt"))
        copy("$cert.key.der", m.resolve("key.der"))
    }

    private val fastRetry = RetryPolicy(attempts = 3, initialDelayMs = 20, maxDelayMs = 100, budgetMs = 5_000)

    private fun certConfig(m: Path) = CnpgConfig(
        host = host!!,
        port = port,
        database = database,
        user = "app_cert",
        sslRootCert = m.resolve("ca.crt").toString(),
        sslCert = m.resolve("tls.crt").toString(),
        sslKey = m.resolve("key.der").toString(),
        applicationName = "conformance",
        retry = fastRetry,
    )

    private fun passwordConfig(m: Path) =
        certConfig(m).copy(user = pwRole, password = pw, sslCert = null, sslKey = null)

    private fun open(c: CnpgConfig, tracer: io.opentelemetry.api.trace.Tracer? = null): CnpgPool =
        CnpgPool.create(c, tracer).also { pools += it }

    @Suppress("UNCHECKED_CAST")
    private fun <T> CnpgPool.scalar(sql: String, vararg params: Any?): T =
        query(sql, params.toList()) { it.getObject(1) }.first() as T

    private fun rejection(fn: () -> Unit): Throwable {
        try {
            fn()
        } catch (e: Throwable) {
            return e
        }
        throw AssertionError("expected a failure")
    }

    private fun chain(t: Throwable) = generateSequence(t) { it.cause }.toList()
    private fun sqlState(t: Throwable) = chain(t).filterIsInstance<SQLException>().firstNotNullOfOrNull { it.sqlState }

    /** A TCP forwarder that can be dropped and restored: the rw Service while the primary behind it switches. */
    private inner class Forwarder {
        private var server: ServerSocket? = null
        private val sockets: MutableSet<Socket> = Collections.synchronizedSet(HashSet())
        var port = 0

        fun up(listenPort: Int = 0) {
            val s = ServerSocket(listenPort, 50, InetAddress.getByName("127.0.0.1"))
            server = s
            port = s.localPort
            Thread {
                while (!s.isClosed) {
                    val c = try { s.accept() } catch (_: IOException) { break }
                    val u = try { Socket(addr, this@ConformanceTest.port) } catch (_: IOException) { c.close(); continue }
                    sockets += c
                    sockets += u
                    pipe(c, u)
                    pipe(u, c)
                }
            }.apply { isDaemon = true }.start()
        }

        private fun pipe(from: Socket, to: Socket) {
            Thread {
                try {
                    from.getInputStream().transferTo(to.getOutputStream())
                } catch (_: IOException) {
                } finally {
                    runCatching { from.close() }
                    runCatching { to.close() }
                }
            }.apply { isDaemon = true }.start()
        }

        fun down() {
            runCatching { server?.close() }
            synchronized(sockets) { sockets.toList() }.forEach { runCatching { it.close() } }
            sockets.clear()
        }
    }

    @Test
    @DisplayName("verify-full-connects")
    fun `verify-full-connects`() {
        val p = open(certConfig(mount("app_cert.1")))
        assertEquals(true, p.scalar<Boolean>("select ssl from pg_stat_ssl where pid = pg_backend_pid()"))
        assertEquals("conformance", p.scalar<String>("select current_setting('application_name')"))
        assertEquals("30s", p.scalar<String>("select current_setting('statement_timeout')"))
        assertEquals("1min", p.scalar<String>("select current_setting('idle_in_transaction_session_timeout')"))
        p.health()
    }

    @Test
    @DisplayName("rejects-unknown-ca")
    fun `rejects-unknown-ca`() {
        val m = mount("app_cert.1")
        copy("other-ca.crt", m.resolve("ca.crt"))
        val err = rejection { CnpgPool.create(certConfig(m)) }
        assertFalse(isRetryable(err), err.toString())
        assertTrue(chain(err).any { it is SSLException }, "expected a TLS error in $err")
    }

    @Test
    @DisplayName("rejects-hostname-mismatch")
    fun `rejects-hostname-mismatch`() {
        val err = rejection { CnpgPool.create(certConfig(mount("app_cert.1")).copy(host = addr)) }
        assertFalse(isRetryable(err), err.toString())
    }

    @Test
    @DisplayName("refuses-weaker-sslmode")
    fun `refuses-weaker-sslmode`() {
        val m = mount("app_cert.1")
        for (mode in listOf("disable", "allow", "prefer", "require", "verify-ca")) {
            val e1 = rejection { CnpgPool.create(certConfig(m).copy(sslMode = mode)) }
            assertTrue(e1 is ConfigError && "only verify-full is accepted" in e1.message!!, "$mode: $e1")
            val e2 = rejection {
                CnpgConfig.fromEnv(
                    mapOf("PGHOST" to "h", "PGDATABASE" to "d", "PGUSER" to "u", "PGSSLROOTCERT" to "/ca", "PGSSLMODE" to mode),
                )
            }
            assertTrue(e2 is ConfigError && "only verify-full is accepted" in e2.message!!, "$mode: $e2")
        }
        val e3 = rejection { CnpgPool.create(certConfig(m).copy(sslRootCert = "")) }
        assertTrue(e3 is ConfigError && "server CA file is required" in e3.message!!, e3.toString())
    }

    @Test
    @DisplayName("password-role")
    fun `password-role`() {
        val m = mount("app_cert.1")
        val p = open(passwordConfig(m))
        assertEquals(pwRole, p.scalar<String>("select current_user"))

        val err = rejection { CnpgPool.create(passwordConfig(m).copy(password = "wrong")) }
        assertEquals("28P01", sqlState(err), err.toString())
        assertFalse(isRetryable(err))
    }

    @Test
    @DisplayName("client-cert-role")
    fun `client-cert-role`() {
        val p = open(certConfig(mount("app_cert.1")))
        assertEquals("app_cert", p.scalar<String>("select current_user"))

        // The right name from the wrong authority is refused, and not retried.
        val m = mount("app_cert.1")
        copy("foreign.crt", m.resolve("tls.crt"))
        copy("foreign.key.der", m.resolve("key.der"))
        val start = System.nanoTime()
        val err = rejection { CnpgPool.create(certConfig(m)) }
        assertFalse(isRetryable(err), err.toString())
        assertTrue((System.nanoTime() - start) / 1_000_000 < 2_000)
    }

    @Test
    @DisplayName("client-cert-rotation")
    fun `client-cert-rotation`() {
        val m = mount("app_cert.1")
        // HikariCP retires a connection no sooner than 30 seconds.
        val p = open(certConfig(m).copy(maxConnLifetimeMs = 30_000))
        fun serial() = p.scalar<String>("select client_serial::text from pg_stat_ssl where pid = pg_backend_pid()")
        assertEquals(0x101.toString(), serial())
        setCert(m, "app_cert.2") // what cert-manager's renewal does to the mounted files
        val deadline = System.nanoTime() + 60_000_000_000L
        while (true) {
            // The two files are replaced one after the other: a connection opened between them
            // sees a pair that does not match and fails. That is the window, not the bug.
            val s = runCatching { serial() }.getOrNull()
            if (s == 0x202.toString()) break
            assertTrue(System.nanoTime() < deadline, "the renewed certificate was never picked up")
            Thread.sleep(250)
        }
    }

    @Test
    @DisplayName("password-file-rotation")
    fun `password-file-rotation`() {
        val m = mount("app_cert.1")
        val file = Files.createTempDirectory("cnpg-kt-pw-").resolve("password")
        fun write(v: String) = Files.writeString(file, "$v\n")
        write(pw)
        val cfg = passwordConfig(m).copy(password = null, passwordFile = file.toString(), maxConnLifetimeMs = 30_000)
        val p = open(cfg)
        assertEquals(pwRole, p.scalar<String>("select current_user"))
        val firstPid = p.scalar<Int>("select pg_backend_pid()")

        val next = "rotated-password"
        p.update("alter role $pwRole password '$next'")
        try {
            write(next)
            // The lifetime retires the pooled connection; the next one must read the new file,
            // or authentication fails (the old password is gone).
            val deadline = System.nanoTime() + 60_000_000_000L
            while (true) {
                val pid = p.scalar<Int>("select pg_backend_pid()")
                if (pid != firstPid) break
                assertTrue(System.nanoTime() < deadline, "no new connection was made")
                Thread.sleep(250)
            }
            assertEquals(pwRole, p.scalar<String>("select current_user"))
        } finally {
            write(next)
            val q = open(passwordConfig(m).copy(password = next))
            q.update("alter role $pwRole password '$pw'")
        }
    }

    @Test
    @DisplayName("reconnects-after-backend-termination")
    fun `reconnects-after-backend-termination`() {
        val p = open(certConfig(mount("app_cert.1")).copy(poolMax = 2))
        p.withConnection { victim ->
            val pid = victim.createStatement().use { st -> st.executeQuery("select pg_backend_pid()").use { it.next(); it.getInt(1) } }
            assertEquals(true, p.scalar<Boolean>("select pg_terminate_backend(?)", pid))
        } // closed broken (or found dead on the next borrow): the pool drops it

        var attempts = 0
        p.withRetry {
            attempts++
            p.query("select 1") { it.getInt(1) }
        }
        assertTrue(attempts >= 1)
        p.health()
    }

    @Test
    @DisplayName("survives-primary-switch")
    fun `survives-primary-switch`() {
        val f = Forwarder()
        f.up()
        try {
            val policy = RetryPolicy(attempts = 20, initialDelayMs = 50, maxDelayMs = 500, budgetMs = 15_000)
            val p = open(certConfig(mount("app_cert.1")).copy(port = f.port, retry = policy))
            assertEquals("app_cert", p.scalar<String>("select current_user"))

            val listenPort = f.port
            f.down() // the old primary is gone and nothing answers yet
            val downAt = System.nanoTime()
            Thread {
                Thread.sleep(1_500)
                f.up(listenPort)
            }.apply { isDaemon = true }.start()

            p.withRetry { p.query("select current_user") { it.getString(1) } }
            val elapsedMs = (System.nanoTime() - downAt) / 1_000_000
            assertTrue(elapsedMs >= 1_000, "the call finished before the Service came back (${elapsedMs}ms)")
        } finally {
            f.down()
        }
    }

    @Test
    @DisplayName("statement-timeout-enforced")
    fun `statement-timeout-enforced`() {
        val p = open(certConfig(mount("app_cert.1")).copy(statementTimeoutMs = 300))
        val start = System.nanoTime()
        var attempts = 0
        val err = rejection {
            p.withRetry {
                attempts++
                p.query("select pg_sleep(5)") { }
            }
        }
        assertEquals("57014", sqlState(err), err.toString())
        assertEquals(1, attempts, "a timeout is not a connection failure")
        assertTrue((System.nanoTime() - start) / 1_000_000 < 3_000)
    }

    @Test
    @DisplayName("permanent-error-not-retried")
    fun `permanent-error-not-retried`() {
        val p = open(certConfig(mount("app_cert.1")))
        p.update("drop table if exists cnpg_clients_conf_kt")
        p.update("create table cnpg_clients_conf_kt (id int primary key)")
        p.update("insert into cnpg_clients_conf_kt values (1)")
        try {
            var attempts = 0
            val err = rejection {
                p.withRetry {
                    attempts++
                    p.update("insert into cnpg_clients_conf_kt values (1)")
                }
            }
            assertEquals("23505", sqlState(err), err.toString())
            assertEquals(1, attempts)
        } finally {
            p.update("drop table if exists cnpg_clients_conf_kt")
        }
    }

    @Test
    @DisplayName("health-check")
    fun `health-check`() {
        val f = Forwarder()
        f.up()
        try {
            val p = open(certConfig(mount("app_cert.1")).copy(port = f.port))
            p.health()
            f.down()
            val start = System.nanoTime()
            val err = rejection { p.health() }
            assertTrue(err is CnpgException, err.toString())
            assertTrue((System.nanoTime() - start) / 1_000_000 < HEALTH_TIMEOUT_MS + 1_000)
        } finally {
            f.down()
        }
    }

    @Test
    @DisplayName("traces-statements-without-arguments")
    fun `traces-statements-without-arguments`() {
        val exporter = InMemorySpanExporter.create()
        val provider = SdkTracerProvider.builder().addSpanProcessor(SimpleSpanProcessor.create(exporter)).build()
        val p = open(certConfig(mount("app_cert.1")), provider.get("test"))
        val secret = "user-data-that-must-not-be-traced"
        assertEquals(secret, p.scalar<String>("select ?::text", secret))

        val spans = exporter.finishedSpanItems
        assertTrue(spans.any { it.attributes.asMap().entries.any { (k, v) -> k.key == "db.query.text" && v == "select ?::text" } })
        for (s in spans) {
            assertEquals("postgresql", s.attributes.asMap().entries.first { it.key.key == "db.system.name" }.value)
            assertFalse(s.toString().contains(secret) || s.attributes.toString().contains(secret))
        }
        assertNotEquals(0, spans.size)
    }
}

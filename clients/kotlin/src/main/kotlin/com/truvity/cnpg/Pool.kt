package com.truvity.cnpg

import com.zaxxer.hikari.HikariConfig
import com.zaxxer.hikari.HikariDataSource
import io.opentelemetry.api.common.AttributeKey
import io.opentelemetry.api.trace.SpanKind
import io.opentelemetry.api.trace.StatusCode
import io.opentelemetry.api.trace.Tracer
import java.io.IOException
import java.io.PrintWriter
import java.net.URLEncoder
import java.nio.charset.StandardCharsets
import java.nio.file.Files
import java.nio.file.Path
import java.sql.Connection
import java.sql.ResultSet
import java.sql.SQLException
import java.util.Properties
import java.util.concurrent.ExecutionException
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import java.util.concurrent.TimeoutException
import java.util.logging.Logger
import javax.sql.DataSource

/** Bounds [CnpgPool.health]. */
const val HEALTH_TIMEOUT_MS = 2_000L

/** An error from this library itself (the driver's own errors are passed through, in the cause chain). */
class CnpgException(message: String, cause: Throwable? = null) : RuntimeException(message, cause)

/** Open, idle and waiting connections, for a metrics exporter. */
data class PoolStats(val total: Int, val idle: Int, val waiting: Int)

/**
 * A [DataSource] that builds the connection properties anew for every
 * physical connection, so a renewed certificate, key, CA or password file
 * reaches the next connection without a restart.
 *
 * pgjdbc itself reads `sslrootcert`, `sslcert` and `sslkey` when it opens a
 * connection (nothing is cached across connections), so passing the paths is
 * enough for TLS. The password is not a file to pgjdbc, so it is read here.
 * The URL is built from the parts; the caller never supplies one.
 */
class ReloadingDataSource(private val config: CnpgConfig) : DataSource {
    init {
        config.validate()
    }

    private val url: String = run {
        val host = if (':' in config.host) "[${config.host}]" else config.host
        val db = URLEncoder.encode(config.database, StandardCharsets.UTF_8).replace("+", "%20")
        "jdbc:postgresql://$host:${config.port}/$db"
    }

    private var loginTimeoutSeconds = 0

    private fun readPassword(): String? {
        val file = config.passwordFile
        if (!file.isNullOrEmpty()) {
            try {
                return Files.readString(Path.of(file)).trimEnd('\r', '\n')
            } catch (e: IOException) {
                throw SQLException("cnpg-client: reading the password file: ${e.message}", e)
            }
        }
        return config.password
    }

    /** Connection properties as of NOW. Visible for tests; contains the password. */
    internal fun connectionProperties(): Properties {
        val c = config
        val p = Properties()
        p["user"] = c.user
        readPassword()?.let { p["password"] = it }
        // verify-full against the given CA file only (pgjdbc's LibPQFactory builds a trust
        // store from exactly this file, not the JVM's), server name = host.
        p["ssl"] = "true"
        p["sslmode"] = "verify-full"
        p["sslfactory"] = "org.postgresql.ssl.LibPQFactory"
        p["sslrootcert"] = c.sslRootCert
        if (!c.sslCert.isNullOrEmpty() && !c.sslKey.isNullOrEmpty()) {
            p["sslcert"] = c.sslCert
            p["sslkey"] = c.sslKey
        }
        p["ApplicationName"] = c.applicationName
        p["connectTimeout"] = ((c.connectTimeoutMs + 999) / 1000).toString()
        p["tcpKeepAlive"] = "true"
        val options = buildList {
            if (c.statementTimeoutMs > 0) add("-c statement_timeout=${c.statementTimeoutMs}")
            if (c.idleInTxTimeoutMs > 0) add("-c idle_in_transaction_session_timeout=${c.idleInTxTimeoutMs}")
        }
        if (options.isNotEmpty()) p["options"] = options.joinToString(" ")
        return p
    }

    override fun getConnection(): Connection =
        org.postgresql.Driver().connect(url, connectionProperties())
            ?: throw SQLException("cnpg-client: the driver refused the URL")

    override fun getConnection(username: String?, password: String?): Connection = connection

    override fun getLogWriter(): PrintWriter? = null
    override fun setLogWriter(out: PrintWriter?) {}
    override fun setLoginTimeout(seconds: Int) {
        loginTimeoutSeconds = seconds
    }

    override fun getLoginTimeout(): Int = loginTimeoutSeconds
    override fun getParentLogger(): Logger = Logger.getLogger("com.truvity.cnpg")
    override fun <T : Any?> unwrap(iface: Class<T>): T =
        if (iface.isInstance(this)) iface.cast(this) else throw SQLException("not a wrapper for $iface")

    override fun isWrapperFor(iface: Class<*>): Boolean = iface.isInstance(this)
}

private val HEALTH_EXECUTOR = Executors.newCachedThreadPool { r ->
    Thread(r, "cnpg-health").apply { isDaemon = true }
}

private val DB_SYSTEM = AttributeKey.stringKey("db.system.name")
private val DB_NAMESPACE = AttributeKey.stringKey("db.namespace")
private val DB_OPERATION = AttributeKey.stringKey("db.operation.name")
private val DB_QUERY_TEXT = AttributeKey.stringKey("db.query.text")
private val SERVER_ADDRESS = AttributeKey.stringKey("server.address")
private val SERVER_PORT = AttributeKey.longKey("server.port")

private val OPERATION = Regex("^[\\s(]*([A-Za-z]{1,16})\\b")

internal fun operation(sql: String): String = OPERATION.find(sql)?.groupValues?.get(1)?.uppercase() ?: "QUERY"

/**
 * A HikariCP pool over pgjdbc with the contract's behaviour: verify-full
 * against the given CA, certificate, key and password reload per connection,
 * bounded connection lifetime, failover-aware [withRetry] and a [health] check.
 *
 * [dataSource] is the pooled `javax.sql.DataSource`, for use with an ORM or a
 * query library; statements run through it are not traced. [query] and
 * [update] are traced when a [Tracer] was given.
 */
class CnpgPool private constructor(
    val config: CnpgConfig,
    private val tracer: Tracer?,
    val dataSource: HikariDataSource,
) : AutoCloseable {

    /** Runs [block] on a pooled connection and returns the connection to the pool. */
    fun <T> withConnection(block: (Connection) -> T): T = dataSource.connection.use(block)

    /** Runs one query and maps every row. [params] bind to the `?` placeholders. */
    fun <T> query(sql: String, params: List<Any?> = emptyList(), map: (ResultSet) -> T): List<T> =
        traced(sql) {
            withConnection { c ->
                c.prepareStatement(sql).use { st ->
                    params.forEachIndexed { i, v -> st.setObject(i + 1, v) }
                    st.executeQuery().use { rs ->
                        buildList { while (rs.next()) add(map(rs)) }
                    }
                }
            }
        }

    /** Runs one statement and returns the update count. */
    fun update(sql: String, params: List<Any?> = emptyList()): Int =
        traced(sql) {
            withConnection { c ->
                c.prepareStatement(sql).use { st ->
                    params.forEachIndexed { i, v -> st.setObject(i + 1, v) }
                    st.executeUpdate()
                }
            }
        }

    private fun <T> traced(sql: String, body: () -> T): T {
        val t = tracer ?: return body()
        val op = operation(sql)
        val span = t.spanBuilder("$op ${config.database}")
            .setSpanKind(SpanKind.CLIENT)
            .setAttribute(DB_SYSTEM, "postgresql")
            .setAttribute(DB_NAMESPACE, config.database)
            .setAttribute(DB_OPERATION, op)
            .setAttribute(DB_QUERY_TEXT, sql) // placeholders only; the arguments are user data
            .setAttribute(SERVER_ADDRESS, config.host)
            .setAttribute(SERVER_PORT, config.port.toLong())
            .startSpan()
        val scope = span.makeCurrent()
        try {
            return body()
        } catch (e: Exception) {
            span.recordException(e)
            span.setStatus(StatusCode.ERROR, "query failed")
            throw e
        } finally {
            scope.close()
            span.end()
        }
    }

    /** Runs [fn] under the retry policy; see [retry]. */
    fun <T> withRetry(fn: () -> T): T = retry(config.retry, fn)

    /**
     * `SELECT 1` on a pooled connection, bounded by [HEALTH_TIMEOUT_MS]. It
     * exercises the real path (Service, TLS, authentication), so it suits a
     * readiness probe. Throws [CnpgException] when unhealthy.
     */
    fun health() {
        val f = HEALTH_EXECUTOR.submit { select1(dataSource) }
        try {
            f.get(HEALTH_TIMEOUT_MS, TimeUnit.MILLISECONDS)
        } catch (_: TimeoutException) {
            f.cancel(true)
            throw CnpgException("cnpg-client: health: timed out")
        } catch (e: ExecutionException) {
            throw CnpgException("cnpg-client: health: ${e.cause?.message}", e.cause)
        }
    }

    fun stats(): PoolStats {
        val m = dataSource.hikariPoolMXBean
        return PoolStats(m.totalConnections, m.idleConnections, m.threadsAwaitingConnection)
    }

    /** Closes every connection. */
    override fun close() = dataSource.close()

    /** Safe to log: no password, no key material. */
    override fun toString(): String = "CnpgPool($config)"

    companion object {
        /**
         * Validates, proves the first connection under the retry policy (a startup race
         * with the Service is retried like any other) and builds the pool.
         */
        @JvmStatic
        @JvmOverloads
        fun create(config: CnpgConfig, tracer: Tracer? = null): CnpgPool {
            config.validate()
            val source = ReloadingDataSource(config)
            try {
                retry(config.retry) { select1(source) }
            } catch (e: Exception) {
                throw CnpgException(
                    "cnpg-client: first connection to ${config.host}:${config.port}: ${e.message}",
                    e,
                )
            }
            val h = HikariConfig()
            h.dataSource = source
            h.poolName = "cnpg-${config.applicationName}"
            h.maximumPoolSize = config.poolMax
            h.minimumIdle = config.poolMin
            h.maxLifetime = config.maxConnLifetimeMs
            h.idleTimeout = config.maxConnIdleMs
            h.keepaliveTime = config.healthPeriodMs
            h.connectionTimeout = maxOf(250L, config.connectTimeoutMs)
            h.validationTimeout = minOf(5_000L, h.connectionTimeout)
            // The first connection was proven above, with the real error; HikariCP's own
            // start-up check would only repeat it.
            h.initializationFailTimeout = -1
            h.isRegisterMbeans = false
            return CnpgPool(config, tracer, HikariDataSource(h))
        }

        private fun select1(source: DataSource) {
            source.connection.use { c ->
                c.createStatement().use { st ->
                    st.queryTimeout = (HEALTH_TIMEOUT_MS / 1000).toInt()
                    st.executeQuery("select 1").use { rs -> check(rs.next()) { "select 1 returned no row" } }
                }
            }
        }
    }
}

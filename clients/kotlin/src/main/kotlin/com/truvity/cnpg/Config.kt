package com.truvity.cnpg

/** Bounds the retry of connection-class failures. */
data class RetryPolicy(
    /** Total tries, including the first. */
    val attempts: Int = 5,
    /** Ceiling of the first sleep, ms. */
    val initialDelayMs: Long = 200,
    /** Ceiling of any sleep, ms. */
    val maxDelayMs: Long = 5_000,
    /** Total time that may be spent waiting, ms. */
    val budgetMs: Long = 30_000,
)

/** Every problem with a configuration, at once. */
class ConfigError(val problems: List<String>) :
    IllegalArgumentException("cnpg-client: invalid configuration:\n  - " + problems.joinToString("\n  - "))

/**
 * What a connection needs. There is no free-form URL: a parameter passed through
 * a string can be dropped on the way to the driver, and a dropped `sslrootcert`
 * turns verify-full into a connection that does not verify.
 */
data class CnpgConfig(
    val host: String,
    val database: String,
    val user: String,
    /** Server CA file. Required: this file is the only trust root. */
    val sslRootCert: String,
    val port: Int = 5432,
    /** For a scram role. [passwordFile] wins and is read again for every new connection. */
    val password: String? = null,
    val passwordFile: String? = null,
    /** Client certificate and key of a certificate role; both or neither. pgjdbc reads the key as DER PKCS#8 (or unencrypted PEM PKCS#8). */
    val sslCert: String? = null,
    val sslKey: String? = null,
    /** Exists to be refused: null or "verify-full". */
    val sslMode: String? = null,
    val applicationName: String = programName(),
    val connectTimeoutMs: Long = 5_000,
    /** 0 disables. */
    val statementTimeoutMs: Long = 30_000,
    /** 0 disables. */
    val idleInTxTimeoutMs: Long = 60_000,
    val poolMax: Int = 10,
    val poolMin: Int = 0,
    /**
     * Keep below the client certificate's life. HikariCP does not retire connections
     * sooner than 30 seconds, so that is the floor.
     */
    val maxConnLifetimeMs: Long = 30 * 60_000,
    /** HikariCP's floor is 10 seconds. */
    val maxConnIdleMs: Long = 5 * 60_000,
    /** Background check of idle pooled connections; 0 disables, otherwise at least 10 seconds. */
    val healthPeriodMs: Long = 30_000,
    val retry: RetryPolicy = RetryPolicy(),
) {
    /** Never prints the password or key material. */
    override fun toString(): String =
        "CnpgConfig(host=$host, port=$port, database=$database, user=$user, " +
            "password=${if (password != null || passwordFile != null) "<redacted>" else "<none>"}, " +
            "sslmode=verify-full, sslRootCert=$sslRootCert, sslCert=$sslCert, sslKey=$sslKey, " +
            "applicationName=$applicationName)"

    /** Throws [ConfigError] listing every problem. */
    fun validate() {
        val p = mutableListOf<String>()
        if (sslMode != null && sslMode != "" && sslMode != "verify-full") {
            p += "sslmode \"$sslMode\" is refused; only verify-full is accepted"
        }
        if (host.isEmpty()) {
            p += "host is required"
        } else if (!HOST.matches(host)) {
            p += "host \"$host\": exactly one host name is accepted (the cluster's rw Service)"
        }
        if (port !in 1..65535) p += "port must be 1-65535"
        if (database.isEmpty()) p += "database is required"
        if (user.isEmpty()) p += "user is required"
        if (sslRootCert.isEmpty()) {
            p += "the server CA file is required (PGSSLROOTCERT): verify-full has nothing to verify against without it"
        }
        if ((sslCert.isNullOrEmpty()) != (sslKey.isNullOrEmpty())) p += "client certificate and key go together"
        if (poolMax < 1) p += "poolMax must be at least 1"
        if (poolMin !in 0..poolMax) p += "poolMin must be between 0 and poolMax"
        if (connectTimeoutMs <= 0) p += "connectTimeoutMs must be positive"
        if (statementTimeoutMs < 0 || idleInTxTimeoutMs < 0) p += "timeouts must not be negative"
        if (maxConnLifetimeMs < MIN_LIFETIME_MS) {
            p += "maxConnLifetimeMs must be at least ${MIN_LIFETIME_MS}ms (HikariCP does not retire connections sooner)"
        }
        if (maxConnIdleMs < MIN_IDLE_MS) {
            p += "maxConnIdleMs must be at least ${MIN_IDLE_MS}ms (HikariCP's floor)"
        }
        if (healthPeriodMs != 0L && healthPeriodMs < MIN_IDLE_MS) {
            p += "healthPeriodMs must be 0 (off) or at least ${MIN_IDLE_MS}ms (HikariCP's floor)"
        }
        val r = retry
        if (!(r.attempts >= 1 && r.initialDelayMs > 0 && r.maxDelayMs >= r.initialDelayMs && r.budgetMs > 0)) {
            p += "retry: attempts>=1, 0<initialDelayMs<=maxDelayMs, budgetMs>0"
        }
        if (p.isNotEmpty()) throw ConfigError(p)
    }

    companion object {
        /** HikariCP clamps a lifetime below 30s back to 30 minutes; refuse it instead of surprising. */
        const val MIN_LIFETIME_MS = 30_000L
        const val MIN_IDLE_MS = 10_000L

        // One DNS name, or an IPv4/IPv6 literal. No `/ ? # & , @`, no spaces: nothing that could
        // smuggle a second host or a parameter into the JDBC URL.
        private val HOST = Regex("[A-Za-z0-9._:-]+")

        /**
         * Builds a configuration from the contract's environment: the libpq names the
         * `cnpg-client` Helm library chart exports, plus the `CNPG_CLIENT_*` tuning
         * variables. The result is validated.
         */
        @JvmStatic
        @JvmOverloads
        fun fromEnv(env: Map<String, String?> = System.getenv()): CnpgConfig {
            val problems = mutableListOf<String>()
            fun get(k: String): String? = env[k]?.takeIf { it.isNotEmpty() }

            fun <T> parse(k: String, fallback: T, f: (String) -> T): T {
                val v = get(k) ?: return fallback
                return try {
                    f(v)
                } catch (e: IllegalArgumentException) {
                    problems += "$k=\"$v\": ${e.message}"
                    fallback
                }
            }

            fun int(s: String): Int = s.takeIf { it.matches(Regex("\\d{1,9}")) }?.toInt()
                ?: throw IllegalArgumentException("not a whole number")

            val d = CnpgConfig(host = "", database = "", user = "", sslRootCert = "")
            val c = CnpgConfig(
                host = get("PGHOST") ?: "",
                database = get("PGDATABASE") ?: "",
                user = get("PGUSER") ?: "",
                sslRootCert = get("PGSSLROOTCERT") ?: "",
                sslCert = get("PGSSLCERT"),
                sslKey = get("PGSSLKEY"),
                sslMode = get("PGSSLMODE"),
                password = get("PGPASSWORD"),
                passwordFile = get("CNPG_CLIENT_PASSWORD_FILE"),
                applicationName = get("PGAPPNAME") ?: d.applicationName,
                port = parse("PGPORT", d.port, ::int),
                connectTimeoutMs = parse("PGCONNECT_TIMEOUT", d.connectTimeoutMs) {
                    val n = int(it)
                    require(n > 0) { "must be positive seconds" }
                    n * 1000L
                },
                statementTimeoutMs = parse("CNPG_CLIENT_STATEMENT_TIMEOUT", d.statementTimeoutMs, ::parseDuration),
                idleInTxTimeoutMs = parse("CNPG_CLIENT_IDLE_TX_TIMEOUT", d.idleInTxTimeoutMs, ::parseDuration),
                poolMax = parse("CNPG_CLIENT_POOL_MAX", d.poolMax, ::int),
                poolMin = parse("CNPG_CLIENT_POOL_MIN", d.poolMin, ::int),
                maxConnLifetimeMs = parse("CNPG_CLIENT_CONN_MAX_LIFETIME", d.maxConnLifetimeMs, ::parseDuration),
                maxConnIdleMs = parse("CNPG_CLIENT_CONN_MAX_IDLE", d.maxConnIdleMs, ::parseDuration),
                healthPeriodMs = parse("CNPG_CLIENT_HEALTH_PERIOD", d.healthPeriodMs, ::parseDuration),
                retry = RetryPolicy(
                    attempts = parse("CNPG_CLIENT_RETRY_ATTEMPTS", d.retry.attempts, ::int),
                    maxDelayMs = parse("CNPG_CLIENT_RETRY_MAX_DELAY", d.retry.maxDelayMs, ::parseDuration),
                    budgetMs = parse("CNPG_CLIENT_RETRY_BUDGET", d.retry.budgetMs, ::parseDuration),
                ),
            )
            if (problems.isNotEmpty()) throw ConfigError(problems)
            c.validate()
            return c
        }
    }
}

private val DURATION = Regex("^(\\d+(?:\\.\\d+)?)(ms|s|m|h)$")

/** "500ms", "30s", "5m", "1h" in milliseconds; a plain "0" is accepted too. */
fun parseDuration(text: String): Long {
    if (text == "0") return 0
    val m = DURATION.matchEntire(text)
        ?: throw IllegalArgumentException("\"$text\" is not a duration (use 500ms, 30s, 5m, 1h)")
    val unit = when (m.groupValues[2]) {
        "ms" -> 1L
        "s" -> 1_000L
        "m" -> 60_000L
        else -> 3_600_000L
    }
    return Math.round(m.groupValues[1].toDouble() * unit)
}

internal fun programName(): String {
    val cmd = System.getProperty("sun.java.command")?.trim()?.substringBefore(' ').orEmpty()
    val name = cmd.substringAfterLast('/').removeSuffix(".jar").ifEmpty { "jvm" }
    return name.take(63) // NAMEDATALEN-1
}

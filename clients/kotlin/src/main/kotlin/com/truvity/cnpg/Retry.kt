package com.truvity.cnpg

import java.io.EOFException
import java.net.ConnectException
import java.net.SocketException
import java.net.SocketTimeoutException
import java.nio.channels.ClosedChannelException
import java.security.GeneralSecurityException
import java.sql.SQLException
import java.sql.SQLTransientConnectionException
import java.util.concurrent.ThreadLocalRandom
import javax.net.ssl.SSLException

/** Exponential backoff with full jitter. `attempt` is 1 for the first retry. */
fun backoffMs(p: RetryPolicy, attempt: Int): Long {
    val shift = minOf(attempt - 1, 30)
    val ceil = minOf(p.maxDelayMs, p.initialDelayMs shl shift)
    return ThreadLocalRandom.current().nextLong(ceil + 1)
}

/**
 * Runs [fn], repeating it while it fails with a retryable error (see
 * [isRetryable]). [fn] must be safe to run again: a statement that may have
 * committed before the connection broke is the caller's to reason about,
 * which is why the unit of retry is the caller's whole function.
 *
 * Interrupting the thread stops the retry and rethrows the last error.
 */
fun <T> retry(policy: RetryPolicy, fn: () -> T): T {
    val start = System.nanoTime()
    var attempt = 1
    while (true) {
        try {
            return fn()
        } catch (e: Exception) {
            if (Thread.currentThread().isInterrupted || !isRetryable(e) || attempt >= policy.attempts) throw e
            val delay = backoffMs(policy, attempt)
            val waited = (System.nanoTime() - start) / 1_000_000
            if (waited + delay > policy.budgetMs) throw e
            try {
                Thread.sleep(delay)
            } catch (_: InterruptedException) {
                Thread.currentThread().interrupt()
                throw e
            }
            attempt++
        }
    }
}

private val RETRYABLE_STATES = setOf(
    "57P01", // admin_shutdown
    "57P02", // crash_shutdown
    "57P03", // cannot_connect_now
    "25006", // read_only_sql_transaction: a demoted primary still answering
    "53300", // too_many_connections: a new primary filling
)

// pgjdbc raises these as plain SQLSTATE 08xxx / 08006 with no TLS exception in the chain,
// which would read as a lost connection. A second try cannot fix any of them.
private val NEVER_MESSAGES = listOf(
    "could not be verified by hostnameverifier", // the certificate does not name the host
    "The server does not support SSL",
)

/**
 * Reports whether [err] means the connection (not the statement) failed: the
 * server went away, was demoted, or is not accepting yet. Those are what a
 * CloudNativePG switchover or crash looks like to a client.
 *
 * The whole cause chain is read, because the driver and the pool wrap: pgjdbc
 * reports a refused certificate as SQLSTATE 08001, with the TLS error as the
 * cause, and HikariCP wraps the last connection failure in a timeout.
 *
 * Never retryable: certificate, host name and TLS failures anywhere in the chain,
 * authentication and permission errors, constraint and syntax errors, a
 * statement timeout (57014) and a serialization failure (40001, 40P01: the
 * caller retries its transaction).
 */
fun isRetryable(err: Throwable): Boolean {
    val chain = generateSequence<Throwable>(err) { it.cause }.take(16).toList()
    if (chain.any { it is SSLException || it is GeneralSecurityException }) return false
    if (chain.any { t -> NEVER_MESSAGES.any { t.message?.contains(it) == true } }) return false
    for (t in chain) {
        val state = (t as? SQLException)?.sqlState
        if (state != null && state.length == 5) {
            return state.startsWith("08") || state in RETRYABLE_STATES
        }
    }
    for (t in chain) {
        if (t is ConnectException || t is SocketException || t is SocketTimeoutException ||
            t is EOFException || t is ClosedChannelException
        ) {
            return true
        }
    }
    // The pool could not hand out a connection: it was trying to open one.
    return chain.any { it is SQLTransientConnectionException }
}

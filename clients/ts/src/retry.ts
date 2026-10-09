import type { RetryPolicy } from "./config.js";

const sleep = (ms: number, signal?: AbortSignal) =>
  new Promise<void>((resolve) => {
    const t = setTimeout(done, ms);
    signal?.addEventListener("abort", done, { once: true });
    function done() {
      clearTimeout(t);
      signal?.removeEventListener("abort", done);
      resolve();
    }
  });

/** Exponential backoff with full jitter. `attempt` is 1 for the first retry. */
export function backoffMs(p: RetryPolicy, attempt: number): number {
  const ceil = Math.min(p.maxDelayMs, p.initialDelayMs * 2 ** (attempt - 1));
  return Math.floor(Math.random() * (ceil + 1));
}

/**
 * Runs `fn`, repeating it while it fails with a retryable error (see
 * {@link isRetryable}). `fn` must be safe to run again: a statement that may
 * have committed before the connection broke is the caller's to reason about,
 * which is why the unit of retry is the caller's whole function.
 */
export async function retry<T>(policy: RetryPolicy, fn: () => Promise<T>, signal?: AbortSignal): Promise<T> {
  const start = Date.now();
  for (let attempt = 1; ; attempt++) {
    try {
      return await fn();
    } catch (err) {
      if (signal?.aborted || !isRetryable(err) || attempt >= policy.attempts) throw err;
      const delay = backoffMs(policy, attempt);
      if (Date.now() - start + delay > policy.budgetMs) throw err;
      policy.onRetry?.(attempt, err as Error, delay);
      await sleep(delay, signal);
      if (signal?.aborted) throw err;
    }
  }
}

const NEVER_CODES = new Set([
  // certificate verification and TLS handshake rejection: a second try cannot fix them
  "DEPTH_ZERO_SELF_SIGNED_CERT",
  "SELF_SIGNED_CERT_IN_CHAIN",
  "UNABLE_TO_VERIFY_LEAF_SIGNATURE",
  "UNABLE_TO_GET_ISSUER_CERT_LOCALLY",
  "CERT_HAS_EXPIRED",
  "CERT_NOT_YET_VALID",
  "ERR_TLS_CERT_ALTNAME_INVALID",
  "ERR_TLS_INVALID_PROTOCOL_VERSION",
]);
const NETWORK_CODES = new Set([
  "ECONNRESET",
  "ECONNREFUSED",
  "EPIPE",
  "ETIMEDOUT",
  "EAI_AGAIN",
  "ECONNABORTED",
  "EHOSTUNREACH",
]);
const MESSAGES = [
  "Connection terminated",
  "timeout exceeded when trying to connect",
  "Client has encountered a connection error",
  "Client was closed and is not queryable",
  "the database system is starting up",
  "the database system is shutting down",
];

/**
 * Reports whether `err` means the connection (not the statement) failed: the
 * server went away, was demoted, or is not accepting yet. Those are what a
 * CloudNativePG switchover or crash looks like to a client.
 *
 * Never retryable: authentication and permission errors, constraint and syntax
 * errors, a statement timeout (57014), a serialization failure (40001, 40P01:
 * the caller retries its transaction) and certificate verification failures.
 */
export function isRetryable(err: unknown): boolean {
  if (!(err instanceof Error)) return false;
  // The cause chain: a wrapper (such as the health check's) hides the pg error's code.
  const chain: Error[] = [];
  for (let e: unknown = err; e instanceof Error && chain.length < 8; e = e.cause) chain.push(e);
  if (chain.some(neverRetryable)) return false;
  return chain.some(retryableOne);
}

/** One try of the first connection ran past its own bound; the caller's signal is separate. */
export class AttemptTimeoutError extends Error {
  constructor(what: string, ms: number) {
    super(`cnpg-client: ${what}: attempt timed out after ${ms}ms`);
    this.name = "AttemptTimeoutError";
  }
}

function neverRetryable(err: Error): boolean {
  const code = (err as { code?: unknown }).code;
  return typeof code === "string" && (NEVER_CODES.has(code) || code.startsWith("ERR_SSL_"));
}

function retryableOne(err: Error): boolean {
  if (err instanceof AttemptTimeoutError) return true;
  const code = (err as { code?: unknown }).code;
  if (typeof code === "string") {
    if (NETWORK_CODES.has(code)) return true;
    if (/^[0-9A-Z]{5}$/.test(code)) return retryableSqlState(code);
  }
  return MESSAGES.some((m) => err.message.includes(m));
}

function retryableSqlState(code: string): boolean {
  if (code.startsWith("08")) return true; // connection exception
  // admin shutdown, crash shutdown, cannot connect now; read_only_sql_transaction
  // (a demoted primary still answering); too_many_connections (a new primary filling)
  return ["57P01", "57P02", "57P03", "25006", "53300"].includes(code);
}

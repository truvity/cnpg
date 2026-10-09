import { readFileSync } from "node:fs";
import { isIP } from "node:net";
import type { Span, Tracer } from "@opentelemetry/api";
import pg from "pg";
import { type CnpgConfig, redactConfig, validateConfig } from "./config.js";
import { AttemptTimeoutError, retry } from "./retry.js";

/** Bounds {@link CnpgPool.health}. */
export const HEALTH_TIMEOUT_MS = 2_000;

/** Bounds one try of the first connection in {@link CnpgPool.create}: cold DNS, TLS and authentication. */
export const STARTUP_ATTEMPT_TIMEOUT_MS = 5_000;

/** Open, idle and waiting connections, for a metrics exporter. */
export interface PoolStats {
  total: number;
  idle: number;
  waiting: number;
}

function readPassword(c: CnpgConfig): string | undefined {
  if (c.passwordFile) return readFileSync(c.passwordFile, "utf8").replace(/[\r\n]+$/, "");
  return c.password;
}

/** Read NOW, for one connection: the CA, the client certificate and key. */
function readSsl(c: CnpgConfig): pg.ConnectionConfig["ssl"] {
  return {
    ca: readFileSync(c.sslRootCert),
    ...(c.sslCert && c.sslKey ? { cert: readFileSync(c.sslCert), key: readFileSync(c.sslKey) } : {}),
    rejectUnauthorized: true,
    minVersion: "TLSv1.2",
    // verify-full: the certificate must name the host that was dialled.
    ...(isIP(c.host) ? {} : { servername: c.host }),
  };
}

/**
 * `pg.PoolConfig` for the contract. The `Client` it carries re-reads the
 * certificate, key, CA and password files for every new physical connection,
 * so a renewed Secret reaches the next connection without a restart. Pass it
 * as TypeORM's `extra`, or to `new pg.Pool(...)`; prefer {@link CnpgPool}.
 */
export function pgPoolConfig(config: CnpgConfig): pg.PoolConfig {
  validateConfig(config);
  const c = { ...config };

  class ReloadingClient extends pg.Client {
    #loadError: Error | undefined;

    constructor(options: pg.ClientConfig) {
      let fresh: Partial<pg.ClientConfig> = {};
      let loadError: Error | undefined;
      try {
        fresh = { ssl: readSsl(c), password: readPassword(c) };
      } catch (e) {
        // pg-pool builds clients where a throw would be an uncaught exception;
        // fail this connection's connect() instead.
        loadError = new Error(`cnpg-client: reading TLS or password files: ${(e as Error).message}`);
      }
      super({ ...options, ...fresh });
      this.#loadError = loadError;
    }

    // biome-ignore lint/suspicious/noExplicitAny: pg's connect has promise and callback overloads
    connect(cb?: any): any {
      if (this.#loadError) {
        const err = this.#loadError;
        if (cb) {
          process.nextTick(cb, err);
          return undefined;
        }
        return Promise.reject(err);
      }
      return super.connect(cb);
    }
  }

  return {
    host: c.host,
    port: c.port,
    database: c.database,
    user: c.user,
    application_name: c.applicationName,
    connectionTimeoutMillis: c.connectTimeoutMs,
    statement_timeout: c.statementTimeoutMs,
    idle_in_transaction_session_timeout: c.idleInTxTimeoutMs,
    max: c.poolMax,
    min: c.poolMin,
    idleTimeoutMillis: c.maxConnIdleMs,
    maxLifetimeSeconds: c.maxConnLifetimeMs / 1000,
    allowExitOnIdle: false,
    Client: ReloadingClient,
  } as unknown as pg.PoolConfig;
}

function operation(text: string): string {
  const m = /^[\s(]*([A-Za-z]{1,16})\b/.exec(text);
  return m ? (m[1] as string).toUpperCase() : "QUERY";
}

export interface CnpgPoolOptions {
  /** OpenTelemetry tracer; spans only for {@link CnpgPool.query}. */
  tracer?: Tracer;
  /** Abandons the first connection (and its retries) when aborted; an abort is never retried. */
  signal?: AbortSignal;
}

/**
 * A node-postgres pool with the contract's behaviour: verify-full against the
 * given CA, certificate/key/password reload per connection, bounded
 * connection lifetime, failover-aware retry and a health check.
 */
export class CnpgPool {
  readonly pool: pg.Pool;

  private constructor(
    readonly config: CnpgConfig,
    private readonly tracer?: Tracer,
  ) {
    this.pool = new pg.Pool(pgPoolConfig(config));
    // An error on an IDLE pooled client (the primary went away) is emitted on
    // the pool; with no listener it is thrown and takes the process down.
    this.pool.on("error", (err) => config.onError?.(err));
  }

  /** Validates, builds the pool and proves the first connection under the retry policy. */
  static async create(config: CnpgConfig, options: CnpgPoolOptions = {}): Promise<CnpgPool> {
    validateConfig(config);
    const p = new CnpgPool(config, options.tracer);
    try {
      await retry(config.retry, () => p.startupProbe(), options.signal);
    } catch (e) {
      await p.end().catch(() => undefined);
      throw new Error(
        `cnpg-client: first connection to ${config.host}:${config.port}: ${(e as Error).message}`,
        {
          cause: e,
        },
      );
    }
    return p;
  }

  /** One try of the first connection: a raw `select 1` bounded by {@link STARTUP_ATTEMPT_TIMEOUT_MS}. */
  private async startupProbe(): Promise<void> {
    let timer: NodeJS.Timeout | undefined;
    const deadline = new Promise<never>((_, reject) => {
      timer = setTimeout(
        () => reject(new AttemptTimeoutError("first connection", STARTUP_ATTEMPT_TIMEOUT_MS)),
        STARTUP_ATTEMPT_TIMEOUT_MS,
      );
    });
    try {
      await Promise.race([
        this.pool.query({ text: "select 1", query_timeout: STARTUP_ATTEMPT_TIMEOUT_MS } as pg.QueryConfig),
        deadline,
      ]);
    } finally {
      clearTimeout(timer);
    }
  }

  /** Runs one statement; traced when a tracer was given. */
  async query<R extends pg.QueryResultRow = pg.QueryResultRow>(
    text: string,
    values?: unknown[],
  ): Promise<pg.QueryResult<R>> {
    const tracer = this.tracer;
    if (!tracer) return this.pool.query<R>(text, values);
    const op = operation(text);
    return tracer.startActiveSpan(
      `${op} ${this.config.database}`,
      {
        kind: 2, // SpanKind.CLIENT, without a runtime import of the optional API
        attributes: {
          "db.system.name": "postgresql",
          "db.namespace": this.config.database,
          "db.operation.name": op,
          "db.query.text": text, // placeholders only; the arguments are user data
          "server.address": this.config.host,
          "server.port": this.config.port,
        },
      },
      async (span: Span) => {
        try {
          const res = await this.pool.query<R>(text, values);
          span.setAttribute("db.response.returned_rows", res.rowCount ?? 0);
          return res;
        } catch (e) {
          span.recordException(e as Error);
          span.setStatus({ code: 2, message: "query failed" }); // SpanStatusCode.ERROR
          throw e;
        } finally {
          span.end();
        }
      },
    );
  }

  /** A pooled client for transactions; statements on it are not traced. Release it. */
  connect(): Promise<pg.PoolClient> {
    return this.pool.connect();
  }

  /** Runs `fn` under the retry policy; see {@link retry}. */
  do<T>(fn: () => Promise<T>, signal?: AbortSignal): Promise<T> {
    return retry(this.config.retry, fn, signal);
  }

  /**
   * SELECT 1 on a pooled connection, bounded by {@link HEALTH_TIMEOUT_MS}. It
   * exercises the real path (Service, TLS, authentication), so it suits a
   * readiness probe. Rejects when unhealthy.
   */
  async health(): Promise<void> {
    let timer: NodeJS.Timeout | undefined;
    const deadline = new Promise<never>((_, reject) => {
      timer = setTimeout(() => reject(new Error("cnpg-client: health: timed out")), HEALTH_TIMEOUT_MS);
    });
    try {
      await Promise.race([
        this.pool.query({ text: "select 1", query_timeout: HEALTH_TIMEOUT_MS } as pg.QueryConfig),
        deadline,
      ]);
    } catch (e) {
      throw new Error(`cnpg-client: health: ${(e as Error).message}`, { cause: e });
    } finally {
      clearTimeout(timer);
    }
  }

  stats(): PoolStats {
    return { total: this.pool.totalCount, idle: this.pool.idleCount, waiting: this.pool.waitingCount };
  }

  /** Closes every connection. */
  end(): Promise<void> {
    return this.pool.end();
  }

  /** Safe to log: no password, no key material. */
  toJSON(): Record<string, unknown> {
    return redactConfig(this.config);
  }
}

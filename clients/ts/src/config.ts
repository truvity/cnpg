import { basename } from "node:path";

/** Bounds the retry of connection-class failures. */
export interface RetryPolicy {
  /** Total tries, including the first. */
  attempts: number;
  /** Ceiling of the first sleep, ms. */
  initialDelayMs: number;
  /** Ceiling of any sleep, ms. */
  maxDelayMs: number;
  /** Total time that may be spent waiting, ms. */
  budgetMs: number;
}

export interface CnpgConfig {
  host: string;
  port: number;
  database: string;
  user: string;
  /** For a scram role. `passwordFile` wins and is read again for every new connection. */
  password?: string;
  passwordFile?: string;
  /** Server CA file. Required: this file is the only trust root. */
  sslRootCert: string;
  /** Client certificate and key of a certificate role; both or neither. */
  sslCert?: string;
  sslKey?: string;
  /** Exists to be refused: undefined or "verify-full". */
  sslMode?: string;
  applicationName: string;
  connectTimeoutMs: number;
  /** 0 disables. */
  statementTimeoutMs: number;
  /** 0 disables. */
  idleInTxTimeoutMs: number;
  poolMax: number;
  poolMin: number;
  /** Keep below the client certificate's life. */
  maxConnLifetimeMs: number;
  maxConnIdleMs: number;
  retry: RetryPolicy;
  /** Called for errors on idle pooled connections (never thrown: an unhandled pool error would kill the process). */
  onError?: (err: Error) => void;
}

export class ConfigError extends Error {
  constructor(readonly problems: string[]) {
    super(`cnpg-client: invalid configuration:\n  - ${problems.join("\n  - ")}`);
    this.name = "ConfigError";
  }
}

export const defaultRetryPolicy = (): RetryPolicy => ({
  attempts: 5,
  initialDelayMs: 200,
  maxDelayMs: 5_000,
  budgetMs: 30_000,
});

/** The contract's defaults; fill host, database, user and sslRootCert. */
export function defaultConfig(): Pick<
  CnpgConfig,
  | "port"
  | "applicationName"
  | "connectTimeoutMs"
  | "statementTimeoutMs"
  | "idleInTxTimeoutMs"
  | "poolMax"
  | "poolMin"
  | "maxConnLifetimeMs"
  | "maxConnIdleMs"
  | "retry"
> {
  return {
    port: 5432,
    applicationName: programName(),
    connectTimeoutMs: 5_000,
    statementTimeoutMs: 30_000,
    idleInTxTimeoutMs: 60_000,
    poolMax: 10,
    poolMin: 0,
    maxConnLifetimeMs: 30 * 60_000,
    maxConnIdleMs: 5 * 60_000,
    retry: defaultRetryPolicy(),
  };
}

function programName(): string {
  const name = basename(process.argv[1] ?? "node");
  return name.slice(0, 63); // NAMEDATALEN-1
}

/** Reports every problem, not just the first. */
export function validateConfig(c: CnpgConfig): void {
  const p: string[] = [];
  if (c.sslMode !== undefined && c.sslMode !== "" && c.sslMode !== "verify-full") {
    p.push(`sslmode "${c.sslMode}" is refused; only verify-full is accepted`);
  }
  if (!c.host) p.push("host is required");
  if (/[, ]/.test(c.host ?? ""))
    p.push(`host "${c.host}": exactly one host name is accepted (the cluster's rw Service)`);
  if (!Number.isInteger(c.port) || c.port < 1 || c.port > 65535) p.push("port must be 1-65535");
  if (!c.database) p.push("database is required");
  if (!c.user) p.push("user is required");
  if (!c.sslRootCert) {
    p.push(
      "the server CA file is required (PGSSLROOTCERT): verify-full has nothing to verify against without it",
    );
  }
  if (Boolean(c.sslCert) !== Boolean(c.sslKey)) p.push("client certificate and key go together");
  if (!(c.poolMax >= 1)) p.push("poolMax must be at least 1");
  if (!(c.poolMin >= 0 && c.poolMin <= c.poolMax)) p.push("poolMin must be between 0 and poolMax");
  if (!(c.connectTimeoutMs > 0)) p.push("connectTimeoutMs must be positive");
  if (c.statementTimeoutMs < 0 || c.idleInTxTimeoutMs < 0) p.push("timeouts must not be negative");
  if (!(c.maxConnLifetimeMs > 0 && c.maxConnIdleMs > 0))
    p.push("maxConnLifetimeMs and maxConnIdleMs must be positive");
  const r = c.retry;
  if (!(r.attempts >= 1 && r.initialDelayMs > 0 && r.maxDelayMs >= r.initialDelayMs && r.budgetMs > 0)) {
    p.push("retry: attempts>=1, 0<initialDelayMs<=maxDelayMs, budgetMs>0");
  }
  if (p.length > 0) throw new ConfigError(p);
}

const DURATION = /^(\d+(?:\.\d+)?)(ms|s|m|h)$/;
const UNIT_MS: Record<string, number> = { ms: 1, s: 1_000, m: 60_000, h: 3_600_000 };

/** "500ms", "30s", "5m", "1h" (a "0" is accepted too). */
export function parseDuration(text: string): number {
  if (text === "0") return 0;
  const m = DURATION.exec(text);
  if (!m) throw new Error(`"${text}" is not a duration (use 500ms, 30s, 5m, 1h)`);
  return Math.round(Number(m[1]) * (UNIT_MS[m[2] as string] as number));
}

/**
 * Builds a configuration from the contract's environment: the libpq names the
 * cnpg-client Helm library chart exports, plus the CNPG_CLIENT_* tuning
 * variables. The result is validated.
 */
export function configFromEnv(env: Record<string, string | undefined> = process.env): CnpgConfig {
  const problems: string[] = [];
  const get = (k: string) => (env[k] === "" ? undefined : env[k]);
  const num = (k: string, into: (n: number) => void, parse: (s: string) => number) => {
    const v = get(k);
    if (v === undefined) return;
    try {
      const n = parse(v);
      if (!Number.isFinite(n)) throw new Error("not a number");
      into(n);
    } catch (e) {
      problems.push(`${k}="${v}": ${(e as Error).message}`);
    }
  };
  const int = (s: string) => {
    if (!/^\d+$/.test(s)) throw new Error("not a whole number");
    return Number(s);
  };

  const c: CnpgConfig = {
    ...defaultConfig(),
    host: get("PGHOST") ?? "",
    database: get("PGDATABASE") ?? "",
    user: get("PGUSER") ?? "",
    sslRootCert: get("PGSSLROOTCERT") ?? "",
    sslCert: get("PGSSLCERT"),
    sslKey: get("PGSSLKEY"),
    sslMode: get("PGSSLMODE"),
    password: get("PGPASSWORD"),
    passwordFile: get("CNPG_CLIENT_PASSWORD_FILE"),
  };
  const app = get("PGAPPNAME");
  if (app) c.applicationName = app;

  num("PGPORT", (n) => (c.port = n), int);
  num(
    "PGCONNECT_TIMEOUT",
    (n) => (c.connectTimeoutMs = n * 1000),
    (s) => {
      const n = int(s);
      if (n <= 0) throw new Error("must be positive seconds");
      return n;
    },
  );
  num("CNPG_CLIENT_STATEMENT_TIMEOUT", (n) => (c.statementTimeoutMs = n), parseDuration);
  num("CNPG_CLIENT_IDLE_TX_TIMEOUT", (n) => (c.idleInTxTimeoutMs = n), parseDuration);
  num("CNPG_CLIENT_POOL_MAX", (n) => (c.poolMax = n), int);
  num("CNPG_CLIENT_POOL_MIN", (n) => (c.poolMin = n), int);
  num("CNPG_CLIENT_CONN_MAX_LIFETIME", (n) => (c.maxConnLifetimeMs = n), parseDuration);
  num("CNPG_CLIENT_CONN_MAX_IDLE", (n) => (c.maxConnIdleMs = n), parseDuration);
  num("CNPG_CLIENT_RETRY_ATTEMPTS", (n) => (c.retry.attempts = n), int);
  num("CNPG_CLIENT_RETRY_MAX_DELAY", (n) => (c.retry.maxDelayMs = n), parseDuration);
  num("CNPG_CLIENT_RETRY_BUDGET", (n) => (c.retry.budgetMs = n), parseDuration);

  if (problems.length > 0) throw new ConfigError(problems);
  validateConfig(c);
  return c;
}

/** A copy that is safe to log or serialise: no password, no key material. */
export function redactConfig(c: CnpgConfig): Record<string, unknown> {
  return {
    host: c.host,
    port: c.port,
    database: c.database,
    user: c.user,
    password: c.password || c.passwordFile ? "<redacted>" : "<none>",
    sslmode: "verify-full",
    sslRootCert: c.sslRootCert,
    sslCert: c.sslCert,
    sslKey: c.sslKey,
    applicationName: c.applicationName,
  };
}

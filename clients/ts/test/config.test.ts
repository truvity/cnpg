import { describe, expect, it } from "vitest";
import {
  type CnpgConfig,
  ConfigError,
  configFromEnv,
  defaultConfig,
  parseDuration,
  pgPoolConfig,
  redactConfig,
  validateConfig,
} from "../src/index.js";

// The environment the cnpg-client chart's `env` helper renders.
const chartEnv = (): Record<string, string> => ({
  PGHOST: "pg-rw.shop.svc",
  PGPORT: "5432",
  PGDATABASE: "app",
  PGUSER: "app_role",
  PGSSLMODE: "verify-full",
  PGSSLROOTCERT: "/var/run/cnpg-client/ca.crt",
  PGSSLCERT: "/var/run/cnpg-client/tls.crt",
  PGSSLKEY: "/var/run/cnpg-client/tls.key",
});

describe("configFromEnv", () => {
  it("reads the chart's environment and applies the contract's defaults", () => {
    const c = configFromEnv(chartEnv());
    expect(c).toMatchObject({
      host: "pg-rw.shop.svc",
      port: 5432,
      database: "app",
      user: "app_role",
      sslRootCert: "/var/run/cnpg-client/ca.crt",
      connectTimeoutMs: 5_000,
      statementTimeoutMs: 30_000,
      idleInTxTimeoutMs: 60_000,
      poolMax: 10,
      maxConnLifetimeMs: 30 * 60_000,
      maxConnIdleMs: 5 * 60_000,
      retry: { attempts: 5, initialDelayMs: 200, maxDelayMs: 5_000, budgetMs: 30_000 },
    });
    expect(c.applicationName).not.toBe("");
  });

  it("applies overrides", () => {
    const c = configFromEnv({
      ...chartEnv(),
      PGAPPNAME: "urls",
      PGCONNECT_TIMEOUT: "3",
      CNPG_CLIENT_STATEMENT_TIMEOUT: "500ms",
      CNPG_CLIENT_IDLE_TX_TIMEOUT: "0",
      CNPG_CLIENT_POOL_MAX: "4",
      CNPG_CLIENT_POOL_MIN: "1",
      CNPG_CLIENT_CONN_MAX_LIFETIME: "10m",
      CNPG_CLIENT_RETRY_ATTEMPTS: "7",
      CNPG_CLIENT_RETRY_MAX_DELAY: "1s",
      CNPG_CLIENT_RETRY_BUDGET: "9s",
      CNPG_CLIENT_PASSWORD_FILE: "/secrets/pw",
    });
    expect(c).toMatchObject({
      applicationName: "urls",
      connectTimeoutMs: 3_000,
      statementTimeoutMs: 500,
      idleInTxTimeoutMs: 0,
      poolMax: 4,
      poolMin: 1,
      maxConnLifetimeMs: 600_000,
      passwordFile: "/secrets/pw",
      retry: { attempts: 7, initialDelayMs: 200, maxDelayMs: 1_000, budgetMs: 9_000 },
    });
  });

  it.each(["disable", "allow", "prefer", "require", "verify-ca"])("refuses PGSSLMODE=%s", (mode) => {
    expect(() => configFromEnv({ ...chartEnv(), PGSSLMODE: mode })).toThrow(/only verify-full is accepted/);
  });

  it("reports every bad number", () => {
    try {
      configFromEnv({
        ...chartEnv(),
        PGPORT: "http",
        CNPG_CLIENT_POOL_MAX: "many",
        CNPG_CLIENT_CONN_MAX_LIFETIME: "soon",
      });
      expect.unreachable();
    } catch (e) {
      expect(e).toBeInstanceOf(ConfigError);
      for (const name of ["PGPORT", "CNPG_CLIENT_POOL_MAX", "CNPG_CLIENT_CONN_MAX_LIFETIME"]) {
        expect((e as Error).message).toContain(name);
      }
    }
  });
});

describe("validateConfig", () => {
  const base = (): CnpgConfig => ({
    ...defaultConfig(),
    host: "h",
    database: "d",
    user: "u",
    sslRootCert: "/ca",
  });

  it("requires the CA and pairs the client files", () => {
    expect(() => validateConfig(base())).not.toThrow();
    expect(() => validateConfig({ ...base(), sslRootCert: "" })).toThrow(/server CA file is required/);
    expect(() => validateConfig({ ...base(), sslCert: "/crt" })).toThrow(/go together/);
    expect(() => validateConfig({ ...base(), host: "a,b" })).toThrow(/exactly one host/);
  });

  it("reports every problem at once", () => {
    const e = (() => {
      try {
        validateConfig({ ...base(), host: "", database: "", user: "", sslRootCert: "", poolMax: 0 });
      } catch (err) {
        return err as ConfigError;
      }
    })();
    expect(e?.problems.length).toBeGreaterThanOrEqual(5);
  });

  it("never lets pgPoolConfig build a pool from an invalid configuration", () => {
    expect(() => pgPoolConfig({ ...base(), sslRootCert: "" })).toThrow(ConfigError);
  });
});

describe("parseDuration", () => {
  it("parses ms, s, m, h", () => {
    expect(parseDuration("500ms")).toBe(500);
    expect(parseDuration("30s")).toBe(30_000);
    expect(parseDuration("1.5s")).toBe(1_500);
    expect(parseDuration("5m")).toBe(300_000);
    expect(parseDuration("1h")).toBe(3_600_000);
    expect(parseDuration("0")).toBe(0);
    expect(() => parseDuration("30")).toThrow();
    expect(() => parseDuration("-1s")).toThrow();
  });
});

describe("redaction", () => {
  it("never carries the password or key material", () => {
    const secret = "s3cr3t-value";
    const c: CnpgConfig = {
      ...defaultConfig(),
      host: "h",
      database: "d",
      user: "u",
      sslRootCert: "/ca",
      password: secret,
    };
    expect(JSON.stringify(redactConfig(c))).not.toContain(secret);
    expect(JSON.stringify(redactConfig(c))).toContain("redacted");
  });
});

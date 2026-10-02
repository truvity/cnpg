// The conformance suite: every case in clients/conformance/cases.txt, against a
// real PostgreSQL that serves TLS (clients/conformance/pg-tls.sh). The `it`
// names are the case names; the CI guard fails unless each one ran and
// passed. Without CNPG_CLIENTS_PG_HOST the suite skips, or, with
// CNPG_CLIENTS_PG=required, fails.

import { copyFileSync, mkdtempSync, writeFileSync } from "node:fs";
import net from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  BasicTracerProvider,
  InMemorySpanExporter,
  SimpleSpanProcessor,
} from "@opentelemetry/sdk-trace-base";
import { afterAll, describe, expect, it } from "vitest";
import {
  type CnpgConfig,
  CnpgPool,
  configFromEnv,
  defaultConfig,
  HEALTH_TIMEOUT_MS,
  isRetryable,
} from "../src/index.js";

const host = process.env.CNPG_CLIENTS_PG_HOST;
const required = process.env.CNPG_CLIENTS_PG === "required";
if (!host && required) {
  throw new Error(
    "CNPG_CLIENTS_PG=required but no TLS PostgreSQL is configured (clients/conformance/pg-tls.sh up)",
  );
}

const e = process.env;
const dir = e.CNPG_CLIENTS_PG_DIR ?? "";
const addr = e.CNPG_CLIENTS_PG_ADDR ?? "";
const port = Number(e.CNPG_CLIENTS_PG_PORT);
const pwRole = e.CNPG_CLIENTS_PG_PW_ROLE ?? "";
const pw = e.CNPG_CLIENTS_PG_PW_PASSWORD ?? "";
const database = e.CNPG_CLIENTS_PG_DATABASE ?? "";

const pools: CnpgPool[] = [];
afterAll(async () => {
  await Promise.all(pools.map((p) => p.end().catch(() => undefined)));
});

const copy = (from: string, to: string) => {
  copyFileSync(join(dir, from), to);
};

/** Lays the files out as the cnpg-client chart does: ca.crt, tls.crt, tls.key. */
function mount(cert: string): string {
  const m = mkdtempSync(join(tmpdir(), "cnpg-ts-"));
  copy("ca.crt", join(m, "ca.crt"));
  setCert(m, cert);
  return m;
}
function setCert(m: string, cert: string) {
  copy(`${cert}.crt`, join(m, "tls.crt"));
  copy(`${cert}.key`, join(m, "tls.key"));
}

const fastRetry = { attempts: 3, initialDelayMs: 20, maxDelayMs: 100, budgetMs: 5_000 };

function certConfig(m: string, over: Partial<CnpgConfig> = {}): CnpgConfig {
  return {
    ...defaultConfig(),
    host: host as string,
    port,
    database,
    user: "app_cert",
    sslRootCert: join(m, "ca.crt"),
    sslCert: join(m, "tls.crt"),
    sslKey: join(m, "tls.key"),
    applicationName: "conformance",
    retry: fastRetry,
    ...over,
  };
}
const passwordConfig = (m: string, over: Partial<CnpgConfig> = {}): CnpgConfig =>
  certConfig(m, { user: pwRole, password: pw, sslCert: undefined, sslKey: undefined, ...over });

async function open(c: CnpgConfig, options?: Parameters<typeof CnpgPool.create>[1]): Promise<CnpgPool> {
  const p = await CnpgPool.create(c, options);
  pools.push(p);
  return p;
}
async function scalar<T>(p: CnpgPool, sql: string, values?: unknown[]): Promise<T> {
  const r = await p.query(sql, values);
  return Object.values(r.rows[0] as Record<string, unknown>)[0] as T;
}
const rejection = async (fn: () => Promise<unknown>): Promise<Error & { code?: string }> => {
  try {
    await fn();
  } catch (err) {
    return err as Error;
  }
  throw new Error("expected a rejection");
};
// The cause chain's root: CnpgPool.create wraps the driver's error.
const root = (err: Error): Error & { code?: string } => {
  let cur: Error & { cause?: unknown } = err;
  while (cur.cause instanceof Error) cur = cur.cause;
  return cur;
};

/** A TCP forwarder that can be dropped and restored: the rw Service while the primary behind it switches. */
class Forwarder {
  server!: net.Server;
  sockets = new Set<net.Socket>();
  port = 0;
  async up(listenPort = 0) {
    this.server = net.createServer((c) => {
      const up = net.connect(port, addr);
      for (const s of [c, up]) {
        this.sockets.add(s);
        s.on("error", () => s.destroy());
        s.on("close", () => this.sockets.delete(s));
      }
      c.pipe(up);
      up.pipe(c);
      c.on("close", () => up.destroy());
      up.on("close", () => c.destroy());
    });
    await new Promise<void>((resolve) => this.server.listen(listenPort, "127.0.0.1", resolve));
    this.port = (this.server.address() as net.AddressInfo).port;
  }
  async down() {
    for (const s of this.sockets) s.destroy();
    await new Promise<void>((resolve) => this.server.close(() => resolve()));
  }
}

describe.skipIf(!host)("conformance", () => {
  it("verify-full-connects", async () => {
    const p = await open(certConfig(mount("app_cert.1")));
    expect(await scalar(p, "select ssl from pg_stat_ssl where pid = pg_backend_pid()")).toBe(true);
    expect(await scalar(p, "select current_setting('application_name')")).toBe("conformance");
    expect(await scalar(p, "select current_setting('statement_timeout')")).toBe("30s");
    expect(await scalar(p, "select current_setting('idle_in_transaction_session_timeout')")).toBe("1min");
    await expect(p.health()).resolves.toBeUndefined();
  });

  it("rejects-unknown-ca", async () => {
    const m = mount("app_cert.1");
    copy("other-ca.crt", join(m, "ca.crt"));
    const err = await rejection(() => CnpgPool.create(certConfig(m)));
    expect(isRetryable(root(err)), err.message).toBe(false);
  });

  it("rejects-hostname-mismatch", async () => {
    const err = await rejection(() => CnpgPool.create(certConfig(mount("app_cert.1"), { host: addr })));
    expect(isRetryable(root(err)), err.message).toBe(false);
  });

  it("refuses-weaker-sslmode", async () => {
    const m = mount("app_cert.1");
    for (const mode of ["disable", "allow", "prefer", "require", "verify-ca"]) {
      await expect(CnpgPool.create(certConfig(m, { sslMode: mode })), mode).rejects.toThrow(
        /only verify-full is accepted/,
      );
      expect(() =>
        configFromEnv({ PGHOST: "h", PGDATABASE: "d", PGUSER: "u", PGSSLROOTCERT: "/ca", PGSSLMODE: mode }),
      ).toThrow(/only verify-full is accepted/);
    }
    await expect(CnpgPool.create(certConfig(m, { sslRootCert: "" }))).rejects.toThrow(
      /server CA file is required/,
    );
  });

  it("password-role", async () => {
    const m = mount("app_cert.1");
    const p = await open(passwordConfig(m));
    expect(await scalar(p, "select current_user")).toBe(pwRole);

    const err = root(await rejection(() => CnpgPool.create(passwordConfig(m, { password: "wrong" }))));
    expect(err.code).toBe("28P01");
    expect(isRetryable(err)).toBe(false);
  });

  it("client-cert-role", async () => {
    const p = await open(certConfig(mount("app_cert.1")));
    expect(await scalar(p, "select current_user")).toBe("app_cert");

    // The right name from the wrong authority is refused, and not retried.
    const m = mount("app_cert.1");
    copy("foreign.crt", join(m, "tls.crt"));
    copy("foreign.key", join(m, "tls.key"));
    const start = Date.now();
    const err = await rejection(() => CnpgPool.create(certConfig(m)));
    expect(isRetryable(root(err)), err.message).toBe(false);
    expect(Date.now() - start).toBeLessThan(2_000);
  });

  it("client-cert-rotation", async () => {
    const m = mount("app_cert.1");
    const p = await open(certConfig(m, { maxConnLifetimeMs: 400 }));
    const serial = () =>
      scalar<string>(p, "select client_serial::text from pg_stat_ssl where pid = pg_backend_pid()");
    expect(await serial()).toBe(String(0x101));
    setCert(m, "app_cert.2"); // what cert-manager's renewal does to the mounted files
    const deadline = Date.now() + 10_000;
    while ((await serial()) !== String(0x202)) {
      expect(Date.now(), "the renewed certificate was never picked up").toBeLessThan(deadline);
      await new Promise((r) => setTimeout(r, 150));
    }
  });

  it("password-file-rotation", async () => {
    const m = mount("app_cert.1");
    const file = join(mkdtempSync(join(tmpdir(), "cnpg-ts-pw-")), "password");
    const write = (v: string) => writeFileSync(file, `${v}\n`, { mode: 0o600 });
    write(pw);
    const p = await open(
      passwordConfig(m, { password: undefined, passwordFile: file, maxConnLifetimeMs: 400 }),
    );
    expect(await scalar(p, "select current_user")).toBe(pwRole);

    const next = "rotated-password";
    await p.query(`alter role ${pwRole} password '${next}'`);
    try {
      write(next);
      // The connection lifetime recycles the pooled connection; the next one reads the new file.
      const deadline = Date.now() + 10_000;
      for (;;) {
        try {
          expect(await scalar(p, "select current_user")).toBe(pwRole);
          await new Promise((r) => setTimeout(r, 500)); // let the old connection expire
          expect(await scalar(p, "select current_user")).toBe(pwRole);
          break;
        } catch (err) {
          if (Date.now() > deadline) throw err;
        }
      }
    } finally {
      write(next);
      const q = await open(passwordConfig(m, { password: next }));
      await q.query(`alter role ${pwRole} password '${pw}'`);
    }
  });

  it("reconnects-after-backend-termination", async () => {
    const p = await open(certConfig(mount("app_cert.1"), { poolMax: 2 }));
    const victim = await p.connect();
    const pid = (await victim.query("select pg_backend_pid() as pid")).rows[0].pid as number;
    expect(await scalar(p, "select pg_terminate_backend($1)", [pid])).toBe(true);
    // The pool's own error listener sees the dead idle client; releasing it with an error evicts it.
    victim.release(true);

    let attempts = 0;
    await p.do(async () => {
      attempts++;
      await p.query("select 1");
    });
    expect(attempts).toBeGreaterThanOrEqual(1);
    await expect(p.health()).resolves.toBeUndefined();
  });

  it("survives-primary-switch", async () => {
    const f = new Forwarder();
    await f.up();
    try {
      const retryPolicy = { attempts: 20, initialDelayMs: 50, maxDelayMs: 500, budgetMs: 15_000 };
      const p = await open(certConfig(mount("app_cert.1"), { port: f.port, retry: retryPolicy }));
      expect(await scalar(p, "select current_user")).toBe("app_cert");

      const listenPort = f.port;
      await f.down(); // the old primary is gone and nothing answers yet
      setTimeout(() => void f.up(listenPort), 1_500);

      let attempts = 0;
      await p.do(async () => {
        attempts++;
        await p.query("select current_user");
      });
      expect(attempts, "the first try must have met the switch").toBeGreaterThan(1);
    } finally {
      await f.down().catch(() => undefined);
    }
  });

  it("statement-timeout-enforced", async () => {
    const p = await open(certConfig(mount("app_cert.1"), { statementTimeoutMs: 300 }));
    const start = Date.now();
    let attempts = 0;
    const err = await rejection(() =>
      p.do(async () => {
        attempts++;
        await p.query("select pg_sleep(5)");
      }),
    );
    expect(err.code).toBe("57014");
    expect(attempts, "a timeout is not a connection failure").toBe(1);
    expect(Date.now() - start).toBeLessThan(3_000);
  });

  it("permanent-error-not-retried", async () => {
    const p = await open(certConfig(mount("app_cert.1")));
    await p.query("drop table if exists cnpg_clients_conf_ts");
    await p.query("create table cnpg_clients_conf_ts (id int primary key)");
    await p.query("insert into cnpg_clients_conf_ts values (1)");
    try {
      let attempts = 0;
      const err = await rejection(() =>
        p.do(async () => {
          attempts++;
          await p.query("insert into cnpg_clients_conf_ts values (1)");
        }),
      );
      expect(err.code).toBe("23505");
      expect(attempts).toBe(1);
    } finally {
      await p.query("drop table if exists cnpg_clients_conf_ts");
    }
  });

  it("health-check", async () => {
    const f = new Forwarder();
    await f.up();
    try {
      const p = await open(certConfig(mount("app_cert.1"), { port: f.port }));
      await expect(p.health()).resolves.toBeUndefined();
      await f.down();
      const start = Date.now();
      await expect(p.health()).rejects.toThrow();
      expect(Date.now() - start).toBeLessThan(HEALTH_TIMEOUT_MS + 1_000);
    } finally {
      await f.down().catch(() => undefined);
    }
  });

  it("traces-statements-without-arguments", async () => {
    const exporter = new InMemorySpanExporter();
    const provider = new BasicTracerProvider({ spanProcessors: [new SimpleSpanProcessor(exporter)] });
    const p = await open(certConfig(mount("app_cert.1")), { tracer: provider.getTracer("test") });
    const secret = "user-data-that-must-not-be-traced";
    expect(await scalar(p, "select $1::text", [secret])).toBe(secret);

    const spans = exporter.getFinishedSpans();
    expect(spans.some((s) => s.attributes["db.query.text"] === "select $1::text")).toBe(true);
    for (const s of spans) {
      expect(s.attributes["db.system.name"]).toBe("postgresql");
      expect(JSON.stringify(s.attributes)).not.toContain(secret);
    }
  });
});

import { describe, expect, it } from "vitest";
import { AttemptTimeoutError, backoffMs, defaultRetryPolicy, isRetryable, retry } from "../src/index.js";

const sqlState = (code: string) => Object.assign(new Error(`pg ${code}`), { code });
const fast = { attempts: 4, initialDelayMs: 1, maxDelayMs: 4, budgetMs: 1_000 };

describe("isRetryable", () => {
  it.each([
    ["admin shutdown (switchover)", sqlState("57P01"), true],
    ["crash shutdown", sqlState("57P02"), true],
    ["cannot connect now", sqlState("57P03"), true],
    ["connection failure", sqlState("08006"), true],
    ["demoted primary, read-only", sqlState("25006"), true],
    ["too many connections", sqlState("53300"), true],
    ["unique violation", sqlState("23505"), false],
    ["syntax", sqlState("42601"), false],
    ["permission", sqlState("42501"), false],
    ["bad password", sqlState("28P01"), false],
    ["statement timeout", sqlState("57014"), false],
    ["serialization failure", sqlState("40001"), false],
    ["deadlock", sqlState("40P01"), false],
    ["connection reset", sqlState("ECONNRESET"), true],
    ["connection refused", sqlState("ECONNREFUSED"), true],
    ["terminated unexpectedly", new Error("Connection terminated unexpectedly"), true],
    ["self-signed in chain", sqlState("SELF_SIGNED_CERT_IN_CHAIN"), false],
    ["unknown issuer", sqlState("UNABLE_TO_VERIFY_LEAF_SIGNATURE"), false],
    ["hostname mismatch", sqlState("ERR_TLS_CERT_ALTNAME_INVALID"), false],
    ["tls alert from the server", sqlState("ERR_SSL_TLSV1_ALERT_UNKNOWN_CA"), false],
    [
      "refused, wrapped like the health check",
      new Error("cnpg-client: health: x", { cause: sqlState("ECONNREFUSED") }),
      true,
    ],
    [
      "timed out in startup, wrapped",
      new Error("outer", { cause: new AttemptTimeoutError("first connection", 5000) }),
      true,
    ],
    ["bad certificate, wrapped", new Error("outer", { cause: sqlState("CERT_HAS_EXPIRED") }), false],
    ["wrapped permanent error", new Error("outer", { cause: sqlState("28P01") }), false],
    ["plain error", new Error("boom"), false],
    ["not an error", "boom", false],
  ])("%s", (_name, err, want) => {
    expect(isRetryable(err)).toBe(want);
  });
});

describe("retry", () => {
  it("stops at the attempt count", async () => {
    let n = 0;
    await expect(
      retry(fast, async () => {
        n++;
        throw sqlState("57P01");
      }),
    ).rejects.toThrow();
    expect(n).toBe(4);
  });

  it("succeeds later", async () => {
    let n = 0;
    const v = await retry(fast, async () => {
      if (++n < 3) throw sqlState("57P01");
      return "ok";
    });
    expect([v, n]).toEqual(["ok", 3]);
  });

  it("never repeats a permanent error", async () => {
    let n = 0;
    await expect(
      retry(fast, async () => {
        n++;
        throw sqlState("23505");
      }),
    ).rejects.toThrow();
    expect(n).toBe(1);
  });

  it("honours the budget and the abort signal", async () => {
    const p = { attempts: 100, initialDelayMs: 50, maxDelayMs: 50, budgetMs: 120 };
    const start = Date.now();
    let n = 0;
    await retry(p, async () => {
      n++;
      throw sqlState("57P01");
    }).catch(() => undefined);
    expect(Date.now() - start).toBeLessThan(1_000);
    expect(n).toBeLessThan(100);

    const ac = new AbortController();
    ac.abort();
    n = 0;
    await retry(
      fast,
      async () => {
        n++;
        throw sqlState("57P01");
      },
      ac.signal,
    ).catch(() => undefined);
    expect(n).toBe(1);
  });

  it("retries a refused first connection and calls onRetry", async () => {
    const seen: [number, string, number][] = [];
    let n = 0;
    const v = await retry({ ...fast, onRetry: (a, e, d) => seen.push([a, e.message, d]) }, async () => {
      if (++n < 3) throw new Error("wrapped", { cause: sqlState("ECONNREFUSED") });
      return "ok";
    });
    expect([v, n]).toEqual(["ok", 3]);
    expect(seen.map((x) => [x[0], x[1]])).toEqual([
      [1, "wrapped"],
      [2, "wrapped"],
    ]);
    expect(seen.every((x) => x[2] >= 0 && x[2] <= fast.maxDelayMs)).toBe(true);
  });

  it("succeeds on the second attempt after a startup timeout", async () => {
    let n = 0;
    const v = await retry(fast, async () => {
      if (++n < 2) throw new AttemptTimeoutError("first connection", 5000);
      return "ok";
    });
    expect([v, n]).toEqual(["ok", 2]);
  });

  it("gives up at the attempt cap with the last error", async () => {
    let n = 0;
    const onRetry: number[] = [];
    await expect(
      retry({ ...fast, onRetry: (a) => onRetry.push(a) }, async () => {
        n++;
        throw sqlState("ECONNREFUSED");
      }),
    ).rejects.toMatchObject({ code: "ECONNREFUSED" });
    expect(n).toBe(fast.attempts);
    expect(onRetry).toEqual([1, 2, 3]);
  });

  it("does not retry after the caller's abort or deadline", async () => {
    let n = 0;
    const ac = new AbortController();
    await retry(
      fast,
      async () => {
        n++;
        ac.abort();
        throw sqlState("ECONNREFUSED");
      },
      ac.signal,
    ).catch(() => undefined);
    expect(n).toBe(1);

    n = 0;
    await retry(
      fast,
      async () => {
        n++;
        await new Promise((r) => setTimeout(r, 30)); // outlives the deadline
        throw sqlState("ECONNREFUSED");
      },
      AbortSignal.timeout(5),
    ).catch(() => undefined);
    expect(n).toBe(1);
    expect(isRetryable(new DOMException("aborted", "AbortError"))).toBe(false);
  });

  it("backs off within bounds", () => {
    const p = defaultRetryPolicy();
    for (let a = 1; a < 20; a++) {
      const d = backoffMs(p, a);
      expect(d).toBeGreaterThanOrEqual(0);
      expect(d).toBeLessThanOrEqual(p.maxDelayMs);
    }
  });
});

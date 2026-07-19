const crypto = require("crypto");

const DEFAULT_SIGNER_URL = "http://127.0.0.1:4010";
const DEFAULT_RECIPIENT = "0x000000000000000000000000000000000000dEaD";
const DEFAULT_USDT = "0x55d398326f99059fF775485246999027B3197955";
const BAD_TOKEN = "0x1111111111111111111111111111111111111111";

function arg(name, fallback = "") {
  const prefix = `--${name}=`;
  const found = process.argv.find((item) => item.startsWith(prefix));
  return found ? found.slice(prefix.length) : fallback;
}

function hmacRaw(secret, ts, nonce, body) {
  return crypto.createHmac("sha256", secret).update(`${ts}.${nonce}.${body}`).digest("hex");
}

function nonce(label) {
  return `${label}-${crypto.randomBytes(12).toString("hex")}`;
}

function signedHeaders(secret, body, nonceValue = nonce("adv"), ts = Math.floor(Date.now() / 1000).toString()) {
  return {
    "content-type": "application/json",
    "x-ts": ts,
    "x-nonce": nonceValue,
    "x-signer-hmac": hmacRaw(secret, ts, nonceValue, body),
  };
}

async function request(baseURL, path, options = {}) {
  const started = Date.now();
  try {
    const res = await fetch(`${baseURL}${path}`, options);
    const text = await res.text();
    return { ok: true, status: res.status, text, ms: Date.now() - started };
  } catch (err) {
    return { ok: false, status: 0, text: err.message, ms: Date.now() - started };
  }
}

function expectStatus(name, got, allowed) {
  const pass = allowed.includes(got.status);
  return {
    name,
    pass,
    status: got.status,
    expected: allowed.join("|"),
    ms: got.ms,
    detail: got.text.slice(0, 220).replace(/\s+/g, " "),
  };
}

function printResult(result) {
  const mark = result.pass ? "PASS" : "FAIL";
  console.log(`${mark} ${result.name} status=${result.status} expected=${result.expected} ${result.ms}ms`);
  if (!result.pass || process.env.VERBOSE === "1") {
    console.log(`  ${result.detail}`);
  }
}

function percentile(sortedValues, p) {
  if (!sortedValues.length) return 0;
  const index = Math.ceil((p / 100) * sortedValues.length) - 1;
  return sortedValues[Math.min(Math.max(index, 0), sortedValues.length - 1)];
}

function latencySummary(results) {
  const values = results
    .map((result) => Number(result.ms))
    .filter((value) => Number.isFinite(value))
    .sort((a, b) => a - b);
  if (!values.length) return null;
  const sum = values.reduce((acc, value) => acc + value, 0);
  return {
    count: values.length,
    min: values[0],
    avg: Math.round(sum / values.length),
    p50: percentile(values, 50),
    p55: percentile(values, 55),
    p75: percentile(values, 75),
    p90: percentile(values, 90),
    p95: percentile(values, 95),
    p99: percentile(values, 99),
    max: values[values.length - 1],
  };
}

function printLatencySummary(results) {
  const summary = latencySummary(results);
  if (!summary) return;
  console.log("");
  console.log("Latency summary:");
  console.log(
    `count=${summary.count} min=${summary.min}ms avg=${summary.avg}ms p50=${summary.p50}ms p55=${summary.p55}ms p75=${summary.p75}ms p90=${summary.p90}ms p95=${summary.p95}ms p99=${summary.p99}ms max=${summary.max}ms`,
  );
}

function transferBody(overrides = {}) {
  return JSON.stringify({
    to: DEFAULT_RECIPIENT,
    amount: "0",
    tokenContract: DEFAULT_USDT,
    network: "BSC",
    idempotencyKey: `adv-${Date.now()}-${crypto.randomBytes(4).toString("hex")}`,
    ...overrides,
  });
}

async function signedTransferCase(baseURL, secret, name, bodyObject, expectedStatuses) {
  const body = typeof bodyObject === "string" ? bodyObject : transferBody(bodyObject);
  return expectStatus(
    name,
    await request(baseURL, "/hd/transfer", {
      method: "POST",
      body,
      headers: signedHeaders(secret, body),
    }),
    expectedStatuses,
  );
}

async function parallelNonceReplayCase(baseURL, secret) {
  const body = transferBody({ amount: "0", idempotencyKey: `parallel-${Date.now()}` });
  const sharedNonce = nonce("parallel");
  const headers = signedHeaders(secret, body, sharedNonce);
  const attempts = await Promise.all(
    Array.from({ length: 10 }, () =>
      request(baseURL, "/hd/transfer", {
        method: "POST",
        body,
        headers,
      }),
    ),
  );
  const unauthorized = attempts.filter((item) => item.status === 401).length;
  const reachedHandler = attempts.length - unauthorized;
  const pass = unauthorized >= 9 && reachedHandler <= 1;
  return {
    name: "parallel same nonce replay is rejected",
    pass,
    status: `${unauthorized}/10 rejected`,
    expected: ">=9/10 rejected",
    ms: Math.max(...attempts.map((item) => item.ms)),
    detail: attempts.map((item) => item.status).join(","),
  };
}

async function main() {
  const baseURL = (arg("url", process.env.SIGNER_URL) || DEFAULT_SIGNER_URL).replace(/\/+$/, "");
  const secret = arg("secret", process.env.SIGNER_HMAC_SECRET || process.env.HMAC_SECRET || "");
  const token = arg("token", process.env.BSC_USDT_CONTRACT || DEFAULT_USDT);
  const recipient = arg("to", DEFAULT_RECIPIENT);
  const network = arg("network", "BSC");

  const results = [];
  results.push(expectStatus("healthz is public", await request(baseURL, "/healthz"), [200]));
  results.push(expectStatus("readyz is public", await request(baseURL, "/readyz"), [200, 503]));

  const bodyObject = {
    to: recipient,
    amount: "0",
    tokenContract: token,
    network,
    idempotencyKey: `adv-${Date.now()}-${crypto.randomBytes(4).toString("hex")}`,
  };
  const body = JSON.stringify(bodyObject);

  results.push(
    expectStatus(
      "protected transfer rejects missing HMAC",
      await request(baseURL, "/hd/transfer", { method: "POST", body, headers: { "content-type": "application/json" } }),
      [401],
    ),
  );

  results.push(
    expectStatus(
      "protected transfer rejects bad HMAC",
      await request(baseURL, "/hd/transfer", {
        method: "POST",
        body,
        headers: { "content-type": "application/json", "x-ts": `${Math.floor(Date.now() / 1000)}`, "x-nonce": nonce("bad"), "x-signer-hmac": "00" },
      }),
      [401],
    ),
  );

  if (secret) {
    const oldTs = `${Math.floor(Date.now() / 1000) - 3600}`;
    results.push(
      expectStatus(
        "protected transfer rejects expired signed request",
        await request(baseURL, "/hd/transfer", {
          method: "POST",
          body,
          headers: signedHeaders(secret, body, nonce("expired"), oldTs),
        }),
        [401],
      ),
    );

    const signedOriginal = signedHeaders(secret, body, nonce("tamper"));
    const tamperedBody = JSON.stringify({ ...bodyObject, to: "0x1111111111111111111111111111111111111111" });
    results.push(
      expectStatus(
        "protected transfer rejects tampered payload",
        await request(baseURL, "/hd/transfer", { method: "POST", body: tamperedBody, headers: signedOriginal }),
        [401],
      ),
    );

    const replayNonce = nonce("replay");
    const replayHeaders = signedHeaders(secret, body, replayNonce);
    results.push(
      expectStatus(
        "valid HMAC reaches policy and rejects zero amount without sending",
        await request(baseURL, "/hd/transfer", { method: "POST", body, headers: replayHeaders }),
        [400, 409, 502],
      ),
    );
    results.push(
      expectStatus(
        "same nonce replay is rejected",
        await request(baseURL, "/hd/transfer", { method: "POST", body, headers: replayHeaders }),
        [401],
      ),
    );

    results.push(await signedTransferCase(baseURL, secret, "signed transfer rejects invalid token allowlist", { tokenContract: BAD_TOKEN, amount: "1" }, [400, 502]));
    results.push(await signedTransferCase(baseURL, secret, "signed transfer rejects amount above max", { amount: "1000000000" }, [400, 502]));
    results.push(await signedTransferCase(baseURL, secret, "signed transfer rejects invalid network", { network: "POLYGON", amount: "1" }, [400, 502]));
    results.push(await signedTransferCase(baseURL, secret, "signed transfer rejects invalid recipient", { to: "abc", amount: "1" }, [400, 502]));

    const idem = `same-idem-${Date.now()}-${crypto.randomBytes(4).toString("hex")}`;
    results.push(await signedTransferCase(baseURL, secret, "same idempotency key first invalid request is handled once", { amount: "0", idempotencyKey: idem }, [400, 409, 502]));
    results.push(await signedTransferCase(baseURL, secret, "same idempotency key replay with new nonce is deduped or blocked", { amount: "0", idempotencyKey: idem }, [200, 400, 409, 502]));
    results.push(await parallelNonceReplayCase(baseURL, secret));
  } else {
    console.log("SKIP signed adversarial cases: set SIGNER_HMAC_SECRET or pass --secret=...");
  }

  for (const result of results) printResult(result);
  const failed = results.filter((result) => !result.pass);
  console.log("");
  console.log(`Signer adversarial smoke finished: ${results.length - failed.length}/${results.length} passed`);
  printLatencySummary(results);
  if (failed.length) process.exitCode = 1;
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});

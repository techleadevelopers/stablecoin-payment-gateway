const crypto = require("crypto");

function arg(name, fallback = "") {
  const prefix = `--${name}=`;
  const found = process.argv.find((item) => item.startsWith(prefix));
  return found ? found.slice(prefix.length) : fallback;
}

function hasFlag(name) {
  return process.argv.includes(`--${name}`);
}

function hmacRaw(secret, ts, nonce, body) {
  return crypto.createHmac("sha256", secret).update(`${ts}.${nonce}.${body}`).digest("hex");
}

function signedHeaders(secret, body) {
  const ts = Math.floor(Date.now() / 1000).toString();
  const nonce = `live-${crypto.randomBytes(16).toString("hex")}`;
  return {
    "content-type": "application/json",
    "x-ts": ts,
    "x-nonce": nonce,
    "x-signer-hmac": hmacRaw(secret, ts, nonce, body),
  };
}

function fail(message) {
  console.error(message);
  process.exit(1);
}

async function main() {
  if (!hasFlag("i-understand-this-sends-funds")) {
    fail("Refusing to run. Add --i-understand-this-sends-funds to send a real signer transfer.");
  }

  const signerURL = (arg("url", process.env.SIGNER_URL) || "").replace(/\/+$/, "");
  const secret = arg("secret", process.env.SIGNER_HMAC_SECRET || process.env.HMAC_SECRET || "");
  const to = arg("to", process.env.SIGNER_LIVE_TEST_TO || "");
  const amount = arg("amount", process.env.SIGNER_LIVE_TEST_AMOUNT || "0.01");
  const tokenContract = arg("token", process.env.SIGNER_LIVE_TEST_TOKEN || process.env.BSC_USDT_CONTRACT || "");
  const network = arg("network", process.env.SIGNER_LIVE_TEST_NETWORK || "BSC");

  if (!signerURL) fail("SIGNER_URL is required.");
  if (!secret) fail("SIGNER_HMAC_SECRET or HMAC_SECRET is required.");
  if (!/^0x[0-9a-fA-F]{40}$/.test(to)) fail("--to or SIGNER_LIVE_TEST_TO must be a valid EVM address.");
  if (!/^0x[0-9a-fA-F]{40}$/.test(tokenContract)) fail("--token or SIGNER_LIVE_TEST_TOKEN/BSC_USDT_CONTRACT must be a valid token contract.");

  const body = JSON.stringify({
    to,
    amount,
    tokenContract,
    network,
    idempotencyKey: `live-${Date.now()}-${crypto.randomBytes(6).toString("hex")}`,
  });

  console.log("Sending real signer transfer request.");
  console.log(`url=${signerURL}`);
  console.log(`network=${network}`);
  console.log(`token=${tokenContract}`);
  console.log(`to=${to}`);
  console.log(`amount=${amount}`);

  const started = Date.now();
  const res = await fetch(`${signerURL}/hd/transfer`, {
    method: "POST",
    body,
    headers: signedHeaders(secret, body),
  });
  const text = await res.text();
  const latencyMs = Date.now() - started;
  console.log(`status=${res.status}`);
  console.log(`latency_ms=${latencyMs}`);
  console.log(text);
  if (res.status < 200 || res.status >= 300) process.exitCode = 1;
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});

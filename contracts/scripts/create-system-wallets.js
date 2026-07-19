const fs = require("fs");
const path = require("path");
const { Wallet } = require("ethers");

const ROOT = path.resolve(__dirname, "..", "..");
const OUT_DIR = path.resolve(__dirname, "..", "wallets");

function arg(name) {
  const prefix = `--${name}=`;
  const found = process.argv.find((item) => item.startsWith(prefix));
  return found ? found.slice(prefix.length) : "";
}

function hasFlag(name) {
  return process.argv.includes(`--${name}`);
}

function nowStamp() {
  return new Date().toISOString().replace(/[:.]/g, "-");
}

function makeWallet(label) {
  const wallet = Wallet.createRandom();
  return {
    label,
    address: wallet.address,
    privateKey: wallet.privateKey,
    mnemonic: wallet.mnemonic ? wallet.mnemonic.phrase : "",
  };
}

function envLines(wallets) {
  const byLabel = Object.fromEntries(wallets.map((wallet) => [wallet.label, wallet]));
  return [
    `TREASURY_HOT="${byLabel.TREASURY_HOT.address}"`,
    `TREASURY_COLD="${byLabel.TREASURY_COLD.address}"`,
    `SELL_WALLET_ADDRESS="${byLabel.SELL_WALLET_ADDRESS.address}"`,
    `CUSTODY_PROTECTED_WALLETS=${byLabel.TREASURY_HOT.address},${byLabel.SELL_WALLET_ADDRESS.address}`,
    "SELL_PAYOUT_MODE=manual",
    "CUSTODY_GUARD_ENABLED=true",
    "CUSTODY_MODE=paper",
    "CUSTODY_TRUSTED_DELEGATES=",
    "CUSTODY_ALLOWED_SELECTORS=",
    "BSC_TREASURY_CONTRACT=",
    "POLYGON_TREASURY_CONTRACT=",
  ];
}

function replaceOrAppendEnv(file, updates) {
  const existing = fs.existsSync(file) ? fs.readFileSync(file, "utf8") : "";
  const lines = existing.split(/\r?\n/);
  const indexByKey = new Map();
  for (let i = 0; i < lines.length; i++) {
    const match = lines[i].match(/^([A-Z0-9_]+)=/);
    if (match) indexByKey.set(match[1], i);
  }

  for (const line of updates) {
    const key = line.split("=", 1)[0];
    if (indexByKey.has(key)) {
      lines[indexByKey.get(key)] = line;
    } else {
      lines.push(line);
    }
  }
  fs.writeFileSync(file, lines.join("\n").replace(/\n{3,}/g, "\n\n"), "utf8");
}

function main() {
  const writeEnv = hasFlag("write-env");
  const envFile = path.resolve(ROOT, arg("env") || ".env");
  const stamp = nowStamp();
  const wallets = [
    makeWallet("TREASURY_HOT"),
    makeWallet("TREASURY_COLD"),
    makeWallet("SELL_WALLET_ADDRESS"),
  ];
  const env = envLines(wallets);

  fs.mkdirSync(OUT_DIR, { recursive: true });
  const jsonPath = path.join(OUT_DIR, `system-wallets-${stamp}.json`);
  const envPath = path.join(OUT_DIR, `system-wallets-${stamp}.env`);
  const secretPayload = {
    createdAt: new Date().toISOString(),
    warning: "PRIVATE KEYS. Keep offline. Do not commit. Fund only the public addresses you intend to use.",
    wallets,
  };
  fs.writeFileSync(jsonPath, JSON.stringify(secretPayload, null, 2), { encoding: "utf8", mode: 0o600 });
  fs.writeFileSync(envPath, env.join("\n") + "\n", { encoding: "utf8", mode: 0o600 });

  if (writeEnv) {
    replaceOrAppendEnv(envFile, env);
  }

  console.log("System wallets generated.");
  console.log("");
  console.log("Addresses:");
  for (const wallet of wallets) {
    console.log(`${wallet.label}=${wallet.address}`);
  }
  console.log("");
  console.log("Env lines:");
  console.log(env.join("\n"));
  console.log("");
  console.log(`Secret backup written to: ${jsonPath}`);
  console.log(`Env snippet written to:   ${envPath}`);
  console.log(writeEnv ? `Updated env file:       ${envFile}` : "Env file not changed. Re-run with --write-env to update .env.");
}

main();

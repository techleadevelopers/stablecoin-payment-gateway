const fs = require("fs");
const path = require("path");

function parseEnvLine(line) {
  const trimmed = line.trim();
  if (!trimmed || trimmed.startsWith("#")) return null;
  const index = trimmed.indexOf("=");
  if (index <= 0) return null;
  const key = trimmed.slice(0, index).trim();
  let value = trimmed.slice(index + 1).trim();
  if ((value.startsWith('"') && value.endsWith('"')) || (value.startsWith("'") && value.endsWith("'"))) {
    value = value.slice(1, -1);
  }
  return [key, value];
}

function loadEnvFile(filePath) {
  if (!fs.existsSync(filePath)) return;
  const content = fs.readFileSync(filePath, "utf8");
  for (const line of content.split(/\r?\n/)) {
    const parsed = parseEnvLine(line);
    if (!parsed) continue;
    const [key, value] = parsed;
    if (process.env[key] === undefined) {
      process.env[key] = value;
    }
  }
}

function loadContractEnv() {
  loadEnvFile(path.resolve(__dirname, "..", "..", ".env"));
  loadEnvFile(path.resolve(__dirname, "..", ".env"));
}

function firstEnv(...keys) {
  for (const key of keys) {
    const value = process.env[key];
    if (value && value.trim()) return value.trim();
  }
  return "";
}

function firstUrlEnv(...keys) {
  const value = firstEnv(...keys);
  if (!value) return "";
  return value.split(",").map((item) => item.trim()).filter(Boolean)[0] || "";
}

function normalizePrivateKey(value) {
  const key = String(value || "").trim();
  if (!key) return "";
  const prefixed = key.startsWith("0x") ? key : `0x${key}`;
  if (!/^0x[0-9a-fA-F]{64}$/.test(prefixed)) {
    throw new Error("private key invalida: use 32 bytes hex em DEPLOYER_PRIVATE_KEY ou PRIVATE_KEY");
  }
  return prefixed;
}

function csv(value) {
  return String(value || "")
    .split(",")
    .map((item) => item.trim())
    .filter(Boolean);
}

module.exports = {
  csv,
  firstEnv,
  firstUrlEnv,
  loadContractEnv,
  normalizePrivateKey,
};

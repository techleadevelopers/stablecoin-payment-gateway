const fs = require("fs");
const path = require("path");
const solc = require("solc");
const { ethers } = require("ethers");

function loadEnvFile(filePath) {
  if (!fs.existsSync(filePath)) return;

  const lines = fs.readFileSync(filePath, "utf8").split(/\r?\n/);
  for (const line of lines) {
    const trimmed = line.trim();
    if (!trimmed || trimmed.startsWith("#") || !trimmed.includes("=")) continue;

    const index = trimmed.indexOf("=");
    const key = trimmed.slice(0, index).trim();
    let value = trimmed.slice(index + 1).trim();
    if ((value.startsWith('"') && value.endsWith('"')) || (value.startsWith("'") && value.endsWith("'"))) {
      value = value.slice(1, -1);
    }

    if (key && process.env[key] === undefined) {
      process.env[key] = value;
    }
  }
}

function argValue(name) {
  const prefix = `--${name}=`;
  const found = process.argv.find((arg) => arg.startsWith(prefix));
  return found ? found.slice(prefix.length) : "";
}

function firstEnv(...keys) {
  for (const key of keys) {
    const value = (process.env[key] || "").trim();
    if (value && value !== "...") return value;
  }
  return "";
}

function firstRPC() {
  const explicit = argValue("rpc");
  const value = explicit || firstEnv(
    "EIP_PROBE_RPC_URLS",
    "POLYGON_AMOY_RPC_URLS",
    "POLYGON_AMOY_RPC_URL",
    "BNB_TESTNET_RPC_URLS",
    "BSC_TESTNET_RPC_URL",
    "BSC_RPC_URLS",
    "RPC1",
    "RPC2",
    "RPC3",
    "RPC4",
    "RPCN",
    "RPC_URL"
  );
  return value.split(",").map((item) => item.trim()).filter(Boolean)[0] || "";
}

function normalizePrivateKey(value, label) {
  const key = (value || "").trim();
  if (!key || key === "...") {
    throw new Error(`${label} is required`);
  }
  return key.startsWith("0x") ? key : `0x${key}`;
}

function probeWalletAddresses() {
  const keys = (process.env.EIP_PROBE_WALLET_PRIVATE_KEYS || "")
    .split(",")
    .map((value) => value.trim())
    .filter((value) => value && value !== "..." && !/^key\d+$/i.test(value));

  const fromKeys = keys.map((key) => new ethers.Wallet(normalizePrivateKey(key, "probe wallet key")).address);
  const extra = (process.env.EIP_PROBE_MINT_TO || "")
    .split(",")
    .map((value) => value.trim())
    .filter(Boolean);

  return [...new Set([...fromKeys, ...extra])];
}

function compileContract() {
  const sourcePath = path.join(__dirname, "..", "src", "mocks", "MockUSDT3009.sol");
  const source = fs.readFileSync(sourcePath, "utf8");
  const input = {
    language: "Solidity",
    sources: {
      "MockUSDT3009.sol": { content: source }
    },
    settings: {
      optimizer: { enabled: true, runs: 200 },
      outputSelection: {
        "*": {
          "*": ["abi", "evm.bytecode.object"]
        }
      }
    }
  };

  const output = JSON.parse(solc.compile(JSON.stringify(input)));
  const errors = (output.errors || []).filter((item) => item.severity === "error");
  if (errors.length > 0) {
    throw new Error(errors.map((item) => item.formattedMessage).join("\n"));
  }

  const contract = output.contracts["MockUSDT3009.sol"].MockUSDT3009;
  return {
    abi: contract.abi,
    bytecode: `0x${contract.evm.bytecode.object}`
  };
}

async function wait(tx, label) {
  const receipt = await tx.wait();
  console.log(`${label}: ${receipt.hash}`);
  return receipt;
}

async function main() {
  loadEnvFile(path.join(__dirname, "..", "..", ".env"));
  loadEnvFile(path.join(__dirname, "..", ".env"));

  const rpc = firstRPC();
  if (!rpc) {
    throw new Error("EIP_PROBE_RPC_URLS or another RPC env var is required");
  }

  const deployerKey = normalizePrivateKey(firstEnv(
    "DEPLOYER_PRIVATE_KEY",
    "EIP_PROBE_RELAYER_PRIVATE_KEY",
    "PAYMASTER_PRIV_KEY"
  ), "DEPLOYER_PRIVATE_KEY or EIP_PROBE_RELAYER_PRIVATE_KEY");

  const name = process.env.EIP_PROBE_TOKEN_NAME || "Tether USD";
  const symbol = process.env.EIP_PROBE_TOKEN_SYMBOL || "USDT";
  const version = process.env.EIP_PROBE_TOKEN_VERSION || "1";
  const mintAmount = ethers.parseUnits(process.env.EIP_PROBE_MINT_AMOUNT || "1000", 6);
  const networkLabel = argValue("network") || process.env.EIP_PROBE_NETWORK || "probe";
  const recipients = probeWalletAddresses();

  const provider = new ethers.JsonRpcProvider(rpc);
  const deployer = new ethers.Wallet(deployerKey, provider);
  const network = await provider.getNetwork();
  const balance = await provider.getBalance(deployer.address);
  if (balance === 0n) {
    throw new Error(`deployer ${deployer.address} has zero native balance on chain ${network.chainId}`);
  }

  console.log("network:", networkLabel);
  console.log("chainId:", network.chainId.toString());
  console.log("deployer:", deployer.address);
  console.log("deployerNativeWei:", balance.toString());
  console.log("token name:", name);
  console.log("token version:", version);

  const compiled = compileContract();
  const factory = new ethers.ContractFactory(compiled.abi, compiled.bytecode, deployer);
  const token = await factory.deploy(name, symbol, version);
  const deployTx = token.deploymentTransaction();
  console.log("deployTx:", deployTx.hash);
  await token.waitForDeployment();

  const tokenAddress = await token.getAddress();
  console.log("MockUSDT3009:", tokenAddress);

  if (recipients.length === 0) {
    console.log("no EIP_PROBE_WALLET_PRIVATE_KEYS/EIP_PROBE_MINT_TO recipients found; skipping mint");
  }

  for (const recipient of recipients) {
    const tx = await token.mint(recipient, mintAmount);
    await wait(tx, `mint ${ethers.formatUnits(mintAmount, 6)} ${symbol} to ${recipient}`);
  }

  console.log("");
  console.log("env suggestions:");
  console.log(`EIP_PROBE_ASSET=${symbol}`);
  console.log(`EIP_PROBE_TOKEN_CONTRACT=${tokenAddress}`);
  console.log(`EIP_PROBE_TOKEN_NAME=${name}`);
  console.log(`EIP_PROBE_TOKEN_SYMBOL=${symbol}`);
  console.log(`EIP_PROBE_TOKEN_VERSION=${version}`);
  console.log("EIP_PROBE_REAL_RUN=true");
}

main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});

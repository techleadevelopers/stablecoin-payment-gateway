const fs = require("fs");
const path = require("path");
const { ethers, network: hardhatNetwork } = require("hardhat");
const { csv, firstEnv, loadContractEnv } = require("./env");

loadContractEnv();

const EXPECTED_CHAIN_IDS = {
  bsc: 56n,
  bscTestnet: 97n,
  polygon: 137n,
  polygonAmoy: 80002n,
  hardhat: 31337n,
};

async function main() {
  const [deployer] = await ethers.getSigners();
  if (!deployer) {
    throw new Error("configure DEPLOYER_PRIVATE_KEY ou PRIVATE_KEY para deploy em rede externa");
  }

  const network = await ethers.provider.getNetwork();
  const networkName = hardhatNetwork.name;
  const expectedChainId = EXPECTED_CHAIN_IDS[networkName];
  if (expectedChainId && network.chainId !== expectedChainId) {
    throw new Error(`chainId inesperado para ${networkName}: recebido ${network.chainId}, esperado ${expectedChainId}`);
  }

  const owner = firstEnv("CONTRACT_OWNER", "OWNER_ADDRESS") || deployer.address;
  const operator = firstEnv("CONTRACT_OPERATOR", "SIGNER_OPERATOR_ADDRESS", "OPERATOR_ADDRESS") || deployer.address;
  const guardians = csv(firstEnv("CONTRACT_GUARDIANS", "TREASURY_GUARDIANS"));
  const recipients = csv(firstEnv("TREASURY_ALLOWED_RECIPIENTS", "CONTRACT_ALLOWED_RECIPIENTS"));
  const token = resolveTreasuryToken(networkName);
  const tokenSymbol = firstEnv("TREASURY_TOKEN_SYMBOL") || defaultTokenSymbol(networkName);
  const tokenDecimals = Number(firstEnv("TREASURY_TOKEN_DECIMALS") || defaultTokenDecimals(networkName));
  const configureOwnerOnly = owner.toLowerCase() === deployer.address.toLowerCase();

  assertAddress(owner, "CONTRACT_OWNER");
  assertAddress(operator, "CONTRACT_OPERATOR");
  if (token) assertAddress(token, "TREASURY_TOKEN_CONTRACT");
  for (const guardian of guardians) assertAddress(guardian, "CONTRACT_GUARDIANS");
  for (const recipient of recipients) assertAddress(recipient, "TREASURY_ALLOWED_RECIPIENTS");

  console.log("deployer:", deployer.address);
  console.log("owner:", owner);
  console.log("operator:", operator);
  console.log("network:", networkName);
  console.log("chainId:", network.chainId.toString());
  console.log("ownerConfigEnabled:", configureOwnerOnly);

  const Vault = await ethers.getContractFactory("SwappyTreasuryVault");
  const vault = await Vault.deploy(owner);
  await vault.waitForDeployment();
  const vaultAddress = await vault.getAddress();
  console.log("SwappyTreasuryVault:", vaultAddress);

  const Registry = await ethers.getContractFactory("SwappyDelegateRegistry");
  const registry = await Registry.deploy(owner);
  await registry.waitForDeployment();
  const registryAddress = await registry.getAddress();
  console.log("SwappyDelegateRegistry:", registryAddress);

  const Delegate = await ethers.getContractFactory("Swappy7702PayoutDelegate");
  const delegate = await Delegate.deploy();
  await delegate.waitForDeployment();
  const delegateAddress = await delegate.getAddress();
  console.log("Swappy7702PayoutDelegate:", delegateAddress);

  const delegateCode = await ethers.provider.getCode(delegateAddress);
  const delegateCodeHash = ethers.keccak256(delegateCode);
  console.log("Swappy7702PayoutDelegate.codeHash:", delegateCodeHash);

  await wait(delegate.initialize(owner, operator), "delegate initialize");

  if (configureOwnerOnly) {
    await wait(vault.setOperator(operator, true), "vault operator");
    for (const guardian of guardians) {
      await wait(vault.setGuardian(guardian, true), `vault guardian ${guardian}`);
      await wait(registry.setGuardian(guardian, true), `registry guardian ${guardian}`);
    }
    for (const recipient of recipients) {
      await wait(vault.setRecipientAllowed(recipient, true), `vault recipient ${recipient}`);
      await wait(delegate.setRecipientAllowed(recipient, true), `delegate recipient ${recipient}`);
    }
    if (token) {
      const maxTransfer = ethers.parseUnits(firstEnv("TREASURY_MAX_TRANSFER", "TREASURY_MAX_TRANSFER_USDT") || "100", tokenDecimals);
      const dailyLimit = ethers.parseUnits(firstEnv("TREASURY_DAILY_LIMIT", "TREASURY_DAILY_LIMIT_USDT") || "1000", tokenDecimals);
      await wait(vault.setTokenPolicy(token, true, maxTransfer, dailyLimit), `${tokenSymbol} vault policy`);
      await wait(delegate.setTokenAllowed(token, true), `${tokenSymbol} delegate token`);
      console.log(`${tokenSymbol} token configured:`, token);
      console.log("token decimals:", tokenDecimals);
    } else {
      console.log("token policy skipped: TREASURY_TOKEN_CONTRACT/BSC_USDT_CONTRACT/POLYGON_* not configured");
    }
    await wait(registry.trustDelegate(delegateAddress, delegateCodeHash, `ChainFX ${networkName} payout delegate`), "registry trust delegate");
  } else {
    console.log("manual next step: owner must configure vault/registry/delegate policies");
  }

  const deployment = {
    network: networkName,
    chainId: network.chainId.toString(),
    deployer: deployer.address,
    owner,
    operator,
    token: token || "",
    tokenSymbol,
    tokenDecimals,
    vault: vaultAddress,
    registry: registryAddress,
    delegate: delegateAddress,
    delegateCodeHash,
    configuredByDeployer: configureOwnerOnly,
    deployedAt: new Date().toISOString(),
  };
  writeDeployment(networkName, deployment);

  console.log("");
  console.log("env suggestions:");
  console.log(`TREASURY_CONTRACT=${vaultAddress}`);
  console.log(`DELEGATE_REGISTRY=${registryAddress}`);
  console.log(`CUSTODY_TRUSTED_DELEGATES=${delegateAddress}`);
  console.log(`CUSTODY_TRUSTED_DELEGATE_CODE_HASH=${delegateCodeHash}`);
}

async function wait(txPromise, label) {
  const tx = await txPromise;
  console.log(`${label} tx:`, tx.hash);
  await tx.wait();
}

function resolveTreasuryToken(networkName) {
  if (firstEnv("TREASURY_TOKEN_CONTRACT")) return firstEnv("TREASURY_TOKEN_CONTRACT");
  if (networkName === "polygon" || networkName === "polygonAmoy") {
    return firstEnv("POLYGON_TOKEN_CONTRACT", "POLYGON_USDC_CONTRACT", "POLYGON_USDT_CONTRACT");
  }
  return firstEnv("BSC_TOKEN_CONTRACT", "BSC_USDT_CONTRACT");
}

function defaultTokenDecimals(networkName) {
  if (networkName === "polygon" || networkName === "polygonAmoy") return "6";
  return "18";
}

function defaultTokenSymbol(networkName) {
  if (networkName === "polygon" || networkName === "polygonAmoy") return "USDC";
  return "USDT";
}

function assertAddress(value, label) {
  if (!ethers.isAddress(value)) {
    throw new Error(`${label} invalido: ${value}`);
  }
}

function writeDeployment(networkName, deployment) {
  const dir = path.resolve(__dirname, "..", "deployments");
  fs.mkdirSync(dir, { recursive: true });
  const file = path.join(dir, `${networkName}.json`);
  fs.writeFileSync(file, JSON.stringify(deployment, null, 2));
  console.log("deployment file:", file);
}

main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});

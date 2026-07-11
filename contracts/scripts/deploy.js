const { ethers, network: hardhatNetwork } = require("hardhat");

async function main() {
  const [deployer] = await ethers.getSigners();
  const network = await ethers.provider.getNetwork();
  const networkName = hardhatNetwork.name;
  const owner = process.env.CONTRACT_OWNER || deployer.address;
  const token = resolveTreasuryToken(networkName);
  const tokenSymbol = process.env.TREASURY_TOKEN_SYMBOL || defaultTokenSymbol(networkName);
  const tokenDecimals = Number(process.env.TREASURY_TOKEN_DECIMALS || defaultTokenDecimals(networkName));

  console.log("deployer:", deployer.address);
  console.log("owner:", owner);
  console.log("network:", networkName);
  console.log("chainId:", network.chainId.toString());

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

  if (owner === deployer.address && token) {
    const maxTransfer = ethers.parseUnits(process.env.TREASURY_MAX_TRANSFER || process.env.TREASURY_MAX_TRANSFER_USDT || "100", tokenDecimals);
    const dailyLimit = ethers.parseUnits(process.env.TREASURY_DAILY_LIMIT || process.env.TREASURY_DAILY_LIMIT_USDT || "1000", tokenDecimals);
    const tx = await vault.setTokenPolicy(token, true, maxTransfer, dailyLimit);
    await tx.wait();
    console.log(`${tokenSymbol} policy configured:`, token);
    console.log("token decimals:", tokenDecimals);
  } else {
    console.log("manual next step: configure token policy from owner wallet");
  }

  console.log("");
  console.log("env suggestions:");
  console.log(`CUSTODY_TRUSTED_DELEGATES=${delegateAddress}`);
  console.log(`TREASURY_CONTRACT=${vaultAddress}`);
  console.log(`DELEGATE_REGISTRY=${registryAddress}`);
}

function resolveTreasuryToken(networkName) {
  if (process.env.TREASURY_TOKEN_CONTRACT) return process.env.TREASURY_TOKEN_CONTRACT;
  if (networkName === "polygon" || networkName === "polygonAmoy") {
    return process.env.POLYGON_TOKEN_CONTRACT || process.env.POLYGON_USDC_CONTRACT || process.env.POLYGON_USDT_CONTRACT || "";
  }
  return process.env.BSC_TOKEN_CONTRACT || process.env.BSC_USDT_CONTRACT || "";
}

function defaultTokenDecimals(networkName) {
  if (networkName === "polygon" || networkName === "polygonAmoy") return "6";
  return "18";
}

function defaultTokenSymbol(networkName) {
  if (networkName === "polygon" || networkName === "polygonAmoy") return "USDC";
  return "USDT";
}

main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});

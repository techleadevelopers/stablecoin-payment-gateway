const { ethers } = require("ethers");
const { firstEnv, firstUrlEnv, loadContractEnv, normalizePrivateKey } = require("./env");

loadContractEnv();

const NETWORKS = {
  bsc: {
    chainId: 56,
    rpc: ["BSC_RPC_URL", "BSC_RPC_URLS", "RPC_URL"],
    token: ["TREASURY_TOKEN_CONTRACT", "BSC_TOKEN_CONTRACT", "BSC_USDT_CONTRACT"],
  },
  bscTestnet: {
    chainId: 97,
    rpc: ["BSC_TESTNET_RPC_URL", "BSC_TESTNET_RPC_URLS"],
    token: ["TREASURY_TOKEN_CONTRACT", "BSC_TOKEN_CONTRACT", "BSC_USDT_CONTRACT"],
  },
  polygon: {
    chainId: 137,
    rpc: ["POLYGON_RPC_URL", "POLYGON_RPC_URLS"],
    token: ["TREASURY_TOKEN_CONTRACT", "POLYGON_TOKEN_CONTRACT", "POLYGON_USDC_CONTRACT", "POLYGON_USDT_CONTRACT"],
  },
  polygonAmoy: {
    chainId: 80002,
    rpc: ["POLYGON_AMOY_RPC_URL", "POLYGON_TESTNET_RPC_URL", "POLYGON_AMOY_RPC_URLS"],
    token: ["TREASURY_TOKEN_CONTRACT", "POLYGON_TOKEN_CONTRACT", "POLYGON_USDC_CONTRACT", "POLYGON_USDT_CONTRACT"],
  },
};

async function main() {
  const requested = process.argv.includes("--network")
    ? process.argv[process.argv.indexOf("--network") + 1]
    : process.env.HARDHAT_NETWORK || "bsc";
  const spec = NETWORKS[requested];
  if (!spec) throw new Error(`rede nao suportada no preflight: ${requested}`);

  const privateKey = normalizePrivateKey(firstEnv("DEPLOYER_PRIVATE_KEY", "PRIVATE_KEY", "EVM_PRIVATE_KEY"));
  if (!privateKey) throw new Error("configure DEPLOYER_PRIVATE_KEY ou PRIVATE_KEY");
  const deployer = new ethers.Wallet(privateKey);
  const owner = firstEnv("CONTRACT_OWNER", "OWNER_ADDRESS") || deployer.address;
  const operator = firstEnv("CONTRACT_OPERATOR", "SIGNER_OPERATOR_ADDRESS", "OPERATOR_ADDRESS") || deployer.address;
  const rpc = firstUrlEnv(...spec.rpc);
  const token = firstEnv(...spec.token);

  if (!ethers.isAddress(owner)) throw new Error(`CONTRACT_OWNER invalido: ${owner}`);
  if (!ethers.isAddress(operator)) throw new Error(`CONTRACT_OPERATOR invalido: ${operator}`);
  if (token && !ethers.isAddress(token)) throw new Error(`token invalido: ${token}`);
  if (!rpc) throw new Error(`RPC nao configurado para ${requested}: ${spec.rpc.join(" ou ")}`);

  const provider = new ethers.JsonRpcProvider(rpc);
  const network = await provider.getNetwork();
  if (Number(network.chainId) !== spec.chainId) {
    throw new Error(`RPC chainId errado para ${requested}: recebido ${network.chainId}, esperado ${spec.chainId}`);
  }
  const balance = await provider.getBalance(deployer.address);

  console.log(JSON.stringify({
    ok: true,
    network: requested,
    chainId: Number(network.chainId),
    deployer: deployer.address,
    owner,
    operator,
    tokenConfigured: Boolean(token),
    deployerNativeWei: balance.toString(),
    ownerIsDeployer: owner.toLowerCase() === deployer.address.toLowerCase(),
  }, null, 2));

  if (balance === 0n) {
    throw new Error("deployer sem saldo nativo para gas");
  }
}

main().catch((error) => {
  console.error(error.message || error);
  process.exitCode = 1;
});

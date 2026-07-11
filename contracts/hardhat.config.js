require("@nomicfoundation/hardhat-toolbox");

const privateKey = process.env.DEPLOYER_PRIVATE_KEY || "";
const bscRpc = process.env.BSC_RPC_URL || process.env.RPC_URL || "";
const bscTestnetRpc = process.env.BSC_TESTNET_RPC_URL || "";

const accounts = privateKey ? [privateKey] : [];

module.exports = {
  paths: {
    sources: "./src",
    tests: "./test",
    cache: "./cache",
    artifacts: "./artifacts"
  },
  solidity: {
    version: "0.8.24",
    settings: {
      optimizer: {
        enabled: true,
        runs: 200
      }
    }
  },
  networks: {
    hardhat: {
      chainId: 31337
    },
    bsc: {
      url: bscRpc || "https://bsc-dataseed.binance.org/",
      chainId: 56,
      accounts
    },
    bscTestnet: {
      url: bscTestnetRpc || "https://data-seed-prebsc-1-s1.binance.org:8545/",
      chainId: 97,
      accounts
    }
  }
};

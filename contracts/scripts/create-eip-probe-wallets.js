const { ethers } = require("ethers");

function readCount(argv) {
  const countArg = argv.find((arg) => arg.startsWith("--count="));
  const raw = countArg ? countArg.slice("--count=".length) : argv[2];
  const count = raw ? Number.parseInt(raw, 10) : 3;

  if (!Number.isInteger(count) || count < 2 || count > 50) {
    throw new Error("wallet count must be an integer between 2 and 50");
  }

  return count;
}

function hasJsonFlag(argv) {
  return argv.includes("--json");
}

function main() {
  const count = readCount(process.argv);
  const json = hasJsonFlag(process.argv);
  const wallets = Array.from({ length: count }, (_, index) => {
    const wallet = ethers.Wallet.createRandom();
    return {
      index: index + 1,
      address: wallet.address,
      privateKey: wallet.privateKey,
      mnemonic: wallet.mnemonic?.phrase || ""
    };
  });

  const envValue = wallets.map((wallet) => wallet.privateKey).join(",");

  if (json) {
    console.log(JSON.stringify({
      env: {
        EIP_PROBE_WALLET_PRIVATE_KEYS: envValue
      },
      wallets
    }, null, 2));
    return;
  }

  console.log("EIP probe wallets generated.");
  console.log("");
  console.log("Add this line to the backend .env:");
  console.log(`EIP_PROBE_WALLET_PRIVATE_KEYS=${envValue}`);
  console.log("");
  console.log("Public addresses to fund on the selected testnet:");
  for (const wallet of wallets) {
    console.log(`${wallet.index}. ${wallet.address}`);
  }
  console.log("");
  console.log("Fund each address with native testnet gas and the test token used by the probe.");
  console.log("Do not use these wallets on mainnet.");
}

main();

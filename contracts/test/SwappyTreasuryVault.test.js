const { expect } = require("chai");
const { ethers } = require("hardhat");

describe("SwappyTreasuryVault", function () {
  async function deployFixture() {
    const [owner, operator, guardian, customer, attacker] = await ethers.getSigners();

    const Token = await ethers.getContractFactory("MockBEP20");
    const token = await Token.deploy("Mock USDT", "USDT", 18);
    await token.waitForDeployment();

    const Vault = await ethers.getContractFactory("SwappyTreasuryVault");
    const vault = await Vault.deploy(owner.address);
    await vault.waitForDeployment();

    const tokenAddress = await token.getAddress();
    const vaultAddress = await vault.getAddress();
    await token.mint(vaultAddress, ethers.parseUnits("10000", 18));
    await vault.setOperator(operator.address, true);
    await vault.setGuardian(guardian.address, true);
    await vault.setRecipientAllowed(customer.address, true);
    await vault.setTokenPolicy(tokenAddress, true, ethers.parseUnits("100", 18), ethers.parseUnits("500", 18));

    return { owner, operator, guardian, customer, attacker, token, tokenAddress, vault };
  }

  it("allows operator payout to allowed recipient", async function () {
    const { operator, customer, token, tokenAddress, vault } = await deployFixture();
    const opId = ethers.id("buy-1");

    await expect(vault.connect(operator).payout(opId, tokenAddress, customer.address, ethers.parseUnits("20", 18)))
      .to.emit(vault, "Payout");

    expect(await token.balanceOf(customer.address)).to.equal(ethers.parseUnits("20", 18));
  });

  it("blocks duplicate operation id", async function () {
    const { operator, customer, tokenAddress, vault } = await deployFixture();
    const opId = ethers.id("buy-duplicate");

    await vault.connect(operator).payout(opId, tokenAddress, customer.address, ethers.parseUnits("1", 18));
    await expect(
      vault.connect(operator).payout(opId, tokenAddress, customer.address, ethers.parseUnits("1", 18))
    ).to.be.revertedWithCustomError(vault, "OperationAlreadyExecuted");
  });

  it("rejects empty operation id explicitly", async function () {
    const { operator, customer, tokenAddress, vault } = await deployFixture();

    await expect(
      vault.connect(operator).payout(ethers.ZeroHash, tokenAddress, customer.address, ethers.parseUnits("1", 18))
    ).to.be.revertedWithCustomError(vault, "InvalidOperationId");
  });

  it("blocks non-allowed recipient", async function () {
    const { operator, attacker, tokenAddress, vault } = await deployFixture();

    await expect(
      vault.connect(operator).payout(ethers.id("bad-recipient"), tokenAddress, attacker.address, ethers.parseUnits("1", 18))
    ).to.be.revertedWithCustomError(vault, "RecipientNotAllowed");
  });

  it("blocks contract recipients by default even when recipient is allowlisted", async function () {
    const { owner, operator, tokenAddress, vault } = await deployFixture();

    await vault.connect(owner).setRecipientAllowed(tokenAddress, true);

    await expect(
      vault.connect(operator).payout(ethers.id("contract-recipient-blocked"), tokenAddress, tokenAddress, ethers.parseUnits("1", 18))
    ).to.be.revertedWithCustomError(vault, "ContractRecipientNotAllowed");
  });

  it("allows audited contract recipients only after explicit contract allowlist", async function () {
    const { owner, operator, token, tokenAddress, vault } = await deployFixture();

    await vault.connect(owner).setRecipientAllowed(tokenAddress, true);
    await expect(vault.connect(owner).setContractRecipientAllowed(tokenAddress, true))
      .to.emit(vault, "ContractRecipientAllowed")
      .withArgs(tokenAddress, true);

    await vault.connect(operator).payout(ethers.id("contract-recipient-approved"), tokenAddress, tokenAddress, ethers.parseUnits("1", 18));

    expect(await token.balanceOf(tokenAddress)).to.equal(ethers.parseUnits("1", 18));
  });

  it("reverts when an allowed token returns false", async function () {
    const { owner, operator, customer, vault } = await deployFixture();
    const FalseToken = await ethers.getContractFactory("MockFalseReturnERC20");
    const falseToken = await FalseToken.deploy();
    await falseToken.waitForDeployment();
    const falseTokenAddress = await falseToken.getAddress();
    await falseToken.mint(await vault.getAddress(), ethers.parseUnits("10", 18));
    await vault.connect(owner).setTokenPolicy(falseTokenAddress, true, ethers.parseUnits("10", 18), ethers.parseUnits("100", 18));

    await expect(
      vault.connect(operator).payout(ethers.id("false-token"), falseTokenAddress, customer.address, ethers.parseUnits("1", 18))
    ).to.be.revertedWithCustomError(vault, "TokenTransferFailed");
  });

  it("supports allowed tokens that do not return a boolean", async function () {
    const { owner, operator, customer, vault } = await deployFixture();
    const NoReturnToken = await ethers.getContractFactory("MockNoReturnERC20");
    const noReturnToken = await NoReturnToken.deploy();
    await noReturnToken.waitForDeployment();
    const noReturnTokenAddress = await noReturnToken.getAddress();
    await noReturnToken.mint(await vault.getAddress(), ethers.parseUnits("10", 18));
    await vault.connect(owner).setTokenPolicy(noReturnTokenAddress, true, ethers.parseUnits("10", 18), ethers.parseUnits("100", 18));

    await vault.connect(operator).payout(ethers.id("no-return-token"), noReturnTokenAddress, customer.address, ethers.parseUnits("1", 18));

    expect(await noReturnToken.balanceOf(customer.address)).to.equal(ethers.parseUnits("1", 18));
  });

  it("blocks token-driven reentrancy during payout", async function () {
    const { owner, operator, customer, vault } = await deployFixture();
    const ReentrantToken = await ethers.getContractFactory("MockReentrantERC20");
    const reentrantToken = await ReentrantToken.deploy();
    await reentrantToken.waitForDeployment();
    const reentrantTokenAddress = await reentrantToken.getAddress();
    const vaultAddress = await vault.getAddress();
    await reentrantToken.mint(vaultAddress, ethers.parseUnits("10", 18));
    await vault.connect(owner).setTokenPolicy(reentrantTokenAddress, true, ethers.parseUnits("10", 18), ethers.parseUnits("100", 18));
    await vault.connect(owner).setOperator(reentrantTokenAddress, true);
    await reentrantToken.configureReentry(vaultAddress, ethers.id("reentrant-inner"), customer.address, true);

    await vault.connect(operator).payout(ethers.id("reentrant-outer"), reentrantTokenAddress, customer.address, ethers.parseUnits("1", 18));

    expect(await reentrantToken.reenterBlocked()).to.equal(true);
    expect(await reentrantToken.balanceOf(customer.address)).to.equal(ethers.parseUnits("1", 18));
  });

  it("rejects adding an EOA to the contract-recipient allowlist", async function () {
    const { owner, customer, vault } = await deployFixture();

    await expect(vault.connect(owner).setContractRecipientAllowed(customer.address, true))
      .to.be.revertedWithCustomError(vault, "ContractRecipientNotAllowed");
  });

  it("guardian can pause payouts", async function () {
    const { operator, guardian, customer, tokenAddress, vault } = await deployFixture();

    await vault.connect(guardian).pause();
    await expect(
      vault.connect(operator).payout(ethers.id("paused"), tokenAddress, customer.address, ethers.parseUnits("1", 18))
    ).to.be.revertedWithCustomError(vault, "PausedError");
  });

  it("enforces daily limit", async function () {
    const { operator, customer, tokenAddress, vault } = await deployFixture();

    await vault.connect(operator).payout(ethers.id("daily-1"), tokenAddress, customer.address, ethers.parseUnits("100", 18));
    await vault.connect(operator).payout(ethers.id("daily-2"), tokenAddress, customer.address, ethers.parseUnits("100", 18));
    await vault.connect(operator).payout(ethers.id("daily-3"), tokenAddress, customer.address, ethers.parseUnits("100", 18));
    await vault.connect(operator).payout(ethers.id("daily-4"), tokenAddress, customer.address, ethers.parseUnits("100", 18));
    await vault.connect(operator).payout(ethers.id("daily-5"), tokenAddress, customer.address, ethers.parseUnits("100", 18));

    await expect(
      vault.connect(operator).payout(ethers.id("daily-6"), tokenAddress, customer.address, ethers.parseUnits("1", 18))
    ).to.be.revertedWithCustomError(vault, "DailyLimitExceeded");
  });

  it("caps batch payout size", async function () {
    const { operator, customer, tokenAddress, vault } = await deployFixture();
    const ids = Array.from({ length: 101 }, (_, i) => ethers.id(`batch-${i}`));
    const recipients = Array.from({ length: 101 }, () => customer.address);
    const amounts = Array.from({ length: 101 }, () => ethers.parseUnits("1", 18));

    await expect(
      vault.connect(operator).batchPayout(ids, tokenAddress, recipients, amounts)
    ).to.be.revertedWithCustomError(vault, "BatchTooLarge");
  });
});

// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

interface IVaultPayout {
    function payout(bytes32 operationId, address token, address to, uint256 amount) external;
}

contract MockReentrantERC20 {
    mapping(address => uint256) public balanceOf;
    IVaultPayout public vault;
    address public reenterTo;
    bytes32 public reenterOperationId;
    bool public reenter;
    bool public reenterBlocked;

    function mint(address to, uint256 amount) external {
        balanceOf[to] += amount;
    }

    function configureReentry(address vault_, bytes32 operationId, address to, bool enabled) external {
        vault = IVaultPayout(vault_);
        reenterOperationId = operationId;
        reenterTo = to;
        reenter = enabled;
        reenterBlocked = false;
    }

    function transfer(address to, uint256 amount) external returns (bool) {
        require(balanceOf[msg.sender] >= amount, "insufficient");
        if (reenter) {
            reenter = false;
            try vault.payout(reenterOperationId, address(this), reenterTo, 1) {
                reenterBlocked = false;
            } catch {
                reenterBlocked = true;
            }
        }
        balanceOf[msg.sender] -= amount;
        balanceOf[to] += amount;
        return true;
    }
}

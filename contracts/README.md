# ChainFX EVM Contracts

## Status atual no backend

O Gas Station/Paymaster integrado hoje roda majoritariamente off-chain:

- `internal/paymaster`: quote, relay, idempotencia, retry, batching e DLQ.
- `internal/rpc`: pool RPC e health checks.
- `signer/`: assinatura isolada e custody guard.
- `gas_relay_requests` e `auto_sweeper_runs`: auditoria/persistencia.

Os contratos deste diretorio continuam sendo a camada opcional de vault/delegates para governanca on-chain, limites e hardening. Nao sao requisito para publicar o MCP ou usar `/v1/gas/*` no corte atual.


Contratos editaveis para operar custodia/payout em redes EVM com foco em seguranca operacional. BSC continua sendo o caminho principal do core atual; Polygon foi adicionada como rede opcional para deploy do mesmo vault em liquidação/settlement de baixo custo.

## Contratos

### `ChainFXTreasuryVault`

Vault de treasury/payout para ERC20, como USDT/USDC em BSC ou Polygon.

Controles principais:

- owner em duas etapas;
- guardians com poder de pause/blocklist;
- operators para payout;
- allowlist de tokens;
- allowlist/blocklist de recipients;
- limite maximo por transferencia;
- limite diario por token;
- idempotencia por `operationId`;
- eventos para auditoria.

Uso recomendado:

```text
Owner multisig
        |
configura token, operadores, guardians e limites
        |
Core/signer solicita payout ao operator
        |
Vault envia USDT ao cliente respeitando limites
```

### `ChainFXDelegateRegistry`

Registry de delegates EIP-7702 confiaveis.

O signer Go ainda valida delegate e bytecode hash off-chain. O registry e uma fonte on-chain auditavel para governanca, incidentes e revogacao.

### `ChainFX7702PayoutDelegate`

Delegate EIP-7702 minimo para payout controlado.

Importante:

- nao possui `execute()` generico;
- nao permite chamada arbitraria;
- exige token permitido;
- exige recipient permitido;
- usa `operationId` para evitar replay;
- pode ser pausado.

Antes de colocar esse contrato em `CUSTODY_TRUSTED_DELEGATES`, faca deploy em testnet, registre o bytecode hash, teste com baixo saldo e valide o comportamento do signer Go em `CUSTODY_MODE=shadow` e depois `paper`.

## Setup

```powershell
cd C:\Users\Paulo\Desktop\payment-gateway\contracts
npm install
npm run compile
npm test
```

## Deploy BSC Testnet

```powershell
$env:DEPLOYER_PRIVATE_KEY="0x..."
$env:CONTRACT_OWNER="0xMultisigOuOwner"
$env:BSC_TESTNET_RPC_URL="https://data-seed-prebsc-1-s1.binance.org:8545/"
npm run deploy:testnet
```

## Deploy BSC Mainnet

```powershell
$env:DEPLOYER_PRIVATE_KEY="0x..."
$env:CONTRACT_OWNER="0xMultisigOuOwner"
$env:BSC_RPC_URL="https://..."
$env:BSC_USDT_CONTRACT="0x55d398326f99059fF775485246999027B3197955"
$env:TREASURY_MAX_TRANSFER_USDT="100"
$env:TREASURY_DAILY_LIMIT_USDT="1000"
npm run deploy:bsc
```

## Deploy Polygon Amoy

Polygon Amoy é a testnet atual para Polygon PoS. Chain ID `80002`; mainnet Polygon PoS usa chain ID `137`. A Polygon mantém instruções oficiais para adicionar Polygon/Amoy via ChainList/MetaMask, e a página de Amoy informa RPC `https://rpc-amoy.polygon.technology/` e chain ID `80002`.

```powershell
$env:DEPLOYER_PRIVATE_KEY="0x..."
$env:CONTRACT_OWNER="0xMultisigOuOwner"
$env:POLYGON_AMOY_RPC_URL="https://rpc-amoy.polygon.technology/"
$env:TREASURY_TOKEN_CONTRACT="0xTokenUSDCouUSDTNaAmoy"
$env:TREASURY_TOKEN_SYMBOL="USDC"
$env:TREASURY_TOKEN_DECIMALS="6"
$env:TREASURY_MAX_TRANSFER="100"
$env:TREASURY_DAILY_LIMIT="1000"
npm run deploy:polygon-amoy
```

## Deploy Polygon Mainnet

Use Polygon para capability settlement/payout quando fizer sentido reduzir custo de gas ou atender providers que já liquidam em Polygon. Nao mude o fluxo principal BSC sem antes adaptar signer/core para aceitar `POLYGON` ponta a ponta.

```powershell
$env:DEPLOYER_PRIVATE_KEY="0x..."
$env:CONTRACT_OWNER="0xMultisigOuOwner"
$env:POLYGON_RPC_URL="https://polygon-rpc.com/"
$env:POLYGON_USDC_CONTRACT="0x2791Bca1f2de4661ED88A30C99A7a9449Aa84174"
$env:TREASURY_TOKEN_SYMBOL="USDC"
$env:TREASURY_TOKEN_DECIMALS="6"
$env:TREASURY_MAX_TRANSFER="100"
$env:TREASURY_DAILY_LIMIT="1000"
npm run deploy:polygon
```

Depois do deploy, o script imprime:

```env
CUSTODY_TRUSTED_DELEGATES=0x...
TREASURY_CONTRACT=0x...
DELEGATE_REGISTRY=0x...
```

Preencha `CUSTODY_TRUSTED_DELEGATES` somente com delegate auditado e validado. Nunca use placeholder como `0xContratoDelegateSeguro`.

## Politica Recomendada

- `owner`: multisig ou carteira operacional separada, nunca a mesma hot wallet do payout.
- `guardian`: carteira capaz de pausar rapidamente em incidente.
- `operator`: signer/operador com limite baixo.
- `TREASURY_MAX_TRANSFER` ou `TREASURY_MAX_TRANSFER_USDT`: comece pequeno.
- `TREASURY_DAILY_LIMIT` ou `TREASURY_DAILY_LIMIT_USDT`: limite menor que o saldo total da hot wallet.
- `TREASURY_TOKEN_DECIMALS`: BSC USDT costuma usar 18; Polygon USDC/USDT usa 6. Confira o contrato antes de configurar limites.
- `CUSTODY_MODE=shadow` primeiro, depois `paper`.

## O Que Nao Fazer

- Nao coloque todos os fundos no vault antes de testar em testnet.
- Nao use delegate EIP-7702 com `execute()` generico.
- Nao permita token contract aberto.
- Nao use owner EOA sem backup/multisig para saldo alto.
- Nao configure `CUSTODY_TRUSTED_DELEGATES` com contrato sem bytecode auditado.

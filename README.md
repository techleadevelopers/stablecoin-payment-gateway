# Financial Product Interface

<div align="center">
  <img src="https://res.cloudinary.com/limpeja/image/upload/v1783198512/d61ccb4b-f711-4a99-b859-1b6f9a5c18fb.png" alt="ChainFx Logo" width="1024" />
</div>

---

## 📱 ChainFx - Instant PIX to Stablecoin Payments

**ChainFx** é uma plataforma Web3 que permite comprar e vender stablecoins como USDT (Tether.io) e EURUSD (Digital Euro Dollar) de forma instantânea e segura. Com integração via PIX, você pode realizar transações em segundos com total confiabilidade.

### ✨ Diferenciais da Plataforma

- ⚡ **Compre e venda cripto instantaneamente** via PIX
- 🔒 **Transações seguras** e sem complicações
- 👥 **950.000+ usuários** confiam na ChainFx
- 💳 **30+ opções** de pagamento locais
- 🪙 **100+ criptomoedas** disponíveis

---

## 🛒 Fluxo de Compra (Buy) - Step 1

### Informe o valor e visualize a cotação

<div align="center">
  <img src="https://res.cloudinary.com/limpeja/image/upload/v1783058374/compra-removebg-preview_ikab4t.png" alt="ChainFx - Tela de Compra" width="600" />
</div>

**Como funciona:**

1. Selecione a moeda que deseja pagar (BRL)
2. Informe o valor que deseja comprar
3. Visualize a cotação atualizada em tempo real
4. Confirme a quantidade de cripto que irá receber

---

## 💳 Fluxo de Pagamento - Step 2

### Insira sua wallet e escolha o método de pagamento

<div align="center">
  <img src="https://res.cloudinary.com/limpeja/image/upload/v1783064002/image-removebg-preview_6_ete3hd.png" alt="ChainFx - Tela de Pagamento" width="680" />
</div>

**Como funciona:**

1. **Informe sua Wallet** - Cole o endereço da sua carteira (ETH, BTC, USDT)
2. **Escolha o método de pagamento**:
   - 💰 **PIX** - Instantâneo e sem taxas extras
   - 💳 **VISA** - Cartão de crédito internacional
   - 💳 **Mastercard** - Cartão de crédito internacional
3. **Confirme a transação** e receba suas criptos em segundos

---

## 💳 Fluxo de Pagamento - Step 3 (PIX)

### Escaneie o QR Code e confirme o pagamento

<div align="center">
  <img src="https://res.cloudinary.com/limpeja/image/upload/v1783064178/image-removebg-preview_7_ighwcw.png" alt="ChainFx - Tela de Pagamento PIX" width="680" />
</div>

**Como funciona:**

1. **Escaneie o QR Code** - Utilize o app do seu banco para escanear o código PIX
2. **Copie o código PIX** - Caso prefira, copie o código e cole no seu banco
3. **Confirme o pagamento** - Realize o pagamento no valor exibido
4. **Receba suas criptos** - Após a confirmação do pagamento, suas criptos serão entregues em segundos

---

## 💳 Fluxo de Pagamento - Step 3 (Cartão de Crédito - Stripe)

### Integração em andamento!

<div align="center">
  <img src="https://res.cloudinary.com/limpeja/image/upload/v1783064734/998ededc-2291-40d7-86c9-6906faea7998_lsbpws.png" alt="ChainFx - Tela de Pagamento" width="480" />
</div>

**Pagamento com cartão via Stripe estará disponível em breve.**

- 💳 **VISA** - Cartão de crédito internacional
- 💳 **Mastercard** - Cartão de crédito internacional

*Por enquanto, utilize PIX para compras instantâneas.*


## 🔄 Fluxo de Venda (Sell)

### Venda suas criptos e receba em reais

1. Selecione a criptomoeda que deseja vender
2. Informe a quantidade
3. Escolha o método de recebimento (PIX)
4. Confirme a transação e receba em sua conta

---


# ChainFx Payment Gateway

Backend Go para settlement cripto programatico.

ChainFx opera como um **payment and liquidity rail** com duas entradas:

- **Human rail**: usuario compra/vende USDT usando PIX e recebe settlement na wallet ou em BRL.
- **Machine rail**: agentes, bots e sistemas descobrem capacidades via manifesto, criam intents, pagam on-chain e recebem settlement stablecoin ou acesso de API.

Em uma frase:

```text
ChainFx lets humans and autonomous agents move between local payments, stablecoins and programmable settlement.
```

Este README foca o produto e o fluxo principal. Detalhes profundos de mobile, custodia, dashboards e modulos internos devem ficar em documentos separados quando forem extraidos:

- `ARCHITECTURE.md`: desenho tecnico completo.
- `SECURITY.md`: signer, treasury, EIP-7702, auditoria e limites.
- `DEVELOPERS.md`: API, SDKs, webhooks e exemplos.
- `MOBILE.md`: app, WebSocket, DCA e experiencia mobile.

## Indice

1. [Arquitetura de Camadas](#arquitetura-de-camadas)
2. [Machine-to-Machine](#machine-to-machine)
3. [Sobre o ChainFx](#sobre-o-chainfx)
4. [Fluxo do Cliente](#fluxo-do-cliente)
5. [Principais Capacidades](#principais-capacidades)
6. [Arquitetura Tecnica](#arquitetura-tecnica)
7. [Deploy](#deploy)
8. [Documentacao Tecnica](#documentacao-tecnica)
9. [Licenca](#licenca)

## Arquitetura de Camadas

```text
                    PAYMENT GATEWAY CORE

                           |
        +------------------+------------------+
        |                                     |
        v                                     v

 HUMAN LAYER                         MACHINE LAYER

 Web App                             Agent API
 Mobile App                          MCP / OpenAPI
 Dashboard                           Machine-to-Machine
 Checkout                            Autonomous Payments

        |                                     |
        |                                     |
        v                                     v

 Users                              AI Agents / Bots / Systems


                Shared Infrastructure

        - Ledger
        - Wallets
        - Balances
        - Transactions
        - Risk Engine
        - Settlement
        - Blockchain Workers
        - Notifications
```

## Machine-to-Machine

O objetivo da camada M2M e permitir que software compre liquidez, settlement ou acesso de API sem interface humana.

O agente nao precisa ler uma landing page. Ele precisa de discovery previsivel, resposta tipada e estados claros.

### Discovery para agentes

Fluxo de descoberta:

```text
AI Agent
  |
  | GET /.well-known/ai-services.json
  v
descobre /agent/v1/capabilities
  |
  | GET /agent/v1/capabilities
  v
entende capacidades, assets, lifecycle, taxas e seguranca
  |
  | GET /agent/v1/assets
  v
escolhe par stablecoin habilitado
  |
  | POST /agent/v1/trade/quote
  v
paga on-chain
  |
  | POST /agent/v1/trade/execute
  v
recebe settlement
```

Endpoints de discovery:

- `/.well-known/ai-services.json`: porta de entrada pequena e previsivel.
- `/agent/v1/capabilities`: manifesto detalhado para agentes.
- `/agent/v1/assets`: ativos habilitados, taxas, minimos e status.
- `/openapi.json`: contrato HTTP.
- `/mcp/initialize`: entrada MCP.
- `/.well-known/x402.json`: discovery de pagamento.
- `/llms.txt`, `/sitemap.xml`, `/robots.txt`: descoberta por crawlers e LLMs.

### ChainFX Marketplace MCP

O MCP passa a ser o canal natural de aquisicao para agentes. A ideia de produto e:

```text
Official MCP Registry
  -> ChainFX MCP
  -> Agent installs / connects
  -> searchCapabilities()
  -> purchaseCapability()
  -> pay stablecoin on-chain
  -> receive access token
  -> executeCapability()
  -> metering + billing + settlement
```

O MCP Registry resolve a descoberta de servidores. A ChainFX resolve a parte que ainda nao tem padrao dominante: compra, monetizacao, liquidacao e acesso por capability.

Posicionamento de Fase 5:

```text
ChainFX Capability Network
```

O Marketplace continua existindo como compatibilidade de endpoints, mas o produto estrategico passa a ser uma rede economica de capacidades:

```text
Agent
  -> Capability Discovery
  -> Capability Contract
  -> Capability Execution
  -> Usage Metering
  -> Billing
  -> Settlement
  -> Receipt
```

O agente nao precisa escolher diretamente OpenAI, Azure, AWS ou outro provider. Ele escolhe uma capacidade:

- `document_ocr`
- `aml_screening`
- `payments_fx`
- `llm_chat`
- `semantic_memory`
- `capability_discovery`

A ChainFX seleciona a rota/provider internamente conforme politica, prioridade, disponibilidade, custo, latencia, qualidade e fallback. No corte atual, o Capability Router e hibrido: metering, quota, billing ledger e settlement da purchase sao reais; `semantic_memory` executa nativamente em Postgres; `llm_chat` usa provider OpenAI-compatible quando `OPENAI_API_KEY` estiver configurada; `document_ocr` usa um adapter HTTP quando `CAPABILITY_OCR_URL` estiver configurada. Sem essas configuracoes, a rota tenta fallback de provider e depois cai para `mock_dev` com evento auditavel.

Roteamento de Fase 4:

- `best_available`: usa prioridade operacional, taxa de sucesso, custo e latencia.
- `cheapest`: prioriza menor `cost_score`.
- `lowest_latency`: prioriza menor `latency_ms`.
- `highest_quality`: prioriza maior `quality_score` e `success_rate_bps`.
- `region`: permite restringir rota por regiao, preservando providers `global`.
- `maxLatencyMs` e `maxCostScore`: permitem politica empresarial por chamada.
- `requireReal`: filtra apenas providers ativos/reais.
- fallback: tenta o provider selecionado, depois candidatos reais ordenados, depois mock/dev auditavel.

Productionization de Fase 5:

- contratos versionados de capability em `/marketplace/capabilities/{id}/contract`;
- ferramenta MCP `getCapabilityContract`;
- recurso MCP `chainfx://capability-contracts/{id}`;
- telemetria real de execucao gravada em `marketplace_execution_events.latency_ms`;
- atualizacao automatica de `latency_ms` e `success_rate_bps` em `marketplace_provider_policies`;
- narrativa pronta para MCP Registry como `ChainFX Capability Network MCP`;
- endpoints antigos `/marketplace/products`, `/marketplace/purchase` e `/v1/access/*` preservados como compatibilidade.

Adapters iniciais da Fase 3:

- `semantic_memory`: provider `chainfx-memory`, operacoes `save_memory`, `get_memory`, `semantic_search` e `knowledge_lookup`.
- `llm_chat`: provider `openai`, operacoes `generate_text`, `chat`, `summarize` e `classify`, via `OPENAI_BASE_URL` + `OPENAI_API_KEY`.
- `document_ocr`: provider `chainfx-ocr-http`, operacoes `extract_text`, `parse_invoice` e `parse_document`, via `CAPABILITY_OCR_URL`.

Variaveis opcionais para providers reais:

- `OPENAI_API_KEY`
- `OPENAI_MODEL`
- `OPENAI_BASE_URL`
- `CAPABILITY_OCR_URL`
- `CAPABILITY_OCR_API_KEY`

Ferramentas MCP expostas:

- `searchCapabilities`
- `listCapabilities`
- `getCapability`
- `getCapabilityContract`
- `purchaseCapability`
- `getPurchase`
- `executeCapability`
- `chooseRoute`
- `getUsage`
- `listAssets`
- `quote`
- `trade`
- `settlementStatus`

Recursos MCP expostos:

- `chainfx://marketplace/capabilities`
- `chainfx://capability-contracts/{id}`
- `chainfx://marketplace/products`
- `chainfx://agent/assets`
- `chainfx://rates/latest`

Fluxo recomendado para agentes:

```text
1. initialize ChainFX MCP
2. listCapabilities()
3. getCapability("document_ocr")
4. getCapabilityContract("document_ocr")
5. purchaseCapability({ capability: "document_ocr", ... })
6. pay on-chain using the returned payment intent
7. submit receipt through /marketplace/purchase/{id}/execute
8. chooseRoute({ capability: "document_ocr", routingMode: "lowest_latency" })
9. executeCapability({ capability: "document_ocr", accessToken, routingMode: "lowest_latency", ... })
10. inspect getUsage() / settlementStatus()
```

Publicacao no MCP Registry:

- Nome tecnico preservado: `chainfx-mcp`
- Titulo publico sugerido: `ChainFX Capability Network MCP`
- Categoria: payments, capability network, agent tools, stablecoin settlement, metered execution
- Descricao curta: `Discover, execute, meter, bill and settle digital capabilities for AI agents with stablecoin payments.`
- Manifesto pronto: `.mcp/server.json`
- Nome de registry atual para GitHub auth: `io.github.techleadevelopers/chainfx-mcp`
- Nome de registry recomendado para dominio ChainFX: `store.chainfx/capability-network`
- Requisitos: URL publica HTTPS para `/mcp/initialize`, API key ChainFX e documentacao de auth.

Antes de publicar:

```bash
mcp-publisher --help
mcp-publisher login github
mcp-publisher publish .mcp/server.json
```

O nome de dominio `store.chainfx/capability-network` deve ser publicado com autenticacao/verificacao de dominio. Com GitHub auth, o `.mcp/server.json` usa:

```json
{
  "name": "io.github.techleadevelopers/chainfx-mcp"
}
```

O servidor remoto publicado no registry aponta para:

```text
https://stablecoin-payment-gateway-production-3ee2.up.railway.app/mcp/initialize
```

Header obrigatorio para clientes MCP:

```text
Authorization: Bearer <CHAINFX_API_KEY>
```

Posicionamento publico:

```text
ChainFX Capability Network lets AI agents discover, purchase, execute,
meter, bill and settle digital capabilities through one MCP integration.
```

O `.well-known` deve continuar pequeno e apontar para URLs canonicas:

```json
{
  "capabilitiesUrl": "/agent/v1/capabilities",
  "assetsUrl": "/agent/v1/assets",
  "openapiUrl": "/openapi.json",
  "mcpUrl": "/mcp/initialize",
  "x402Url": "/.well-known/x402.json"
}
```

### Capacidades M2M

Capacidades publicadas:

```json
{
  "service": "chainfx",
  "version": "1.0",
  "capabilities": [
    "stablecoin_exchange",
    "api_access_purchase",
    "mcp_tools"
  ],
  "networks": [
    {
      "name": "BNB Smart Chain",
      "chainId": 56
    }
  ]
}
```

Trade lifecycle publicado para agentes:

```json
{
  "tradeLifecycle": [
    "discover_capabilities",
    "list_assets",
    "create_trade_intent",
    "pay_onchain",
    "submit_tx",
    "verify_receipt",
    "settle",
    "check_status"
  ]
}
```

Estados reais do intent:

```json
{
  "tradeStates": {
    "transient": ["pending", "paid"],
    "terminal": ["settled", "expired", "failed"],
    "retryable": ["failed"]
  }
}
```

### Stablecoin rail

Primeiro corte comercial:

- `USDC -> USDT`
- `USDT -> USDC`

`BUSD` continua suportado tecnicamente no registry, mas fica marcado como:

```yaml
BUSD:
  enabled: false
  status: legacy
```

Isso evita construir o produto novo em cima de um ativo legado, mantendo compatibilidade operacional se algum caso especifico exigir.

### Taxa M2M

Taxa padrao:

```text
gatewayFeeBps = 600
feeCalculation = deducted_from_gross_payment
```

Formula:

```text
receiveAmount = payAmount * (1 - feeBps / 10000)
payAmount = receiveAmount / (1 - feeBps / 10000)
```

Exemplo para o agente receber `500 USDT`:

```json
{
  "receiveAmount": "500.000000",
  "payAmount": "531.914894",
  "feeBps": 600,
  "feeAmount": "31.914894",
  "feeCalculation": "deducted_from_gross_payment"
}
```

Pagamento maior que o esperado e aceito apenas pelo valor do intent. O excedente e registrado como `overpaymentAmount` e nao recebe refund automatico no primeiro corte.

### Seguranca M2M

Protecoes implementadas no core:

- `nonce` por intent.
- `request_hash` amarrado a wallet, ativos, contratos, valores, destino e expiracao.
- `idempotency_key`.
- identificador de pagamento por `chain_id + tx_hash + log_index`.
- validacao de `chainId == 56`.
- `receipt.status == success`.
- contrato ERC20 exatamente cadastrado no registry.
- evento `Transfer`.
- `from == agentWallet`.
- `to == paymentAddress`.
- `amount >= requiredPayAmount`.
- pelo menos uma confirmacao.
- validacao basica de `blockHash` canonico.
- lock no banco antes do settlement.
- signer isolado para transferencia da treasury.

Metadados persistidos por pagamento:

```text
chain_id
tx_hash
log_index
block_number
block_hash
token_contract
transfer_from
transfer_to
transfer_amount_raw
overpayment_amount
```

### Autenticacao

Disponivel agora:

```json
{
  "authentication": {
    "current": [
      {
        "type": "api_key",
        "status": "available"
      },
      {
        "type": "onchain_payment_receipt",
        "status": "available"
      }
    ],
    "planned": [
      {
        "type": "wallet_signature",
        "status": "planned",
        "enabled": false
      }
    ]
  }
}
```

Wallet signature ainda nao deve ser interpretada como disponivel. O modelo planejado:

```text
X-ChainFX-Wallet
X-ChainFX-Timestamp
X-ChainFX-Nonce
X-ChainFX-Request-Hash
X-ChainFX-Signature
```

No primeiro corte, `agentWallet` e `payerWallet` devem ser iguais. A API pode evoluir para separar:

```json
{
  "agentWallet": "0x...",
  "payerWallet": "0x...",
  "destinationWallet": "0x..."
}
```

### API access para agentes

Alem do rail de liquidez, agentes podem comprar acesso temporario de API/MCP:

- `/marketplace/apis`
- `/marketplace/apis/{id}`
- `/v1/access/quote`
- `/v1/access/purchase`
- `/v1/access/{id}`
- `/v1/meter/usage`

Modelo de dados:

- `api_products`: capacidades/produtos.
- `api_payments`: quote/payment intent.
- `api_access_grants`: token temporario, quota e validade.
- `api_usage_events`: consumo auditavel.
- `agent_wallets`: historico operacional por wallet.

## Sobre o ChainFx

ChainFx permite comprar e vender stablecoins como USDT de forma rapida, com integracao via PIX e entrega cripto para wallet BSC.

Principais diferenciais:

- Compra de USDT via PIX.
- Cotacao travada por janela configuravel.
- Webhook de pagamento com HMAC.
- Delivery cripto assinado por signer isolado.
- Auditoria por ordem, request ID, provider ID e hash on-chain.
- LGPD por minimizacao, hash e criptografia AES-GCM nos dados sensiveis de SELL.

## Fluxo do Cliente

### BUY BRL via Pix

1. Usuario informa quanto quer pagar em BRL.
2. API retorna cotacao e quantidade estimada de USDT.
3. Usuario informa wallet BSC.
4. Gateway cria `buy_order` em `aguardando_pix`.
5. Cliente paga o PIX.
6. Webhook bancario confirma pagamento.
7. Gateway marca `pago_fiat`.
8. `BuySendWorker` entrega USDT para a wallet do cliente.
9. Ordem recebe `tx_hash_out` e `delivered_at`.

Fluxo esperado:

```text
Cliente paga Pix -> Webhook confirma -> BuySendWorker dispara da wallet ChainFx -> USDT chega na wallet do cliente
```

### SELL USDT -> Pix

1. Usuario informa chave PIX e valor BRL.
2. Usuario escolhe a rede de deposito: `BSC` por padrao, ou `POLYGON` quando habilitada no backend.
3. Monitor on-chain confirma deposito USDT.
4. `PayoutWorker` liquida PIX para o usuario.

Polygon no sell e opt-in. O fluxo web/mobile existente continua usando BSC se nenhuma rede for enviada. Para aceitar `POL`, `POLYGON` ou `MATIC`, configure RPC e contrato Polygon no backend; a ordem e salva com `network=POLYGON`, cursor separado e decimals 6.

## Principais Capacidades

- API publica em `cmd/api`.
- Workers concorrentes em `internal/workers`.
- Persistencia PostgreSQL em `internal/database`.
- Webhooks PIX e Stripe com idempotencia.
- SSE para acompanhamento de status.
- Healthcheck `/healthz` e readiness `/readyz`.
- Benchmark do fluxo PIX -> delivery em `cmd/benchflow`.
- Deploy por `Dockerfile` e `railway.json`.

## ChainFX Developers API

ChainFX posiciona o gateway como **Digital FX Payments Infrastructure**:

```text
PIX -> ChainFX API -> USDT na wallet
USDT -> ChainFX API -> PIX BRL na conta
```

### Status tecnico

Implementado neste repositorio:

- Fase 1: API REST, webhooks basicos, sandbox operacional e documentacao inicial.
- Fase 2: Developer Dashboard, API keys, logs operacionais e retry de webhook.
- Fase 3: SDK Node, SDK Python, OpenAPI e exemplos de integracao.
- Fase 4: MCP server, OpenAI Agents, webhooks n8n/Zapier/Make e retry system com fila e backoff.

Planejado para integracao futura:

- Fase 5: expansao de assets, paises e rails adicionais.

Nao faz parte do escopo atual:

- Bridge entre redes.
- Pool, AMM, DEX, LP ou yield.
- Custodia multi-chain complexa fora do fluxo PIX <> stablecoin.

### API REST

Endpoints principais implementados:

- `GET /rates`
- `POST /quote`
- `POST /buy`
- `POST /sell`
- `GET /order/{id}`
- `POST /webhooks/test`
- `POST /webhooks/retry`
- `GET /developers/dashboard`
- `GET /developers/logs`
- `GET /developers/api-keys`
- `GET /openapi.json`

### Autenticacao

```http
Authorization: Bearer sk_live_xxx
```

Modelo de keys:

- Public test: `pk_test_xxx`
- Secret test: `sk_test_xxx`
- Public live: `pk_live_xxx`
- Secret live: `sk_live_xxx`

As chamadas server-to-server usam secret key no header `Authorization`. Public keys ficam reservadas para futuras experiencias client-side ou identificacao publica.

### Webhooks

Eventos documentados:

- `payment.created`
- `payment.completed`
- `payment.failed`
- `order.confirmed`
- `crypto.sent`
- `crypto.confirmed`
- `order.failed`

Payload padrao:

```json
{
  "event": "payment.completed",
  "orderId": "ord_123",
  "status": "paid",
  "asset": "USDT",
  "amount": "96.52",
  "timestamp": "2026-07-04T00:00:00Z"
}
```

Retry tecnico:

- `POST /webhooks/retry` reconstrui o payload de uma ordem existente.
- Se `targetUrl` for informado, o gateway faz um `POST` real para a URL.
- A entrega inclui `X-ChainFX-Event` e `X-ChainFX-Signature`.

### Sandbox

O ambiente sandbox fica separado conceitualmente em:

```text
https://sandbox-api.chainfx.com
```

No desenvolvimento local, use:

```text
http://localhost:8080
```

Recursos previstos/implementados no modo sandbox:

- PIX fake.
- QR fake.
- wallet fake.
- webhooks simulados.
- ordens de teste.
- API keys test.

### Developer Dashboard

Rotas operacionais:

- `/developers`
- `/developers/dashboard`
- `/developers/dashboard.json`
- `/developers/logs`
- `/developers/api-keys`

O dashboard e protegido por API key ChainFX (`Authorization: Bearer ...` ou `?apiKey=...`) e mostra:

- API keys mascaradas.
- API logs reais de `api_request_logs`, sem payload sensivel e sem API key em texto aberto.
- MCP tool calls de `mcp_tool_logs`, incluindo tool, status, erro e latencia.
- Purchases recentes do marketplace/capability exchange.
- Usage/execution events de `marketplace_execution_events`.
- Status agregado de webhooks, assinaturas ativas, entregas e falhas.

Tabelas operacionais:

- `api_request_logs`
- `mcp_tool_logs`
- `marketplace_purchases`
- `marketplace_execution_events`
- `webhook_subscriptions`
- `webhook_deliveries`

Seguranca:

- API keys completas nunca sao gravadas nos logs do dashboard.
- Apenas hash curto da key e salvo para correlacao operacional.
- Payloads de requests HTTP nao sao persistidos.
- `mock_dev` e seed fixtures do Capability Layer aparecem como nao producao no discovery.

### Artefatos da Fase 3

- Node SDK: `sdk/node`
- Python SDK: `sdk/python`
- OpenAPI estatico: `docs/openapi/chainfx.openapi.json`
- Exemplos Node: `examples/node`
- Exemplos Python: `examples/python`

### Exemplo Node

```js
import { ChainFX } from "./sdk/node/index.js";

const chainfx = new ChainFX({
  apiKey: process.env.CHAINFX_API_KEY,
  baseUrl: process.env.CHAINFX_API_BASE_URL || "https://sandbox-api.chainfx.com"
});

const order = await chainfx.buy({
  fiat: "BRL",
  asset: "USDT",
  amount: 500,
  wallet: "0x000000000000000000000000000000000000dEaD",
  customer: {
    name: "Maria Silva",
    email: "maria@example.com",
    cpf: "12345678909",
    phone: "11999999999",
    birthDate: "1990-05-20",
    address: {
      line1: "Av Paulista",
      number: "1000",
      city: "Sao Paulo",
      state: "SP",
      postalCode: "01310100",
      country: "BR"
    }
  }
});
```

Como o fluxo de compra por API funciona:

1. O dev envia `POST /buy` com valor, asset, wallet e dados do comprador.
2. O backend cria uma `buy_order` com `id/buyId` e `accessToken`.
3. O backend envia os dados do comprador e o valor para o PagBank para gerar a cobranca PIX.
4. A resposta retorna `qrCodeUrl`, `pixKey`, `payment`, `orderUrl` e `statusUrl`.
5. O sistema do dev renderiza o QR Code ou apresenta o payload PIX ao pagador.
6. Quando o usuario paga, o webhook PagBank chama o backend.
7. O backend marca a compra como paga.
8. O worker de entrega envia USDT para a wallet informada.
9. O dev acompanha por `GET /order/{id}?accessToken=...` ou webhooks.

Campos de comprador aceitos hoje em `/buy`:

- `customer.name`
- `customer.email`
- `customer.cpf`
- `customer.phone`
- `customer.birthDate`
- `customer.address`

Esses campos sao enviados ao provider de pagamento quando disponiveis. Para auditoria local, o backend guarda hashes/minimizacao e nao precisa manter PII completa em claro.

### Exemplo Python

```python
from chainfx import ChainFX

chainfx = ChainFX(api_key="sk_test_chainfx_local", base_url="http://localhost:8080")
quote = chainfx.quote(side="buy", fiat="BRL", asset="USDT", amount=500)
```

### Roadmap futuro

Fase 4 integrada com foco em automacao e IA:

- MCP server (`/mcp/initialize`, `/mcp/tools/*`, `/mcp/resources/*`, `/mcp/prompts/list`).
- OpenAI Agents (`/api/agents/*`): analise de mercado, recomendacao, deteccao de anomalias, previsao de preco, resumo de transacoes.
- Webhooks de automacao n8n/Zapier/Make (`/api/webhooks/subscriptions`), com fila de retry, backoff, registro e logs de entrega.

Fase 5 sera integrada futuramente com foco em expansao operacional:

- mais assets alem de USDT;
- mais paises;
- mais rails locais;
- limites e compliance por mercado;
- SDKs adicionais como Go e PHP.

## Segurança de Custódia com EIP-7702

O signer Go inclui uma camada opcional de proteção de custódia baseada em EIP-7702. O objetivo não é executar arbitragem nem alterar o fluxo PIX, mas proteger a hot wallet contra delegações inesperadas de conta EOA.

O EIP-7702 introduz transações `SET_CODE` (`type 0x04`) com `authorizationList`, permitindo que uma EOA autorize temporariamente/de forma controlada a execução de código delegado. Isso é poderoso para account abstraction, batching e session keys, mas também cria um novo risco operacional: se a hot wallet autorizar um delegate desconhecido ou comprometido, a custódia pode ser afetada.

Por isso o signer tem um `CustodyGuard`:

```text
Signer monitora pending/latest blocks
-> detecta transações EIP-7702 type 0x04
-> lê authorizationList
-> recupera a authority/wallet que assinou a autorização
-> se a authority for uma wallet protegida e o delegate não estiver na allowlist:
   signer entra em lockdown
   /hd/transfer deixa de assinar novas saídas
```

Configuração opcional no serviço do signer:

```env
CUSTODY_GUARD_ENABLED=true
CUSTODY_GUARD_POLL_MS=1500
CUSTODY_TRUSTED_DELEGATES=
CUSTODY_ALLOWED_SELECTORS=
CUSTODY_PROTECTED_WALLETS=
```

A hot wallet derivada de `EVM_PRIVATE_KEY` entra automaticamente na lista protegida. `CUSTODY_PROTECTED_WALLETS` serve para adicionar outras carteiras. `CUSTODY_TRUSTED_DELEGATES` deve conter somente contratos auditados e esperados. Se o bytecode de um delegate confiável mudar ou surgir delegate desconhecido, o signer bloqueia a assinatura até intervenção operacional.

## Custodia Operacional em Producao

O `CustodyGuard` fica no servico do signer, nao no fluxo PIX. O PIX continua sendo a entrada de pagamento do cliente; a protecao atua no caixa, hot wallet, treasury e assinatura on-chain.

```text
Cliente paga PIX
        |
Gateway confirma ordem
        |
Core solicita liquidacao ao signer
        |
CustodyGuard valida risco, nonce, limites e lifecycle
        |
Signer assina e envia token on-chain
```

### Protecao EIP-7702

O signer monitora blocos `pending` e `latest` nos RPCs configurados. Quando encontra uma transacao EIP-7702 (`SET_CODE`, type `0x04`), ele:

- le a `authorizationList`;
- recupera a `authority`, que e a wallet que assinou a autorizacao;
- verifica se a wallet esta protegida;
- valida se o delegate esta em `CUSTODY_TRUSTED_DELEGATES`;
- valida selector permitido, se `CUSTODY_ALLOWED_SELECTORS` estiver configurado;
- confere se o bytecode hash do delegate confiavel nao mudou;
- registra evento em `custody_events`;
- em `paper` ou `live`, abre incidente em `custody_incidents` e bloqueia novas assinaturas.

A hot wallet derivada de `EVM_PRIVATE_KEY` entra automaticamente na lista protegida. `CUSTODY_PROTECTED_WALLETS` adiciona outras carteiras.

### Variaveis do Signer

```env
CUSTODY_GUARD_ENABLED=true
CUSTODY_GUARD_POLL_MS=1500
CUSTODY_MODE=paper
CUSTODY_UNLOCK_COOLDOWN_SEC=900
CUSTODY_TRUSTED_DELEGATES=
CUSTODY_ALLOWED_SELECTORS=
CUSTODY_PROTECTED_WALLETS=
TREASURY_MIN_USDT=0
TREASURY_TARGET_USDT=0
TREASURY_MAX_USDT=0
TREASURY_MAX_DAILY_OUTFLOW=0
TREASURY_LOCKDOWN_THRESHOLD=0
```

Modos de custodia:

- `shadow`: registra eventos, mas nao bloqueia transferencias. Bom para observar em staging ou no inicio da producao.
- `paper`: registra incidente persistente e bloqueia `/hd/transfer`. Recomendado para producao inicial.
- `live`: reservado para resposta automatica futura. Hoje aplica o mesmo bloqueio defensivo do `paper`.

O destrave operacional usa `POST /custody/unlock` no signer, protegido pelo mesmo HMAC do signer: `x-ts`, `x-nonce` e `x-signer-hmac`. O destrave respeita `CUSTODY_UNLOCK_COOLDOWN_SEC`.

### Nonce Manager Persistente

Para evitar colisao de nonce em compras simultaneas, o signer reserva nonce no banco antes de assinar:

```text
PendingNonceAt(chain)
        |
signer_chain_nonces reserva proximo nonce por wallet/rede
        |
tx assinada
        |
nonce vira submitted ou failed
```

Tabela usada:

- `signer_chain_nonces`: controla `reserved`, `submitted` e `failed` por `wallet`, `network` e `nonce`.

Isso complementa a idempotencia por ordem. A idempotencia evita duplo envio da mesma ordem; o nonce manager evita conflito entre ordens diferentes sendo liquidadas ao mesmo tempo.

### Lifecycle de Transacoes

Toda transacao enviada pelo signer passa a ser registrada:

- `signer_transactions`: guarda `tx_hash`, `idempotency_key`, origem, destino, token, valor, rede, nonce, status e confirmations.
- status possiveis hoje: `submitted`, `confirmed`, `reverted`, `failed`.
- um monitor consulta receipts nos RPCs e atualiza o status.

Isso melhora suporte, auditoria e reconciliacao financeira.

### Politica de Treasury

O signer pode bloquear novas assinaturas quando a saida diaria ultrapassa limites configurados:

- `TREASURY_MAX_DAILY_OUTFLOW`: limite operacional diario de saida.
- `TREASURY_LOCKDOWN_THRESHOLD`: limite mais severo que retorna erro de lockdown antes de assinar.
- `TREASURY_MIN_USDT`, `TREASURY_TARGET_USDT` e `TREASURY_MAX_USDT`: politica de caixa exibida no `/readyz` para operacao e alerta.

Valores `0` deixam o limite desabilitado. Para producao com saldo real, configure limites pequenos no inicio e aumente depois de observar o fluxo.

### Tabelas Criadas pelo Signer

- `custody_events`: eventos de seguranca e auditoria.
- `custody_incidents`: incidente ativo e historico de resolucao.
- `signer_chain_nonces`: reserva atomica de nonce por wallet e rede.
- `signer_transactions`: lifecycle das transacoes assinadas/enviadas.
- `signer_idempotency`: resposta final por idempotency key.
- `signer_idempotency_locks`: reserva transacional contra corrida de idempotencia.
- `signer_nonces`: nonces HMAC contra replay.

## Arquitetura Tecnica

A documentacao tecnica completa esta em [ARCHITECTURE.md](./ARCHITECTURE.md).

Ela cobre:

- Diagrama de sequencia.
- Componentes internos.
- Endpoints.
- Status de ordens.
- Webhooks.
- Variaveis de ambiente.
- Benchmark E2E.
- Deploy Railway/Docker.
- Troubleshooting.
- Rollback operacional.

## Deploy

Este repositorio inclui:

- `Dockerfile`
- `.dockerignore`
- `railway.json`

No Railway, configure as variaveis de ambiente de producao antes do deploy:

```env
APP_ENV=production
ALLOW_SIMULATIONS=false
PORT=3000
DATABASE_URL=postgres://...
LGPD_SECRET=...
WEBHOOK_SECRET=...
PIX_WEBHOOK_SECRET=...
PAGSEGURO_API_TOKEN=...
SIGNER_URL=http://signer.railway.internal:4010
SIGNER_NETWORK=BSC
SIGNER_HMAC_SECRET=...
BSC_XPUB=...
BSC_USDT_CONTRACT=...
BSC_FULLNODE_URL=...
TREASURY_HOT=...
CUSTODY_GUARD_ENABLED=false
CUSTODY_MODE=paper
CUSTODY_UNLOCK_COOLDOWN_SEC=900
CUSTODY_TRUSTED_DELEGATES=
TREASURY_MIN_USDT=0
TREASURY_TARGET_USDT=0
TREASURY_MAX_USDT=0
TREASURY_MAX_DAILY_OUTFLOW=0
TREASURY_LOCKDOWN_THRESHOLD=0
```

Mais detalhes em [ARCHITECTURE.md](./ARCHITECTURE.md#deploy).

No Railway, `SIGNER_URL` da API principal deve apontar para a rede privada do service do signer, nao para `https://...up.railway.app`. Se o service do signer se chama `signer` e escuta `PORT=4010`, use:

```env
SIGNER_URL=http://signer.railway.internal:4010
```

O dominio publico `*.up.railway.app` fica bloqueado em producao porque exporia o signer na internet. O aviso `Arquivo .env nao encontrado` e normal em cloud quando as variaveis estao configuradas pelo painel do Railway.

### Custodia, Treasury e EIP-7702

O Custody Guard roda no signer e protege o fluxo financeiro de caixa/hot wallet. Ele nao altera o fluxo PIX: o PIX continua sendo a entrada de pagamento; o signer continua sendo a saida on-chain.

Na camada de custodia, o signer agora possui:

- eventos persistentes em `custody_events`;
- incidente ativo em `custody_incidents`;
- modo `shadow`, `paper` ou `live`;
- cooldown para destrave operacional via `POST /custody/unlock`;
- reserva atomica de nonce em `signer_chain_nonces`;
- lifecycle de transacoes em `signer_transactions`;
- politica de treasury para limitar saida diaria antes de assinar.

Em producao, use `CUSTODY_MODE=paper` primeiro. `shadow` serve para observar sem bloquear. `live` fica reservado para uma etapa futura com resposta automatica depois de validacao operacional.

# 🏗️ Complete Technical Architecture

## 📱 Mobile + Backend Ecosystem

```mermaid
graph TB
    subgraph "Mobile Layer (React Native)"
        RN[React Native App]
        RN --> API_REST[API REST]
        RN --> WS[WebSocket]
        RN --> PUSH[Push Notifications]
    end

    subgraph "API Layer (Go)"
        API[API Gateway]
        API --> AUTH[JWT Auth]
        API --> RATE[Rate Limiter]
        API --> CORS[CORS Middleware]
    end

    subgraph "Mobile API Handlers"
        MH_AUTH[Auth Handlers]
        MH_USER[User/KYC Handlers]
        MH_WALLET[Wallet Handlers]
        MH_ORDERS[Orders Handlers]
        MH_DCA[DCA Handlers]
        MH_PIX[PIX Handlers]
        MH_SEC[Security Handlers]
        MH_CONTRACT[Contract Handlers]
        MH_NOTIFY[Notification Handlers]
        MH_WS[WebSocket Handlers]
    end

    subgraph "Core Services"
        SERVICE_ORDER[Order Service]
        SERVICE_WALLET[Wallet Service]
        SERVICE_PAYOUT[Payout Service]
        SERVICE_PRICE[Price Service]
        SERVICE_DCA[DCA Service]
    end

    subgraph "Resilience Layer"
        CB[Circuit Breaker]
        RETRY[Retry with Exponential Backoff]
        FALLBACK[RPC Fallback]
        DLQ[Dead Letter Queue]
    end

    subgraph "Background Workers"
        W_ONCHAIN[On-Chain Worker]
        W_PAYOUT[Payout Worker]
        W_PRICE[Price Worker]
        W_DCA[DCA Worker]
        W_SWEEP[Sweep Worker]
    end

    subgraph "Infrastructure"
        DB[(PostgreSQL)]
        CACHE[(Redis)]
        QUEUE[(RabbitMQ)]
        RPC1[BSC RPC #1]
        RPC2[BSC RPC #2]
        RPC3[BSC RPC #3]
        RPC4[BSC RPC #4]
        RPCN[BSC RPC #N]
    end

    subgraph "Blockchain (BNB Smart Chain)"
        VAULT[SwappyTreasuryVault]
        REGISTRY[SwappyDelegateRegistry]
        DELEGATE[Swappy7702PayoutDelegate]
        TOKEN[USDT / BEP-20]
    end

    subgraph "External Services"
        PIX[PIX API - Efi]
        EMAIL[Brevo Email Service]
        COINGECKO[CoinGecko Price Feed]
    end

    API --> MH_AUTH
    API --> MH_USER
    API --> MH_WALLET
    API --> MH_ORDERS
    API --> MH_DCA
    API --> MH_PIX
    API --> MH_SEC
    API --> MH_CONTRACT
    API --> MH_NOTIFY
    API --> MH_WS

    MH_ORDERS --> SERVICE_ORDER
    MH_WALLET --> SERVICE_WALLET
    MH_DCA --> SERVICE_DCA

    SERVICE_ORDER --> DB
    SERVICE_ORDER --> QUEUE

    SERVICE_WALLET --> DB

    SERVICE_PAYOUT --> QUEUE
    SERVICE_PRICE --> CACHE

    CB --> RETRY
    RETRY --> FALLBACK

    W_ONCHAIN --> RPC1
    W_ONCHAIN --> RPC2
    W_ONCHAIN --> RPC3
    W_ONCHAIN --> RPC4
    W_ONCHAIN --> RPCN

    W_ONCHAIN --> VAULT
    W_ONCHAIN --> REGISTRY

    W_PAYOUT --> QUEUE
    W_PAYOUT --> PIX
    W_PAYOUT --> VAULT
    W_PAYOUT --> DB

    W_PRICE --> COINGECKO
    W_PRICE --> CACHE

    W_DCA --> SERVICE_DCA
    W_DCA --> QUEUE

    W_SWEEP --> VAULT

    VAULT --> TOKEN
    DELEGATE --> VAULT

    API --> EMAIL
    PUSH --> FCM[Firebase / APNS]
```

---

# 📊 Mobile API (50+ Endpoints)

| Module | Endpoints | Methods | Auth |
|---------|-----------|----------|------|
| **Auth** | `/api/mobile/auth/register`<br>`/api/mobile/auth/login`<br>`/api/mobile/auth/refresh`<br>`/api/mobile/auth/logout` | POST<br>POST<br>POST<br>POST | ❌<br>❌<br>❌<br>✅ |
| **User** | `/api/mobile/user/profile`<br>`/api/mobile/user/profile`<br>`/api/mobile/user/kyc`<br>`/api/mobile/user/kyc/status` | GET<br>PUT<br>POST<br>GET | ✅<br>✅<br>✅<br>✅ |
| **Wallet** | `/api/mobile/wallet/balance`<br>`/api/mobile/wallet/tokens`<br>`/api/mobile/wallet/address`<br>`/api/mobile/wallet/generate`<br>`/api/mobile/wallet/history` | GET<br>GET<br>GET<br>POST<br>GET | ✅<br>✅<br>✅<br>✅<br>✅ |
| **Orders** | `/api/mobile/order/buy`<br>`/api/mobile/order/sell`<br>`/api/mobile/order/swap`<br>`/api/mobile/order/{id}`<br>`/api/mobile/orders`<br>`/api/mobile/order/cancel` | POST<br>POST<br>POST<br>GET<br>GET<br>POST | ✅<br>✅<br>✅<br>✅<br>✅<br>✅ |
| **PIX** | `/api/mobile/pix/generate`<br>`/api/mobile/pix/confirm`<br>`/api/mobile/pix/status/{id}`<br>`/api/mobile/pix/copy` | POST<br>POST<br>GET<br>POST | ✅<br>❌<br>✅<br>✅ |
| **DCA** | `/api/mobile/dca/create`<br>`/api/mobile/dca/strategies`<br>`/api/mobile/dca/{id}`<br>`/api/mobile/dca/{id}`<br>`/api/mobile/dca/{id}/status` | POST<br>GET<br>PUT<br>DELETE<br>GET | ✅<br>✅<br>✅<br>✅<br>✅ |
| **Security** | `/api/mobile/security/pin`<br>`/api/mobile/security/biometry`<br>`/api/mobile/security/2fa`<br>`/api/mobile/security/devices`<br>`/api/mobile/security/device` | POST<br>POST<br>POST<br>GET<br>DELETE | ✅<br>✅<br>✅<br>✅<br>✅ |
| **Smart Contracts** | `/api/mobile/contracts/payout`<br>`/api/mobile/contracts/vault`<br>`/api/mobile/contracts/delegate`<br>`/api/mobile/contracts/pause`<br>`/api/mobile/contracts/unpause` | POST<br>GET<br>GET<br>POST<br>POST | ✅<br>✅<br>✅<br>✅<br>✅ |
| **Notifications** | `/api/mobile/notifications`<br>`/api/mobile/notifications/read`<br>`/api/mobile/notifications/{id}`<br>`/api/mobile/notifications/token` | GET<br>PUT<br>DELETE<br>POST | ✅<br>✅<br>✅<br>✅ |
| **WebSocket** | `ws://api/mobile/ws/orders`<br>`ws://api/mobile/ws/price`<br>`ws://api/mobile/ws/notifications` | WS<br>WS<br>WS | ✅<br>✅<br>✅ |
| **Settings** | `/api/mobile/settings`<br>`/api/mobile/settings`<br>`/api/mobile/settings/limits` | GET<br>PUT<br>GET | ✅<br>✅<br>✅ |
| **Health** | `/api/mobile/health` | GET | ❌ |

---

# 🔄 Asynchronous Workers

| Worker | Responsibility | Interval | Dependencies |
|----------|---------------|-----------|--------------|
| **On-Chain Worker** | Monitor BNB Smart Chain deposits | Every 3 seconds | RPC Pool, Smart Contracts |
| **Payout Worker** | Process PIX payouts | Every 5 seconds | Efi API, Treasury Vault |
| **Price Worker** | Update crypto market prices | Every 30 seconds | CoinGecko, Redis |
| **DCA Worker** | Execute recurring DCA purchases | Every hour | Order Service |
| **Sweep Worker** | Treasury vault consolidation | Every 6 hours | Vault, Signer |

---

# 🛡️ Resilience Layer

```yaml
Circuit Breaker:
  max_failures: 5
  timeout: 60s
  state:
    - Closed
    - Open
    - Half-Open

Retry Policy:
  max_attempts: 5
  base_delay: 1s
  max_delay: 30s
  multiplier: 2.0

RPC Fallback:
  providers: 6
  health_check: 30s
  timeout: 10s

Dead Letter Queue:
  max_retries: 5
  storage: dlq_events
  retention: 7 days
```

---

# 🚀 Technology Stack

| Layer | Technology |
|--------|------------|
| Mobile | React Native |
| Backend | Go |
| Authentication | JWT |
| Database | PostgreSQL |
| Cache | Redis |
| Queue | RabbitMQ |
| Blockchain | BNB Smart Chain |
| Smart Contracts | Solidity |
| Token | USDT (BEP-20) |
| Price Feed | CoinGecko |
| PIX Gateway | Efi |
| Email | Brevo |
| Push Notifications | Firebase Cloud Messaging (FCM) / Apple Push Notification Service (APNS) |
| Communication | REST API + WebSocket |
| Architecture | Event-Driven Microservices |
| Resilience | Circuit Breaker, Retry, RPC Fallback, DLQ |

# 🗄️ Database Schema (Mobile)

## Core Tables

| Table | Description |
|--------|-------------|
| `users` | Authentication and user profile |
| `devices` | Connected devices |
| `orders` | Buy, sell and swap orders |
| `payouts` | PIX payments |
| `dca_strategies` | Dollar-Cost Averaging (DCA) strategies |
| `notifications` | Push notifications |
| `settings` | User preferences and configuration |

## Audit & Resilience

| Table | Description |
|--------|-------------|
| `operation_ids` | Idempotency control (duplicate request prevention) |
| `dlq_events` | Dead Letter Queue events |
| `health_checks` | Service monitoring |
| `audit_logs` | Complete audit trail |

---

# 🔐 Security Controls

| Layer | Control | Details |
|--------|----------|---------|
| API | JWT Access Token | Expires in **15 minutes** |
| API | JWT Refresh Token | Expires in **7 days** |
| API | Rate Limiting | **100 requests/minute** per IP |
| API | CORS | Authorized domains only |
| API | PIN / Biometrics | Local device authentication |
| API | Two-Factor Authentication (2FA) | OTP via Authenticator App |
| Database | Password Hashing | bcrypt |
| Database | PII Encryption | AES-256 |
| Network | TLS/SSL | HTTPS mandatory |
| Infrastructure | Web Application Firewall (WAF) | Cloudflare |
| Infrastructure | Secrets Management | Vault / AWS Secrets Manager |

---

# 📈 Performance Metrics

```yaml
SLO Targets:
  API Latency: < 200ms (P95)
  PIX Processing: < 60s
  On-Chain Detection: < 5min
  Availability: 99.95%
  Error Rate: < 0.1%

Monitoring:
  - Prometheus Metrics
  - Grafana Dashboards
  - PagerDuty Alerts
  - Datadog APM
  - Structured JSON Logging
```

---

# 🚀 Deployment Strategy

## Infrastructure

```yaml
Infrastructure:
  Container:
    - Docker

  Orchestration:
    - Kubernetes

  Database:
    - PostgreSQL (Managed Cloud)

  Cache:
    - Redis (Upstash)

  Queue:
    - RabbitMQ (CloudAMQP)
```

## Continuous Deployment

```yaml
Deploy:
  CI/CD:
    - GitHub Actions

  Canary Deployment:
    - 10%
    - 50%
    - 100%

  Rollback:
    - Automatic if error rate > 1%

  Blue-Green Deployment:
    - Zero Downtime
```

## Scalability

```yaml
Scaling:
  Auto Scaling:
    - Kubernetes HPA (CPU 70%)

  Workers:
    - 2 → 10 Replicas

  Database:
    - Read Replicas

  Blockchain RPC:
    - Multi-Provider Fallback
```

# 📝 Environment Variables

```env
# ============================================================
# Database
# ============================================================
DATABASE_URL=postgresql://...

# ============================================================
# JWT Authentication
# ============================================================
MOBILE_JWT_SECRET=xxx
MOBILE_REFRESH_SECRET=xxx

# ============================================================
# Blockchain - Binance Smart Chain (6 RPC Nodes)
# ============================================================
BSC_RPC_URL_1=...
BSC_RPC_URL_2=...
BSC_RPC_URL_3=...
BSC_RPC_URL_4=...
BSC_RPC_URL_5=...
BSC_RPC_URL_FALLBACK=...

# ============================================================
# Blockchain - Polygon sell deposits (optional)
# ============================================================
POLYGON_RPC_URLS=https://polygon-rpc.com
POLYGON_USDT_CONTRACT=0xc2132D05D31c914a87C6611C10748AEb04B58e8F

# ============================================================
# Smart Contracts
# ============================================================
BSC_USDT_CONTRACT=0x55d398...
PRIVATE_KEY=0x...

# ============================================================
# PIX Gateway (Efi)
# ============================================================
EFI_CLIENT_ID=xxx
EFI_CLIENT_SECRET=xxx
EFI_PIX_KEY=xxx

# ============================================================
# Email (Brevo)
# ============================================================
SMTP_HOST=smtp-relay.brevo.com
SMTP_USER=xxx
SMTP_PASS=xxx

# ============================================================
# Push Notifications
# ============================================================
FCM_SERVER_KEY=xxx

# ============================================================
# Security
# ============================================================
LGPD_SECRET=xxx
PIX_WEBHOOK_SECRET=xxx
```

---

# 🎯 Evolution Roadmap

```mermaid
timeline
    title Mobile Ecosystem Evolution

    Phase 1 : Foundation
             : Auth + Wallet + Orders

    Phase 2 : Payments
             : PIX + On-Chain

    Phase 3 : Automation
             : DCA + Workers

    Phase 4 : Real-time
             : WebSocket + Push

    Phase 5 : Scale
             : Resilience + RPC Fallback
```

## Documentacao Tecnica

- [Consoles, API Keys, Policies e Testes E2E](#consoles-api-keys-policies-e-testes-e2e)
- [ARCHITECTURE.md](./ARCHITECTURE.md): especificacao tecnica e operacional.
- [schema.sql](./schema.sql): estrutura SQL.
- [signer/README.md](./signer/README.md): signer isolado.
- [contracts/README.md](./contracts/README.md): contratos BSC editaveis para treasury, custody e delegates.
- [contracts/AUDIT_NOTES.md](./contracts/AUDIT_NOTES.md): notas de auditoria e plano seguro de adocao.

## Consoles, API Keys, Policies e Testes E2E

### Console APIs

Endpoints usados pelos consoles frontend:

- `GET /app/agent/summary`: resumo do Agent Console.
- `GET /app/developer/summary`: resumo do Developer Console.
- `GET /developer/projects`: lista projetos.
- `POST /developer/projects`: cria projeto.
- `GET /developer/projects/{id}`: detalhe do projeto.
- `PATCH /developer/projects/{id}`: edita projeto.
- `GET /developer/api-keys`: lista API keys reais.
- `POST /developer/projects/{id}/api-keys`: cria API key vinculada ao projeto.
- `POST /developer/api-keys/{id}/rotate`: rotaciona secret, exibido uma unica vez.
- `POST /developer/api-keys/{id}/disabled`: desabilita API key.
- `POST /developer/api-keys/{id}/revoked`: revoga API key.
- `GET /agent/{id}/policy`: le policy real do agente.
- `PATCH /agent/{id}/policy`: atualiza policy real do agente.

### Persistencia nova

Tabelas criadas pela migracao idempotente:

- `developer_projects`: projetos por ambiente, limite de gasto e metadata.
- `developer_api_keys`: public key, hash do secret, scopes, IP restrictions, rate limit e usage hash.
- `developer_project_agents`: vinculo entre projeto e agente.
- `marketplace_agent_policies`: limites, permissoes, assets, capabilities, providers e fallback por agente.

Secrets de API key nunca sao persistidos em texto puro. O backend salva hash para autenticacao e `log_hash` para correlacao com `api_request_logs`.

### API Keys

Formatos gerados:

- Sandbox: `pk_test_cfx_...` e `sk_test_cfx_...`
- Production: `pk_live_cfx_...` e `sk_live_cfx_...`

O `secretKey` aparece apenas na resposta de criacao ou rotacao. Depois disso, somente `maskedSecret` e `publicKey` ficam visiveis.

As chaves criadas em `developer_api_keys` autenticam chamadas via:

```http
Authorization: Bearer sk_test_cfx_...
```

### Agent policies

Campos principais:

- `dailyLimitUsdt`
- `monthlyLimitUsdt`
- `maxTransactionUsdt`
- `allowedAssets`
- `allowedCapabilities`
- `allowedProviders`
- `permissions`
- `requireRealProvider`
- `mockFallback`

Enforcement ativo:

- purchase valida `capabilities:purchase`, asset, capability e valor maximo por transacao.
- execution valida `capabilities:execute`, capability, provider e `requireRealProvider`.
- enforcement existe em handler HTTP e na camada `database`, cobrindo tambem chamadas MCP/internas.

### Testes E2E/MCP

Flags opt-in:

```env
RUN_E2E_TESTS=false
RUN_TESTNET_PAYMENT_TESTS=false
RUN_LIVE_PAYMENT_TESTS=false
E2E_BASE_URL=http://localhost:8080
E2E_API_KEY=
E2E_AGENT_WALLET=0x0000000000000000000000000000000000001001
E2E_PAYER_WALLET=0x0000000000000000000000000000000000001001
E2E_DEST_WALLET=0x0000000000000000000000000000000000001001
E2E_PAYMENT_ASSET=USDT
E2E_PIX_KEY=e2e@example.com
E2E_TEST_WALLET_PRIVATE_KEY=
E2E_TEST_TX_HASH=
E2E_TEST_LOG_INDEX=0
LIVE_PAYMENT_MAX_USD=1.00
LIVE_PAYMENT_CONFIRMATION_REQUIRED=true
```

Execucao local:

```powershell
go test ./...
```

E2E HTTP/MCP contra servidor local:

```powershell
$env:RUN_E2E_TESTS="true"
$env:E2E_API_KEY="sk_test_cfx_..."
go test ./tests/e2e -v
```

Canario testnet:

```powershell
$env:RUN_E2E_TESTS="true"
$env:RUN_TESTNET_PAYMENT_TESTS="true"
$env:E2E_TEST_TX_HASH="0x..."
$env:E2E_TEST_LOG_INDEX="0"
go test ./tests/e2e -run TestMCPAgentTestnetPaymentExecuteCanary -v
```

Live payment tests nunca devem rodar em CI automatico. Exigem `RUN_LIVE_PAYMENT_TESTS=true`, `LIVE_PAYMENT_CONFIRMATION_REQUIRED=true` e limite explicito em `LIVE_PAYMENT_MAX_USD`.

## Licenca

Licenca ainda nao definida neste repositorio. Antes de distribuicao publica, adicionar um arquivo `LICENSE` com a licenca escolhida.

# Financial Product Interface

<div align="center">
  <img src="https://res.cloudinary.com/limpeja/image/upload/v1783059789/2d3a41b4-0ea0-4649-a27a-f7dcb646c9f1.png" alt="Swappy Logo" width="1024" />
</div>

---

## 📱 Swappy - Buy & Sell Crypto Instantly

**Swappy** é uma plataforma Web3 que permite comprar e vender stablecoins como USDT(Tether.io) e EURUSD Nova moeda europeia de forma instantânea e segura. Com integração via PIX, você pode realizar transações em segundos com total confiabilidade.

### ✨ Diferenciais da Plataforma

- ⚡ **Compre e venda cripto instantaneamente** via PIX
- 🔒 **Transações seguras** e sem complicações
- 👥 **950.000+ usuários** confiam na Swappy
- 💳 **30+ opções** de pagamento locais
- 🪙 **100+ criptomoedas** disponíveis

---

## 🛒 Fluxo de Compra (Buy) - Step 1

### Informe o valor e visualize a cotação

<div align="center">
  <img src="https://res.cloudinary.com/limpeja/image/upload/v1783058374/compra-removebg-preview_ikab4t.png" alt="Swappy - Tela de Compra" width="300" />
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
  <img src="https://res.cloudinary.com/limpeja/image/upload/v1783058436/sp2-removebg-preview_iarh45.png" alt="Swappy - Tela de Pagamento" width="300" />
</div>

**Como funciona:**

1. **Informe sua Wallet** - Cole o endereço da sua carteira (ETH, BTC, USDT)
2. **Escolha o método de pagamento**:
   - 💰 **PIX** - Instantâneo e sem taxas extras
   - 💳 **VISA** - Cartão de crédito internacional
   - 💳 **Mastercard** - Cartão de crédito internacional
3. **Confirme a transação** e receba suas criptos em segundos

---

## 🔄 Fluxo de Venda (Sell)

### Venda suas criptos e receba em reais

1. Selecione a criptomoeda que deseja vender
2. Informe a quantidade
3. Escolha o método de recebimento (PIX)
4. Confirme a transação e receba em sua conta

---

# Swappy Payment Gateway

Backend Go para orquestracao instantanea de settlement fiat -> USDT.

O sistema nao tenta ser um "crypto gateway simples". Ele opera como um **instant settlement orchestration system**: recebe fiat por rails tradicionais, confirma o pagamento, registra tudo de forma auditavel e dispara entrega cripto para a wallet do usuario.

## Fluxo Principal

### BUY BRL via Pix

1. Usuario informa quanto quer pagar em BRL.
2. `Quote Service` retorna cotacao travada e quanto USDT sera entregue.
3. Usuario informa wallet TRON.
4. Gateway cria `buy_order` com status `aguardando_pix`.
5. Sistema gera payload/QR Pix da Swappy.
6. Banking webhook confirma pagamento.
7. `Settlement Engine` marca a ordem como `pago_fiat`.
8. `BuySendWorker` entrega USDT para a wallet do usuario.
9. Ordem recebe `tx_hash_out` e `delivered_at`.

### BUY USD via Stripe

1. Usuario informa quanto quer pagar em USD.
2. `Quote Service` usa cotacao USDT/USD.
3. Gateway cria `buy_order` com `fiat_currency=USD` e `payment_method=stripe`.
4. Stripe confirma o charge via webhook.
5. Settlement marca `pago_fiat`.
6. Delivery cripto envia USDT para a wallet.

### SELL USDT -> Pix

1. Usuario informa chave Pix e valor BRL.
2. Gateway gera endereco de deposito TRON deterministico.
3. `Blockchain Monitor` confirma deposito USDT.
4. `PayoutWorker` liquida Pix para o usuario.

## Camadas

```text
User
  -> Payment Rail Layer
     -> Pix BRL
     -> Stripe USD
  -> Settlement Engine
     -> quote lock
     -> idempotencia
     -> status transacional
     -> auditoria
  -> Crypto Delivery Layer
     -> wallet engine
     -> signer
     -> broadcast / transfer USDT
     -> tx hash
```

## Componentes

- `cmd/api`: servidor HTTP publico.
- `internal/server`: handlers REST, request id, rate limit, webhooks e SSE.
- `internal/workers`: workers concorrentes para price, on-chain, payout, sweep e buy delivery.
- `internal/database`: schema, repositorios, auditoria e persistencia LGPD.
- `internal/privacy`: hash e criptografia AES-GCM para dados pessoais.
- `internal/tron`: validacao/derivacao TRON.
- `signer`: servico isolado de assinatura com HMAC anti-replay.

## Endpoints

### Quote

```http
GET /api/quote?mode=buy&amountBRL=150&asset=USDT
GET /api/quote?mode=buy&amountUSD=150&fiatCurrency=USD&paymentMethod=stripe&asset=USDT
POST /api/quote
```

Resposta principal:

```json
{
  "mode": "buy",
  "asset": "USDT",
  "amountFiat": 150,
  "fiatCurrency": "BRL",
  "paymentMethod": "pix",
  "rate": 5.43,
  "cryptoAmount": 27.62,
  "rateLockExpiresAt": "2026-07-03T03:00:00Z"
}
```

### Buy

```http
POST /api/buy
```

Pix BRL:

```json
{
  "amountBRL": 150,
  "asset": "USDT",
  "address": "T..."
}
```

Stripe USD:

```json
{
  "amountUSD": 150,
  "fiatCurrency": "USD",
  "paymentMethod": "stripe",
  "asset": "USDT",
  "address": "T..."
}
```

BUY nao exige KYC nem CPF/telefone. A Swappy recebe o fiat e entrega USDT para a wallet informada.

### Webhooks

```http
POST /api/pix/webhook/buy
POST /api/stripe/webhook/buy
```

Ambos convergem para o mesmo settlement:

```text
aguardando_pix / aguardando_stripe -> pago_fiat -> enviado
```

## Auditoria

Toda ordem tem UUID proprio e timestamps de ciclo de vida:

- `id`: identificador imutavel da ordem.
- `request_id`: correlacao HTTP/log/evento.
- `created_at`: criacao.
- `updated_at`: ultima alteracao.
- `paid_at`: pagamento fiat confirmado.
- `settled_at`: settlement interno concluido.
- `delivered_at`: entrega USDT concluida.
- `provider_payment_id`: id externo do banco/Stripe.
- `tx_hash_out`: hash de entrega cripto.

Eventos ficam em tabelas separadas:

- `order_events`
- `buy_order_events`

Cada evento carrega `request_id`, `type`, `payload` e `created_at`.

## LGPD

BUY segue minimizacao de dados: nao coleta CPF/telefone.

No SELL, quando chave Pix pessoal e necessaria:

- CPF/telefone nao ficam expostos na resposta JSON.
- Hashes ficam em `orders.pix_cpf_hash` e `orders.pix_phone_hash` para velocity/risk.
- Valores reversiveis ficam separados em `order_private`.
- `order_private` usa AES-GCM com `LGPD_SECRET`.
- Sem `LGPD_SECRET`, o backend falha antes de persistir dado pessoal.

Variavel obrigatoria para SELL com dados pessoais:

```env
LGPD_SECRET=use-um-segredo-forte-de-producao
```

## Idempotencia

Webhooks usam `provider_payment_id` e eventos `webhook.provider` para evitar dupla liquidacao.

Delivery usa `idempotencyKey` no signer/worker para evitar envio duplicado.

## Variaveis Importantes

```env
DATABASE_URL=postgres://...
LGPD_SECRET=...
WEBHOOK_SECRET=...
PIX_WEBHOOK_SECRET=...
SIGNER_URL=http://signer:4010
SIGNER_HMAC_SECRET=...
TRON_XPUB=...
TRON_USDT_CONTRACT=...
TRON_FULLNODE_URL=...
FEE_BPS=0
FEE_MIN_BRL=0
```

## Verificacao Local

```bash
go test -run TestDoesNotExist ./internal/privacy ./internal/config ./internal/database ./internal/server ./internal/workers ./cmd/api
```

Para subir a API:

```bash
go run ./cmd/api
```

## Nota Operacional

O caminho rapido da UX fica no quote e na criacao da intencao de compra. Confirmacao fiat e delivery cripto rodam em workers para manter baixa latencia no frontend e preservar consistencia financeira no backend.

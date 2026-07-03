# Swappy Payment Gateway - Arquitetura Tecnica

## Indice

1. [Visao Geral](#visao-geral)
2. [Requisitos](#requisitos)
3. [Diagrama de Sequencia](#diagrama-de-sequencia)
4. [Componentes](#componentes)
5. [Fluxos Principais](#fluxos-principais)
6. [Status de Ordem](#status-de-ordem)
7. [Endpoints](#endpoints)
8. [Webhooks](#webhooks)
9. [Auditoria e LGPD](#auditoria-e-lgpd)
10. [Idempotencia](#idempotencia)
11. [Configuracao](#configuracao)
12. [Deploy](#deploy)
13. [Benchmark E2E](#benchmark-e2e)
14. [Troubleshooting](#troubleshooting)
15. [Monitoramento](#monitoramento)
16. [Rollback Operacional](#rollback-operacional)

## Visao Geral

O Swappy Payment Gateway e um backend Go para orquestracao de pagamento fiat e entrega de USDT. O sistema separa o caminho de UX rapida do caminho financeiro critico:

- Quote e criacao de ordem respondem rapido para o frontend.
- Confirmacao fiat entra por webhook assinado.
- Delivery cripto roda em worker.
- Eventos e timestamps ficam persistidos para auditoria.

Fluxo critico:

```text
Cliente paga Pix -> Webhook confirma -> BuySendWorker dispara da wallet Swappy -> USDT chega na wallet do cliente
```

## Requisitos

- Go: `1.25.0`, conforme `go.mod`.
- PostgreSQL.
- Signer TRON dedicado para producao.
- Provedor PIX/PagBank configurado.
- Full node/API TRON configurada.

## Diagrama de Sequencia

```mermaid
sequenceDiagram
    participant U as Usuario
    participant F as Frontend
    participant API as Go API
    participant DB as PostgreSQL
    participant PIX as Provedor PIX
    participant BUS as EventBus
    participant W as BuySendWorker
    participant S as Signer TRON
    participant T as Blockchain TRON

    U->>F: Informa BRL e wallet TRON
    F->>API: GET/POST /api/quote
    API-->>F: Cotacao, taxa, USDT estimado
    F->>API: POST /api/buy
    API->>PIX: Cria cobranca PIX
    API->>DB: INSERT buy_order aguardando_pix
    API-->>F: buyId + dados de pagamento
    U->>PIX: Paga PIX
    PIX->>API: POST /api/pix/webhook/buy
    API->>API: Valida HMAC e idempotencia
    API->>DB: status = pago_fiat
    API->>BUS: publish buy.paid
    BUS->>W: buy.paid
    W->>S: /hd/transfer HMAC
    S->>T: Broadcast USDT TRC20
    T-->>S: tx hash
    S-->>W: txHash
    W->>DB: status = enviado, tx_hash_out
    F->>API: SSE /api/buy/{id}/stream
    API-->>F: status enviado
```

## Componentes

- `cmd/api`: servidor HTTP publico.
- `cmd/benchflow`: ferramenta de benchmark do fluxo webhook/delivery.
- `internal/server`: handlers REST, webhooks, CORS, request ID, readiness e SSE.
- `internal/workers`: price, on-chain, payout, sweep e buy delivery.
- `internal/database`: schema, repositorios, eventos e persistencia LGPD.
- `internal/privacy`: hash e criptografia AES-GCM.
- `internal/tron`: validacao e derivacao TRON.
- `signer`: servico isolado de assinatura. Em producao de TRON, usar signer com `SIGNER_NETWORK=tron`.

## Fluxos Principais

### BUY BRL via PIX

1. Frontend chama `/api/quote`.
2. Frontend chama `/api/buy`.
3. API cria `buy_order` em `aguardando_pix`.
4. Provedor PIX chama `/api/pix/webhook/buy`.
5. API valida assinatura HMAC.
6. API verifica duplicidade via `webhook.provider`.
7. API marca `pago_fiat`.
8. API publica `buy.paid`.
9. `BuySendWorker` chama signer TRON.
10. Ordem vai para `enviado`.

### BUY USD via Stripe

1. Frontend ou camada upstream cria PaymentIntent/Checkout com `metadata.buyId`.
2. Stripe chama `/api/stripe/webhook/buy`.
3. API valida `Stripe-Signature`.
4. Eventos liquidados marcam `pago_fiat`.
5. Delivery segue o mesmo fluxo do PIX.

Eventos aceitos como liquidacao:

- `checkout.session.completed`
- `payment_intent.succeeded`
- `charge.succeeded`

### SELL USDT -> PIX

1. Frontend cria `/api/order`.
2. API gera ou aceita endereco TRON.
3. `OnchainWorker` monitora transferencias USDT.
4. Deposito valido marca `pago`.
5. `PayoutWorker` liquida PIX.

## Status de Ordem

### BUY

| Status | Descricao | Proximo status |
| --- | --- | --- |
| `aguardando_pix` | Ordem criada e aguardando confirmacao PIX | `pago_fiat`, `erro` |
| `aguardando_stripe` | Ordem criada e aguardando confirmacao Stripe | `pago_fiat`, `erro` |
| `pago_fiat` | Pagamento fiat confirmado | `enviado`, `erro` |
| `pago_pix` | Alias legado para pagamento PIX confirmado | `enviado`, `erro` |
| `enviado` | Cripto enviada para wallet do cliente | Final |
| `delivered` | Cripto entregue/confirmada | Final |
| `confirmado` | Confirmacao final | Final |
| `erro` | Falha operacional ou rejeicao de provider/signer | Intervencao manual |

### SELL

| Status | Descricao | Proximo status |
| --- | --- | --- |
| `aguardando_deposito` | Aguardando deposito USDT do usuario | `pago`, `expirada`, `aguardando_validacao` |
| `aguardando_validacao` | Deposito fora da faixa/tolerancia | Intervencao manual |
| `expirada` | Ordem vencida | Final |
| `pago` | Deposito on-chain detectado | `concluida`, `erro` |
| `processando_payout` | PIX de saida em processamento | `concluida`, `erro` |
| `concluida` | PIX liquidado | Final |
| `erro` | Falha no payout ou validacao | Intervencao manual |

## Endpoints

### Health

```http
GET /healthz
GET /readyz
```

### Quote

```http
GET /api/quote?mode=buy&amountBRL=150&asset=USDT
GET /api/quote?mode=buy&amountUSD=150&fiatCurrency=USD&paymentMethod=stripe&asset=USDT
POST /api/quote
```

Resposta:

```json
{
  "mode": "buy",
  "asset": "USDT",
  "amountFiat": 150,
  "fiatCurrency": "BRL",
  "paymentMethod": "pix",
  "feeFiat": 12,
  "payoutFiat": 138,
  "rate": 5.43,
  "cryptoAmount": 25.41436464,
  "rateLockExpiresAt": "2026-07-03T03:00:00Z"
}
```

### Buy

```http
POST /api/buy
GET /api/buy/{id}
GET /api/buy/{id}/stream
```

PIX BRL:

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

### Sell

```http
POST /api/order
GET /api/order/{id}
GET /api/order/{id}/stream
POST /api/order/{id}/deposit
POST /api/order/{id}/payout
```

Payload:

```json
{
  "amountBRL": 150,
  "asset": "USDT",
  "network": "TRON",
  "pixCpf": "12345678901",
  "pixPhone": "11999999999"
}
```

## Webhooks

### Pix BUY

```http
POST /api/pix/webhook/buy
x-pagbank-signature: <hmac_sha256_hex_raw_body>
```

Payload aceito:

```json
{
  "buyId": "018f3f4e-0000-4000-9000-000000000000",
  "status": "concluido",
  "providerId": "pix_123456",
  "error": ""
}
```

Variantes de status que comecam com `conclu` sao tratadas como confirmacao.

### Pix SELL/Payout legado

```http
POST /api/pix/webhook
x-pagbank-signature: <hmac_sha256_hex_raw_body>
```

```json
{
  "orderId": "018f3f4e-0000-4000-9000-000000000000",
  "status": "concluido",
  "providerId": "pix_payout_123",
  "error": ""
}
```

### Stripe BUY

```http
POST /api/stripe/webhook/buy
Stripe-Signature: t=<timestamp>,v1=<signature>
```

Exemplo simplificado:

```json
{
  "id": "evt_123",
  "type": "payment_intent.succeeded",
  "data": {
    "object": {
      "metadata": {
        "buyId": "018f3f4e-0000-4000-9000-000000000000"
      }
    }
  }
}
```

## Auditoria e LGPD

Campos de auditoria por ordem:

- `id`
- `request_id`
- `created_at`
- `updated_at`
- `paid_at`
- `settled_at`
- `delivered_at`
- `provider_payment_id`
- `tx_hash_out`

Tabelas de eventos:

- `order_events`
- `buy_order_events`

Cada evento guarda:

- `request_id`
- `type`
- `payload`
- `created_at`

LGPD:

- BUY minimiza dados pessoais e nao exige CPF/telefone.
- SELL exige chave PIX quando aplicavel.
- CPF/telefone sao salvos criptografados em `order_private`.
- Hashes ficam em `orders.pix_cpf_hash` e `orders.pix_phone_hash`.
- Sem `LGPD_SECRET`, o backend falha antes de persistir dado pessoal.

## Idempotencia

- Webhooks usam `providerId` em eventos `webhook.provider`.
- Existe indice unico parcial para evitar duplicidade de provider por ordem.
- Delivery usa `idempotencyKey` no signer.
- Endpoints internos aceitam `x-idempotency-key` quando aplicavel.

## Configuracao

Exemplo completo de `.env` para desenvolvimento/staging:

```env
# Runtime
APP_ENV=development
ALLOW_SIMULATIONS=true
PORT=3000
ALLOWED_ORIGINS=http://localhost:5173

# Database
DATABASE_URL=postgres://user:pass@localhost:5432/swappy?sslmode=disable

# Security
LGPD_SECRET=use-um-segredo-forte
WEBHOOK_SECRET=webhook-secret
PIX_WEBHOOK_SECRET=pix-webhook-secret

# Fees / limits
ORDER_MIN_BRL=10
ORDER_MAX_BRL=10000
RATE_LOCK_SEC=600
FEE_BPS=200
FEE_FIXED_USD=2
FEE_MIN_BRL=0

# Pix / PagBank
PAGSEGURO_API_TOKEN=token
PAGSEGURO_API_BASE_URL=https://api.pagseguro.com
PIX_CHARGE_ENDPOINT=/orders

# Tron
TRON_XPUB=xpub...
TRON_USDT_CONTRACT=TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t
TRON_FULLNODE_URL=https://api.trongrid.io
TRON_SOLIDITY_URL=https://api.trongrid.io
TRON_USDT_DECIMALS=6
TRON_CONFIRMATIONS=20
TRON_HMAC_SECRET=internal-hmac-secret

# Signer
SIGNER_URL=http://localhost:4010
SIGNER_NETWORK=tron
SIGNER_HMAC_SECRET=signer-hmac-secret

# Treasury
TREASURY_HOT=T...
TREASURY_COLD=T...
ENABLE_SWEEP_WORKER=false
ENABLE_SWEEP_STUB=false
SWEEP_FREQUENCY_MS=30000
TRON_GAS_RESERVE_TRX=5

# SMTP / ops
SMTP_HOST=smtp.example.com
SMTP_PORT=587
SMTP_USER=user
SMTP_PASS=pass
SMTP_SECURE=false
SMTP_FROM_EMAIL=ops@example.com
SMTP_FROM_NAME=Swappy Ops
OPS_EMAIL=ops@example.com
```

Producao deve usar:

```env
APP_ENV=production
ALLOW_SIMULATIONS=false
SIGNER_NETWORK=tron
ENABLE_SWEEP_STUB=false
```

## Deploy

### Railway

Arquivos:

- `Dockerfile`
- `railway.json`
- `.dockerignore`

O `railway.json` usa:

- builder: `DOCKERFILE`
- healthcheck: `/healthz`
- start command: `/app/api`

Variaveis obrigatorias no Railway:

```env
APP_ENV=production
ALLOW_SIMULATIONS=false
PORT=3000
DATABASE_URL=postgres://...
LGPD_SECRET=...
WEBHOOK_SECRET=...
PIX_WEBHOOK_SECRET=...
PAGSEGURO_API_TOKEN=...
SIGNER_URL=...
SIGNER_NETWORK=tron
SIGNER_HMAC_SECRET=...
TRON_XPUB=...
TRON_USDT_CONTRACT=...
TRON_FULLNODE_URL=...
TREASURY_HOT=...
```

### Docker local

```bash
docker build -t swappy-payment-gateway .
docker run --rm -p 3000:3000 --env-file .env swappy-payment-gateway
```

### Docker Compose

Exemplo minimo:

```yaml
services:
  api:
    build: .
    ports:
      - "3000:3000"
    env_file:
      - .env
    depends_on:
      - postgres

  postgres:
    image: postgres:16
    environment:
      POSTGRES_DB: swappy
      POSTGRES_USER: swappy
      POSTGRES_PASSWORD: swappy
    ports:
      - "5432:5432"
    volumes:
      - pgdata:/var/lib/postgresql/data

volumes:
  pgdata:
```

### Kubernetes

Nao ha manifest Kubernetes oficial neste repositorio. Se for necessario, criar:

- `Deployment` com readiness em `/readyz` e liveness em `/healthz`.
- `Secret` para variaveis sensiveis.
- `Service` HTTP.
- `HorizontalPodAutoscaler` apenas depois de medir gargalos reais.

## Benchmark E2E

Ferramenta:

```bash
go run ./cmd/benchflow -h
```

### Preparar ambiente

1. Subir API com Postgres real.
2. Configurar `PIX_WEBHOOK_SECRET`.
3. Configurar signer TRON real se for medir `-mode e2e`.
4. Criar ordens BUY validas em `aguardando_pix`.
5. Salvar os IDs em `buy_ids.txt`, um por linha.

Exemplo `buy_ids.txt`:

```text
018f3f4e-0000-4000-9000-000000000001
018f3f4e-0000-4000-9000-000000000002
018f3f4e-0000-4000-9000-000000000003
```

### Benchmark ACK

Mede validacao de webhook, idempotencia, persistencia e publicacao `buy.paid`.

```bash
go run ./cmd/benchflow \
  -api http://localhost:3000 \
  -secret "$PIX_WEBHOOK_SECRET" \
  -buy-ids ./buy_ids.txt \
  -count 50 \
  -concurrency 8 \
  -mode ack \
  -json bench-ack.json \
  -csv bench-ack.csv
```

### Benchmark E2E

Mede ate o status `enviado`, `delivered` ou `confirmado`.

```bash
go run ./cmd/benchflow \
  -api http://localhost:3000 \
  -secret "$PIX_WEBHOOK_SECRET" \
  -buy-ids ./buy_ids.txt \
  -count 20 \
  -concurrency 4 \
  -mode e2e \
  -json bench-e2e.json \
  -csv bench-e2e.csv
```

### Gerar buy_ids.txt automaticamente

Em staging, com `ALLOW_SIMULATIONS=true` ou provider PIX configurado:

```bash
go run ./cmd/benchflow \
  -api http://localhost:3000 \
  -secret "$PIX_WEBHOOK_SECRET" \
  -create-buy \
  -address TXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX \
  -count 10 \
  -concurrency 2 \
  -mode ack
```

Se precisar reutilizar IDs, gere as ordens pelo frontend/admin ou por `POST /api/buy` e salve os `buyId` retornados em `buy_ids.txt`.

### Resultado de benchmark local

Data do teste: **03/07/2026**

Ambiente:

- OS: Windows
- Go arch: `386`
- CPU: Intel(R) Core(TM) i3-7100 CPU @ 3.90GHz
- Comando: `go test ./internal/workers -bench BenchmarkEventBus -benchtime=100ms -count=1`

Resultado:

```text
BenchmarkEventBusPublishNoSubscriber-4       27.23 ns/op    0 B/op    0 allocs/op
BenchmarkEventBusPublishSingleSubscriber-4   93.10 ns/op    0 B/op    0 allocs/op
BenchmarkEventBusPublishManySubscribers-4    1367 ns/op     0 B/op    0 allocs/op
```

Leitura:

- O barramento interno nao e gargalo relevante no caminho `webhook -> buy.paid -> BuySendWorker`.
- `webhook_ack_ms` e `delivery_ms` precisam ser medidos em staging com Postgres, signer TRON, secrets reais e `buy_ids` validos.
- O benchmark E2E real nao foi executado neste ambiente porque signer TRON e IDs de teste nao estavam disponiveis na sessao.

### Teste de fluxo com dinheiro ficticio

Existe um teste automatizado em memoria para validar a reacao esperada do backend sem dinheiro real, provider real ou signer real:

```bash
go test ./internal/settlement
```

O teste cobre:

- PIX ficticio confirmado.
- Transicao `aguardando_pix -> pago_fiat`.
- Publicacao do evento `buy.paid`.
- Worker simulado entregando token.
- Transicao final `pago_fiat -> enviado`.
- Geracao de `txHashOut` simulado.
- Bloqueio de webhook duplicado pelo mesmo `providerId`.
- Status rejeitado nao publica `buy.paid`.

Ultima execucao nesta sessao:

```text
go test ./...
PASS
```

Observacao: a API HTTP completa nao foi iniciada localmente porque o `DATABASE_URL` do `.env` estava malformado e a autenticacao do Postgres local em `localhost:5432` falhou com credenciais padrao. Para rodar o fluxo HTTP completo, configurar um `DATABASE_URL` valido e repetir com `cmd/benchflow`.

### Proximos gargalos provaveis

| Gargalo | Onde aparece | Como medir | Mitigacao |
| --- | --- | --- | --- |
| Postgres lento | `webhook_ack_ms` alto | `cmd/benchflow -mode ack` | indices, pool, menor payload em transacao |
| Provider enviando webhook duplicado | `duplicate=true` frequente | eventos `webhook.provider` | manter idempotencia e observar taxa |
| Signer/RPC TRON lento | `delivery_ms` alto | `cmd/benchflow -mode e2e` | signer dedicado, timeout, retry controlado, RPC redundante |
| EventBus cheio | drops ou delivery sem evento | metricas de fila | aumentar buffer, fila persistente se necessario |
| CoinGecko/price indisponivel | quote 503 | logs `PriceWorker` | cache, fallback controlado, provider alternativo |
| PagBank indisponivel | erro ao criar PIX/payout | logs provider e status 5xx | retry com idempotency key, circuit breaker |

## Troubleshooting

### API nao sobe

Verificar:

```bash
go run ./cmd/api
```

Erros comuns:

- `DATABASE_URL nao configurado`: configurar Postgres.
- `Configuracao invalida para producao`: alguma variavel obrigatoria esta ausente.
- `ALLOW_SIMULATIONS deve ser false em producao`: ajustar variavel no Railway.
- `SIGNER_NETWORK deve ser tron em producao`: evitar signer EVM por engano.

### `/readyz` retorna `ok=false`

Chamar:

```bash
curl http://localhost:3000/readyz
```

Campos comuns em `warnings`:

- `pix_provider`: falta `PAGSEGURO_API_TOKEN`.
- `pix_webhook`: falta `PIX_WEBHOOK_SECRET` ou `WEBHOOK_SECRET`.
- `signer`: falta `SIGNER_URL` ou `SIGNER_HMAC_SECRET`.
- `signer_tron`: `SIGNER_NETWORK` nao e `tron`.
- `tron_contract`: falta `TRON_USDT_CONTRACT`.
- `tron_fullnode`: falta `TRON_FULLNODE_URL`.
- `lgpd_secret`: falta `LGPD_SECRET`.

### Webhook PIX recebe 401

Verificar:

- Header `x-pagbank-signature`.
- HMAC SHA-256 em hex do body bruto.
- Mesmo segredo entre provider e `PIX_WEBHOOK_SECRET`.
- Body nao pode ser reformatado depois de assinado.

### Ordem paga nao envia USDT

Verificar:

- Status da ordem: `GET /api/buy/{id}`.
- Logs do `BuySendWorker`.
- `SIGNER_URL`.
- `SIGNER_HMAC_SECRET`.
- `TRON_USDT_CONTRACT`.
- Se o signer e realmente TRON.

### Duplicidade de webhook

Comportamento esperado:

- Mesmo `providerId` para a mesma ordem deve retornar `duplicate=true`.
- Nao deve disparar segundo envio.

### Testes locais

```bash
go test ./cmd/api ./internal/config ./internal/server ./internal/workers
go build ./cmd/api
go test ./internal/workers -bench BenchmarkEventBus -benchmem
```

## Monitoramento

Ainda nao ha integracao Prometheus/Grafana neste repositorio.

Recomendacao para proxima etapa:

- Expor `/metrics` com Prometheus.
- Medir `webhook_ack_ms`, `buy_delivery_ms`, erros por provider e tamanho de fila.
- Dashboard Grafana com p50, p55, p95, p99.
- Alerta quando delivery ficar acima do SLO.

SLO inicial sugerido para staging:

| Metrica | Alvo inicial |
| --- | --- |
| `webhook_ack_ms p95` | < 300 ms |
| `webhook_ack_ms p99` | < 800 ms |
| `delivery_ms p95` | depende de signer/RPC, medir antes de fixar |
| `duplicate_webhook_rate` | monitorar, nao necessariamente erro |

## Rollback Operacional

Estrategia recomendada:

1. Pausar novos pagamentos no frontend ou provider se houver falha grave.
2. Manter webhooks aceitando callbacks para nao perder confirmacoes.
3. Desabilitar delivery automatico alterando configuracao do signer/worker apenas se houver risco de envio incorreto.
4. Preservar ordens em `pago_fiat` para reprocessamento controlado.
5. Corrigir deploy.
6. Reprocessar ordens pendentes por evento ou ferramenta operacional.
7. Conferir `buy_order_events`, `provider_payment_id` e `tx_hash_out` antes de qualquer reenvio.

Regra financeira: nunca apagar eventos para "corrigir" estado. Adicionar evento compensatorio ou executar migracao auditavel.

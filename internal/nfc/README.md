# ChainFX NFC Rail

`internal/nfc` e o nucleo Go do cartao NFC fechado da ChainFX. Ele implementa o protocolo financeiro real usado por app mobile, leitor/terminal ChainFX e backend:

- emissao de token dinamico `nfc1...`;
- contrato APDU/TLV do cartao digital;
- parser do token lido no terminal;
- autorizacao online;
- hold de USDT;
- captura;
- reversao;
- auditoria e idempotencia.

O NFC fisico do celular continua sendo responsabilidade do app mobile nativo, porque Android/iOS controlam a antena e o registro de AID. O Go e a fonte de verdade do protocolo, do token, do autorizador e do dinheiro.

## Escopo de Producao

Este trilho e real e closed-loop:

```text
ChainFX mobile wallet -> NFC tap -> ChainFX terminal -> ChainFX Go API -> USDT hold/capture
```

Ele nao usa PAN, CVV ou Track2 real. Tambem nao tenta se passar por Visa/Mastercard. Maquininha comum de adquirente so roteia para a ChainFX se existir contrato tecnico/comercial de adquirente, bandeira, BIN sponsor ou issuer processor. Sem isso, o caminho correto e terminal/leitor ChainFX.

## Fluxo Financeiro

1. Usuario tem wallet mobile registrada.
2. App chama `POST /api/mobile/nfc/provision` com JWT mobile.
3. Go emite token `nfc1...` com TTL curto e persiste `token_hash`.
4. App nativo entrega o token via NFC usando o contrato APDU ChainFX.
5. Terminal extrai `DF01=<token>` da resposta APDU.
6. Terminal chama `POST /api/nfc/authorize`.
7. Go valida assinatura, expiracao, idempotencia, token persistido e saldo.
8. Go trava saldo em `nfc_wallet_balances.locked_usdt_micro`.
9. Terminal recebe:
   - `response_code=00`, `status=approved`;
   - `response_code=51`, `status=requires_funding`;
   - `response_code=05`, `status=declined`.
10. Na conclusao da venda, terminal chama `POST /api/nfc/authorizations/{id}/capture`.
11. Se houver cancelamento/falha, terminal chama `POST /api/nfc/authorizations/{id}/reverse`.

## Contrato APDU

AID ChainFX:

```text
F222222222
```

SELECT esperado pelo cartao digital:

```text
00 A4 04 00 05 F2 22 22 22 22
```

Resposta SELECT:

```text
6F ... 84 05 F222222222 A5 ... 50 0B "ChainFX NFC" 87 01 01 9000
```

Resposta com token:

```text
70 <len>
  DF02 01 01
  DF01 <len> <token nfc1... em UTF-8>
9000
```

Sem token valido:

```text
6985
```

Funcoes Go:

- `BuildTokenResponse(token string) ([]byte, error)`: monta a resposta APDU com `DF01`.
- `ParseTokenResponse(apdu []byte) (string, error)`: extrai o token no terminal.
- `NewCardApplet(token string)`: representa o contrato de cartao digital ChainFX no Go.
- `CardApplet.ProcessCommandAPDU(apdu []byte)`: processa SELECT/GPO/READ RECORD/GET DATA conforme o contrato.

## Token

Formato:

```text
nfc1.<payload-base64url>.<hmac-base64url>
```

Claims:

```json
{
  "tid": "token id",
  "wallet": "0x...",
  "device_id": "device-id",
  "network": "BSC",
  "iat": 1784380000,
  "exp": 1784380120,
  "nonce": "random"
}
```

Funcoes:

- `IssueToken(...)`: emite token assinado por HMAC-SHA256.
- `VerifyToken(...)`: valida assinatura, expiracao e estrutura.
- `TokenHash(...)`: gera hash persistido no banco.

## Endpoints Mobile

### Cartao Digital

```http
GET /api/mobile/nfc/card
Authorization: Bearer <mobile-access-token>
```

Retorna wallet, asset, rede, AID e saldo NFC.

### Provisionamento

```http
POST /api/mobile/nfc/provision
Authorization: Bearer <mobile-access-token>
Content-Type: application/json
```

```json
{
  "device_id": "android-device-id",
  "network": "BSC",
  "ttl_seconds": 120
}
```

Resposta:

```json
{
  "token": "nfc1...",
  "token_id": "...",
  "expires_at": "2026-07-18T13:53:00Z",
  "aid": "F222222222",
  "network": "BSC",
  "apdu": {
    "response_template": "70",
    "token_tag": "DF01",
    "version_tag": "DF02"
  }
}
```

## Endpoints Terminal

Todos exigem `Authorization: Bearer <terminal-api-key>`.

### Autorizar

```http
POST /api/nfc/authorize
Idempotency-Key: terminal-tx-001
Content-Type: application/json
```

```json
{
  "token": "nfc1...",
  "amount_brl": "25.90",
  "currency": "BRL",
  "merchant_id": "merchant_demo",
  "terminal_id": "terminal_01",
  "external_ref": "cupom-123",
  "idempotency_key": "terminal-tx-001"
}
```

### Capturar

```http
POST /api/nfc/authorizations/{id}/capture
```

Finaliza a venda e consome o saldo travado.

### Reverter

```http
POST /api/nfc/authorizations/{id}/reverse
```

Cancela a autorizacao e devolve o valor travado para `available_usdt_micro`.

### Consultar

```http
GET /api/nfc/authorizations/{id}
GET /api/nfc/balance/{wallet}?network=BSC
```

## Banco

Migration:

```text
migrations/020_nfc_closed_loop.sql
```

Tabelas:

- `nfc_tokens`: token id, hash, wallet, device, rede, status e expiracao.
- `nfc_wallet_balances`: saldo disponivel e travado por wallet/rede/asset.
- `nfc_authorizations`: autorizacao, valor BRL, taxa, USDT requerido, status, hold, capture e reverse.

Estados:

```text
approved -> captured
approved -> reversed
declined
requires_funding
```

## Variaveis

```env
NFC_ENABLED=true
NFC_TOKEN_SECRET=use-um-segredo-forte
NFC_TOKEN_TTL_SEC=120
NFC_HOLD_TTL_SEC=900
NFC_MAX_AMOUNT_BRL=500
```

Em producao, `NFC_TOKEN_SECRET` e obrigatorio quando `NFC_ENABLED=true`.

## Performance

Metrica local do token `IssueToken + VerifyToken`:

```text
p50 = 9.973us
p55 = 9.987us
p95 = 100.645us
p99 = 101.557us
max = 116.765us
```

Essa medida cobre criptografia local. A latencia real do pagamento inclui app NFC, terminal, HTTP, Postgres, price worker e lock transacional.

## Validacao

```powershell
go test ./internal/nfc ./internal/mobile ./internal/database ./internal/server
CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o api-linux-check ./cmd/api
```

<div align="center">
<img src="https://res.cloudinary.com/limpeja/image/upload/v1770993671/swap_1_mvctri.png" alt="Swappy Logo" width="320">
<h3>Swappy Financial Core</h3>
<p>Infraestrutura de Alta Performance para On/Off-Ramp Automatizado de Criptoativos (USDT/BRL)</p>
</div>

---

## 1. Visão Arquitetural do Ecossistema

O **Swappy Financial Core** é uma stack transacional de nível industrial desenhada especificamente para operações de liquidação instantânea de criptoativos (*Sell/Off-ramp* e *Buy/On-ramp*). O sistema foi arquitetado sob o padrão de **Monorepo em Go**, separando estritamente a API pública de I/O, os Workers assíncronos orientados a eventos e o Cofre Criptográfico de Assinaturas (`signer`).

### Divisão de Responsabilidades (Isolamento de Processos)
1. **API Gateway Core (`cmd/api`):** Camada de entrada pública, enxuta e endurecida. Responsável por expor endpoints REST, aplicar Rate Limiting, travar cotações com TTL estrito via cache em memória e persistir intenções de ordens no PostgreSQL com status `aguardando_deposito`.
2. **Asynchronous Processing Workers (`internal/workers`):** Daemons assíncronos isolados que escutam um Barramento de Eventos de memória (com interface abstrata pronta para acoplamento em filas gerenciadas como AWS SQS ou Apache Kafka).
   * **`PriceWorker`:** Sincroniza e faz o cache da cotação institucional com TTL controlado.
   * **`OnchainWorker`:** Escuta ativamente os nós de RPC (TRON/BSC), processa eventos de blocos confirmados e valida se os depósitos dos usuários entraram com as confirmações matemáticas exigidas.
   * **`PayoutWorker`:** Conecta-se às APIs bancárias reguladas (PIX/PagBank) para executar a liquidação em moeda fiduciária instantaneamente após a validação cripto.
   * **`SweepWorker`:** Varre os endereços efêmeros de depósito dos usuários (*Child Addresses*) enviando os fundos para a carteira fria/tesouraria central.
3. **Cofre Isolado Signer (`signer/`):** Microsserviço crítico rodando em sub-rede privada (*Air-gapped* lógico). É o único processo que retém as chaves privadas (`EVM_PRIVATE_KEY` / `TRON_XPRV`). Nenhuma outra parte do sistema tem acesso à memória onde as chaves operam.

---

## 2. Porquê Go? (Decisões de Engenharia de Produção)

A infraestrutura original em Node.js (Express) apresentava gargalos críticos para a escala financeira real que foram mitigados pela migração para Go:

* **Gerenciamento de CPU-Bound vs I/O-Bound:** A validação e geração de assinaturas criptográficas (HMAC-SHA256 e Criptografia de Curva Elíptica ECDSA) são operações intensivas de CPU. No Node.js, isso bloqueava o *Event Loop Single-Threaded*, atrasando requisições HTTP de entrada. O Go resolve isto nativamente escalonando Goroutines entre múltiplos cores de CPU via *M:N Scheduler*.
* **Segurança e Imutabilidade de Memória:** Strings em Node/V8 podem sofrer vazamento em buffers compartilhados em caso de *memory dumping* após falhas catastróficas. O Go oferece tipagem estática e total controle sobre ponteiros e arrays de bytes (`[]byte`), permitindo que dados sensíveis de chaves e buffers de criptografia sejam limpos da memória de forma previsível e segura.
* **Race Detector Native:** Sistemas de criptografia que gerenciam saldos concorrentes não podem sofrer de *Race Conditions**. O compilador do Go traz a flag `-race`, utilizada em nossas esteiras de CI/CD para auditar matematicamente se duas threads tentaram atualizar ou liquidar a mesma ordem ao mesmo tempo.

---

## 🔐 3. Security Engineering & Mathematical Foundations

### End-to-End HMAC-SHA256 Authentication

Para impedir interceptações na rede interna, toda comunicação entre o Core e o serviço `signer` é autenticada utilizando HMAC-SHA256.

```text
digest = HMAC_SHA256(
    HMAC_SECRET,
    x-ts || "." || x-nonce || "." || RawBody
)
```

Onde:

- `||` representa a concatenação binária dos campos.
- `x-ts` corresponde ao Unix Timestamp da requisição.
- `x-nonce` é um identificador aleatório utilizado para impedir reutilização da mesma requisição.

O Signer rejeita automaticamente qualquer requisição cuja diferença entre o horário atual e o timestamp recebido seja superior a 60 segundos.

```text
| current_time - x_ts | > 60 seconds
```

Além disso, cada `x-nonce` é armazenado utilizando uma restrição `UNIQUE` no banco de dados. Caso o mesmo nonce seja reutilizado dentro da janela válida, a operação é imediatamente abortada, eliminando ataques de replay.

---

### Deterministic Wallet Derivation (BIP-44)

O sistema deriva endereços exclusivos para cada usuário utilizando uma única chave estendida privada (`TRON_XPRV`).

```text
Address = Derive(
    XPRV,
    m/44'/195'/0'/0/index
)
```

Essa abordagem permite gerar bilhões de endereços determinísticos mantendo uma única Seed Master como raiz criptográfica do sistema. Cada endereço é monitorado continuamente pelo Onchain Worker até que seus fundos sejam automaticamente consolidados pelo Sweep Worker.

Para evitar o pior cenário de um gateway financeiro — o **Duplo Gasto** ou **Dupla Liquidação** (enviar dois PIX para o mesmo depósito ou assinar duas transferências on-chain por instabilidade de rede), implementamos o padrão de **Idempotência Persistida**.

### O Mecanismo da Trava Transacional (Idempotência no Postgres)

Para blindar o fluxo contra falhas de rede, timeouts ou retentativas automáticas, o sistema utiliza o banco de dados como única fonte da verdade (*Single Source of Truth*), aplicando uma constraint de chave única (`PRIMARY KEY`) em um bloco transacional isolado:

```sql
CREATE TABLE IF NOT EXISTS signer_idempotency (
    idempotency_key VARCHAR(128) PRIMARY KEY,
    tx_hash VARCHAR(128) NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

### O Algoritmo de Execução Segura

* **Entrada de Evento:** Um evento de liquidação de compra ou varredura entra com uma chave exclusiva (ex: `idempotencyKey: "sweep-order-550e8400"`).
* **Abertura de Transação:** O worker inicia um bloco atômico no PostgreSQL (`BEGIN TRANSACTION;`).
* **Verificação de Chave:** O sistema tenta ler a chave na tabela `signer_idempotency`:
  * **Se a chave já existir (Cache Hit):** A transação sofre um *Rollback* instantâneo no banco de dados. O sistema recupera o `tx_hash` gravado anteriormente e responde com `StatusCode 200`. Nenhuma nova operação financeira ou chamada de assinatura blockchain é disparada.
  * **Se a chave não existir (Cache Miss):** O sistema prossegue com a assinatura criptográfica via curva elíptica ECDSA, envia o payload para a rede (EVM/TRON), captura o hash retornado pela blockchain, insere o registro na tabela e executa o `COMMIT;`.

---

### 📂 5. Organização do Código (Árvore Estrutural do Monorepo)

O projeto está estruturado em um padrão limpo de camadas (*Clean Architecture*) usando pacotes desacoplados e tipagem estática:

```text
meu-gateway-go/
├── cmd/
│   └── api/                        # Ponto de entrada do servidor HTTP Core público
├── internal/
│   ├── config/                     # Validação e parser de variáveis de ambiente (.env)
│   ├── database/                   # Repositórios SQL, pools do Postgres e migrações
│   └── workers/                    # Daemons assíncronos orientados a eventos
│       ├── bus.go                  # Event Bus concorrente thread-safe usando sync.RWMutex
│       ├── onchain_worker.go       # Listener / Poller de nós RPC de Blockchain
│       ├── payout_worker.go        # Executor de ordens de liquidação PIX (PagBank)
│       ├── price_worker.go         # Sincronizador de cotação institucional com TTL
│       └── sweep_worker.go         # Varredura automática de saldos de endereços filhos
├── signer/                         # Microsserviço Cofre Isolado (Assinador EVM/TRON)
│   ├── main.go                     # Servidor HTTP privado do Signer
│   └── crypto_test.go              # Testes unitários matemáticos do escudo HMAC
└── tests/                          # SUÍTE DE TESTES INTEGRADOS DE ALTA ESCALA
    ├── test_helpers.go             # Helper do Testcontainers para ciclo de vida do Docker
    ├── signer_integration_test.go  # Testes E2E do Signer baseados nos fluxos de payload (.ps1)
    └── api_order_integration_test.go # Testes E2E de criação e liquidação de ordens

    ### 🧪 6. Elite Automated Testing Suite

Para garantir confiabilidade de nível de produção antes de qualquer deploy, a estratégia de testes é dividida em duas camadas complementares.

#### 6.1 Fast Unit Tests (In-Memory)

Valida exclusivamente lógica de negócio, cálculos financeiros, manipulação de buffers, precisão decimal, criptografia HMAC, validações e componentes puros, sem qualquer dependência externa.

```bash
# Execute only fast unit tests
go test -v -short ./...
```

#### 6.2 Integration Tests with Real PostgreSQL (Testcontainers)

A camada de integração utiliza **Testcontainers-Go** para provisionar automaticamente um banco PostgreSQL limpo durante cada execução.

O helper de testes inicializa um container oficial (`postgres:15-alpine`), aplica todas as migrações reais do projeto, executa a suíte completa e remove completamente o ambiente ao término dos testes.

Essa abordagem garante que toda a camada de persistência seja validada contra um banco real, eliminando inconsistências entre ambientes de desenvolvimento e produção.

Além disso, todos os testes podem ser executados com o **Go Race Detector**, responsável por identificar condições de corrida (*data races*) entre goroutines concorrentes.

```bash
# Integration tests with Race Detector enabled
go test -v -race ./tests/...
```

---

### 🐳 7. Production Deployment Pipeline

A aplicação utiliza uma estratégia de **Multi-Stage Docker Build**, separando completamente o ambiente de compilação do ambiente de execução.

No primeiro estágio, todas as dependências são resolvidas e o binário é compilado estaticamente.

O segundo estágio gera uma imagem extremamente enxuta baseada em Alpine Linux, contendo apenas o executável final e os certificados necessários para comunicação TLS, reduzindo significativamente o tamanho da imagem e a superfície de ataque.

```dockerfile
# Stage 1 - Build
FROM golang:1.21-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux \
    go build -ldflags="-w -s" \
    -o /engine ./cmd/api/main.go

# Stage 2 - Runtime
FROM alpine:3.19

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /

COPY --from=builder /engine /engine

EXPOSE 4010

USER 65534:65534

ENTRYPOINT ["/engine"]
```

#### 🛡️ Production Security Notes

- Multi-stage builds removem completamente o toolchain do Go da imagem final.
- O processo é executado como usuário não privilegiado (`UID 65534`), evitando execução como **root**.
- Apenas certificados CA e timezone são incluídos na imagem de produção.
- As flags `-ldflags="-w -s"` removem símbolos e informações de depuração do executável, reduzindo significativamente seu tamanho e dificultando processos de engenharia reversa.
- O resultado é um artefato leve, otimizado para produção e com menor superfície de ataque.
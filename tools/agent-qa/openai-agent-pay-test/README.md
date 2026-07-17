# ChainFX Agent QA

External QA harness for Agent Pay interoperability. It receives only the public Agent Card URL, discovers skills, calls `/a2a`, creates a PIX intent when credentials are provided, checks status, and writes a JSON report.

It does not call internal REST, MCP, or backend-private routes directly.

## Run discovery without credentials

```powershell
cd C:\Users\Paulo\Desktop\payment-gateway
node tools\agent-qa\openai-agent-pay-test\index.mjs --card "https://api-production-bc748.up.railway.app/.well-known/agent-card.json"
```

Expected result without a bearer token: card discovery works, public methods work, and the report stops at the authenticated quote/payment step with `outcome: discovery_ok_auth_required`.

## Run full Agent Pay QA

```powershell
cd C:\Users\Paulo\Desktop\payment-gateway
$env:CHAINFX_API_KEY="YOUR_CHAINFX_API_KEY"
$env:CHAINFX_AGENT_WALLET="0x830000000000000000000000000000000000019a"
$env:CHAINFX_PIX_KEY="+5511999999999"
$env:CHAINFX_AMOUNT_BRL="10.00"
node tools\agent-qa\openai-agent-pay-test\index.mjs --card "https://api-production-bc748.up.railway.app/.well-known/agent-card.json" --out ".\agent-qa-report.json"
```

The report answers whether the agent discovered skills, selected `pay_pix_with_usdt`, called `quote_required_usdt`, created an intent, checked status, and where it failed if the flow did not complete.

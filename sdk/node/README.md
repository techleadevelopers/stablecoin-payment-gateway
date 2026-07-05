# ChainFX Node SDK

Minimal Node SDK for the ChainFX Digital FX Payments API.

```js
import { ChainFX } from "@chainfx/sdk";

const chainfx = new ChainFX({
  apiKey: process.env.CHAINFX_API_KEY,
  baseUrl: process.env.CHAINFX_API_BASE_URL || "https://sandbox-api.chainfx.com"
});

const quote = await chainfx.quote({
  side: "buy",
  fiat: "BRL",
  asset: "USDT",
  amount: 500
});

const order = await chainfx.buy({
  fiat: "BRL",
  asset: "USDT",
  amount: 500,
  wallet: "0x000000000000000000000000000000000000dEaD"
});
```

Requires Node 18+ because it uses native `fetch`.

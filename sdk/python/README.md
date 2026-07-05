# ChainFX Python SDK

Minimal Python SDK for the ChainFX Digital FX Payments API.

```python
import os
from chainfx import ChainFX

chainfx = ChainFX(
    api_key=os.environ["CHAINFX_API_KEY"],
    base_url=os.getenv("CHAINFX_API_BASE_URL", "https://sandbox-api.chainfx.com"),
)

quote = chainfx.quote(side="buy", fiat="BRL", asset="USDT", amount=500)

order = chainfx.buy(
    fiat="BRL",
    asset="USDT",
    amount=500,
    wallet="0x000000000000000000000000000000000000dEaD",
)
```

No external dependency is required; it uses Python standard library HTTP clients.

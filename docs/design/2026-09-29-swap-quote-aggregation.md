# Swap Quote Aggregation — Design

- **Reference:** the wallet's swap flow today builds a Horizon path payment on the device; this adds a
  server-side comparison with the XOXNO aggregator.
- **Last updated:** 2026-09-29
- **Status:** Proposed

## Summary

The wallet asks the backend for one swap quote. The backend asks **Horizon** (the route the wallet
already uses) and the **XOXNO aggregator** (Soroban router across Stellar DEX pools) in parallel and
returns whichever gives the **higher output**, together with everything the swap page and the review
modal display. An aggregator route comes with a ready-to-sign transaction; a Horizon route is built by
the wallet as before.

The aggregator is **opt-in** (`--swap-xoxno-enabled`, default off). With it off, the backend quotes
Horizon only and never contacts XOXNO.

## Goals and non-goals

- Best available output for the user, chosen server-side so every client gets the same answer.
- The wallet never signs something the backend merely claims: the backend verifies an aggregator
  transaction before returning it, and the wallet verifies it again before signing.
- Non-goals: order routing on the device, custody of any key, a price oracle, or changing the classic
  swap flow when the aggregator is off or unavailable.

## Flow

```
wallet ── POST /api/v1/swap/quote ──▶ backend
                                       ├── Horizon  (strict-send / strict-receive path)
                                       └── XOXNO    (quote + simulated transaction)   [if enabled]
                                            │
                       verify aggregator answer, pick highest output, build response
wallet ◀─ source, amounts, min after slippage, rate, fee, transaction? ──────────────┘
```

- **Quote by send or by receive.** `sourceAmount` or `destAmount` (exactly one). For a receive amount
  the backend first sizes the input, then quotes forward at that input, so the returned minimum is a
  real slippage floor and never the exact target.
- **Tie or a source failing.** A tie prefers Horizon. A failed or slow source is dropped (per-source
  timeout); the other still answers. All sources answering "no route" is `404`; all failing is `502`.
- **Missing trustline.** A route into an asset the sender does not hold is quoted without a
  transaction and flagged `requiresTrustline`; the wallet adds the trustline, then asks again.
- **Soroban tokens.** Assets are `XLM`, `CODE:ISSUER` or a contract id. A Soroban token needs its
  `decimals` in the request; classic assets are fixed at 7.

## Trust model: what the backend checks before it returns an aggregator transaction

The XOXNO service is a third party, so its answer is treated as untrusted input.

- The quote must match the request (assets, amount, decimals) and its minimum output must respect the
  requested slippage.
- The transaction must be one operation calling `execute_strategy` on the **pinned router** for the
  network, from the sender, with a bounded fee.
- The authorization tree may contain only the sender's transfer of the **source token**, and never
  more than the input amount.
- The route payload inside the call must name the same input and output tokens and carry a minimum
  output at least the quoted floor.
- Anything else is dropped as if the source had no answer. The wallet repeats these checks against the
  same pinned router before signing, so a compromised backend cannot redirect a signature.

## Token lists

- `GET /api/v1/swap/tokens` lists the tokens the aggregator can route **and** XOXNO marks swappable
  (name, decimals, icon, USD price, kind: native, classic or Soroban).
- `GET /api/v1/swap/tokens?scope=all` is a compact catalog of every registered token (logo and price),
  so the wallet can show a logo and price for tokens its own sources do not know.
- **Caching.** Contract id, code, name, decimals, icon URL and asset kind never change, so they are
  kept for the life of the process and a token is classified at most once. Only the price and the
  swappable flag expire (`--swap-token-cache-ttl`). If a refresh fails the last list is served, but a
  price older than three TTLs is dropped rather than shown stale. With no list ever fetched, the route
  errors.
- **Prices.** The XOXNO price is used when present. Otherwise the aggregator's pool price is used only
  when the pool holds at least $1,000; a thin pool is easy to move, so no price beats a misleading one.

## Configuration

The aggregator is switched on with `--swap-xoxno-enabled`. Its quote server, pinned router and token
list are set per network with `--swap-xoxno-{pubnet,testnet}-{quote-url,router,token-list-url}`;
`--swap-source-timeout` and `--swap-token-cache-ttl` tune the per-source timeout and the cache
lifetime (defaults in `--help`). Enabling the aggregator sends the sender's account address to its
quote server. Boot fails on a malformed URL or router when it is enabled.

## Operational surfaces

- Routes: `POST /api/v1/swap/quote`, `GET /api/v1/swap/tokens`, both behind the existing auth
  middleware and a 9 s handler cap (under the server write timeout).
- Metrics (existing service-call series): `swap-quote` and `swap-tokens` per method
  (`GetBestQuote`, `horizon`, `xoxno`, `GetSwapTokens`, `GetTokenCatalog`, upstream fetches).
- No database, Redis key or migration. State is in-process caches only.

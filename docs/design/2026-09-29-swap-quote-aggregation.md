# Swap Quote Aggregation — Design

- **Reference:** the wallet's swap flow today builds a Horizon path payment on the device; this adds a
  server-side comparison with the XOXNO aggregator.
- **Last updated:** 2026-09-30
- **Status:** Proposed

## Summary

The wallet asks the backend for one swap quote. The backend asks **Horizon** (the route the wallet
already uses), the **XOXNO aggregator**, and optionally **LI.FI** (via Soroswap) in parallel and
returns whichever gives the **higher output**, together with everything the swap page and the review
modal display. An aggregator route comes with a ready-to-sign transaction; a Horizon route is built by
the wallet as before.

The aggregator is **opt-in** (`--swap-xoxno-enabled`, default off). With it off, the backend quotes
Horizon only unless LI.FI is independently enabled.

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
                                       ├── LI.FI    (pubnet Soroswap quote + simulated transaction) [if enabled]
                                       └── XOXNO    (quote + simulated transaction)   [if enabled]
                                            │
                       verify aggregator answer, pick highest output, build response
wallet ◀─ source, amounts, min after slippage, rate, fee, transaction? ──────────────┘
```

- **Quote by send or by receive.** `sourceAmount` or `destAmount` (exactly one). For a receive amount
  the backend first sizes the input, then quotes forward at that input, so the returned minimum is a
  real slippage floor and never the exact target.
- **Tie or a source failing.** A tie prefers Horizon. A failed or slow source is dropped; the other
  still answers. Receive quotes split the source timeout between sizing and forward quoting. All
  sources answering "no route" is `404`; all failing is `502`.
- **Missing trustline.** A route into an asset the sender does not hold is quoted without a
  transaction and flagged `requiresTrustline`; the wallet adds the trustline, then asks again.
  LI.FI skips pairs needing a new trustline; it competes after another source opens it.
- **Soroban tokens.** Assets are `XLM`, `CODE:ISSUER` or a contract id. A Soroban token needs its
  `decimals` in the request; classic assets are fixed at 7.

## Trust model: what the backend checks before it returns an aggregator transaction

The XOXNO service is a third party, so its answer is treated as untrusted input.

- The quote must match the request (assets, amount, decimals) and its minimum output must respect the
  requested slippage.
- The transaction must be one operation calling `execute_strategy` on the **pinned router** for the
  network, from the sender, with a bounded fee.
- Each authorization root must equal the approved router call. Beneath it, only direct transfers of
  the **source token** from sender to router are allowed, totalling at most the input amount.
- The route payload inside the call must name the same input and output tokens and carry a minimum
  output at least the quoted floor.
- Anything else is dropped as if the source had no answer. The wallet repeats these checks against the
  same pinned router before signing, so a compromised backend cannot redirect a signature.

## Token lists

- `GET /api/v1/swap/tokens` lists the tokens the aggregator can route **and** XOXNO marks swappable
  (name, decimals, icon, USD price, kind: native, classic or Soroban).
- `GET /api/v1/swap/tokens?scope=all` is a compact catalog of every registered token (logo and price),
  so the wallet can show a logo and price for tokens its own sources do not know.
- **Caching.** Descriptive metadata and asset classifications are kept for the life of the process,
  and a token is classified at most once. Each fetched row keeps its checked decimals, price and
  swappable flag until the cache expires (`--swap-token-cache-ttl`). If a refresh fails the last list
  is served, but a price older than three minutes is dropped rather than shown stale. With no list ever fetched, the route
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

## LI.FI same-chain provider

Enable independently with `--swap-lifi-enabled` / `SWAP_LIFI_ENABLED`. Optional
`--swap-lifi-api-key` / `SWAP_LIFI_API_KEY` stays on the backend. The public unauthenticated
API has a small quote quota; use a backend key for regular traffic. No LI.FI SDK or new
wallet dependency is needed. Enabling sends the sender address to `li.quest`.

LI.FI is pubnet-only, same-chain, same sender/recipient, exact-input execution. Receive-mode
input sizing stays with Horizon/XOXNO; LI.FI joins the forward comparison. Responses use the
existing amounts, minimum, fee, transaction and alternatives fields with `source: "lifi"`.
Output already includes LI.FI's 25 bps input fee, so selection compares net output.
The simulated XDR's sequence and expiry are verified and preserved, never restamped.
The mobile review, Blockaid scan, local signer, submission, confirmation and history are reused.

Both backend and mobile bind the LI.FI wrapper payload and nested Soroswap call to the
reviewed tokens, gross/net input, sender, deadline and positive minimum output. Exactly
one source-account authorization is allowed, with one fee transfer and the expected swap.
All sender transfers are leaf source-token transfers; total spending equals the gross input.
Initial support is Aquarius routes through Soroswap, including multiple hops and reverse
hops with sorted Aquarius token pairs. Unknown venue authorizations are rejected; add
Soroswap/Phoenix routes only after their dynamic pool destinations are independently checked.
LI.FI slippage failures currently use the existing generic failure UI.

Trust is in the pinned, upgradeable third-party contracts. The official LI.FI deployment
[lists the wrapper](https://docs.li.fi/introduction/lifi-architecture/smart-contract-addresses).
Its deployed WASM was checked by unsigned simulation and bytecode: it checks sender input
and output balance deltas and enforces its minimum; its Soroswap registry also pins the
[Soroswap code hash](https://github.com/soroswap/aggregator/blob/84de10e0f8d26168b4a76f8c23b963e50917517c/public/mainnet.contracts.json).
The LI.FI wrapper source repository is private, limiting independent source audit.
The pinned fee recipient is observed in official API quotes; independent ownership
attribution was not available. A contract upgrade or changed auth/fee shape can require
revalidation; unfamiliar envelopes fail closed.

## Usage

# Deploy to Cloudflare

[![Deploy to Cloudflare](https://deploy.workers.cloudflare.com/button)](https://deploy.workers.cloudflare.com/?url=https://github.com/hexvision/hexvision/tree/main/app)

## Env vars

If you are self-hosting relay or other components, update the env vars accordingly in the Cloudflare worker > Settings > Build > Variables and secrets.

For example, in production we replace this

```
VITE_RELAY_API_URL=http://127.0.0.1:8020
```

with

```
VITE_RELAY_API_URL=https://api.unblink.net
```

# Development

```sh
bun i
bun dev
```

# Run locally

```sh
bun start
```

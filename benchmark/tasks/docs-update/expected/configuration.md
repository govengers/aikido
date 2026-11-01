# Configuration

Last updated: 2026-11-01

The service is configured through environment variables only. Unknown
variables are ignored; invalid values stop the service at startup.

## Server

| Variable | Default | Description |
| --- | --- | --- |
| `LISTEN_ADDR` | `:8080` | Address the HTTP server binds to |
| `METRICS_ADDR` | `:9090` | Internal address for `/metrics` |

## Authentication

Every request needs an API key in the `Authorization: Bearer <key>` header.
Keys are managed in the admin console.

| Variable | Default | Description |
| --- | --- | --- |
| `AUTH_ISSUER` | (none) | OIDC issuer for admin logins |
| `API_KEY_CACHE_TTL` | `60s` | How long validated keys are cached |

## Rate limiting

Requests are limited per API key. Clients that exceed the limit get HTTP 429 with a `Retry-After` header.

| Variable | Default | Description |
| --- | --- | --- |
| `RATE_LIMIT_RPS` | `20` | Sustained requests per second |
| `RATE_LIMIT_BURST` | `40` | Maximum burst size |

## Logging

| Variable | Default | Description |
| --- | --- | --- |
| `LOG_LEVEL` | `info` | One of `debug`, `info`, `warn`, `error` |
| `LOG_FORMAT` | `json` | `json` or `text` |

## Storage

| Variable | Default | Description |
| --- | --- | --- |
| `DATABASE_URL` | (none) | PostgreSQL connection string, required |

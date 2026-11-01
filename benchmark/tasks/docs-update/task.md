Update `docs/configuration.md` for the new release:

1. Change the line `Last updated: 2026-06-14` to `Last updated: 2026-11-01`.
2. In the `## Logging` table, the default of `LOG_LEVEL` is now `info` instead of `debug` (the description stays the same).
3. Add a new section directly after the `## Authentication` section and before `## Logging`, separated by single blank lines like the other sections, with exactly this content:

```markdown
## Rate limiting

Requests are limited per API key. Clients that exceed the limit get HTTP 429 with a `Retry-After` header.

| Variable | Default | Description |
| --- | --- | --- |
| `RATE_LIMIT_RPS` | `20` | Sustained requests per second |
| `RATE_LIMIT_BURST` | `40` | Maximum burst size |
```

Do not change anything else in the file or in any other file.

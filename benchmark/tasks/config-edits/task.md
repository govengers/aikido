Make these configuration changes for the orders-api release.

In `config/app.json`:
1. Set the server port (`server.port`) to `9090`. The upstream ports stay at 8080.
2. Set the database pool maximum (`database.pool.max`) to `25`.
3. Enable the new checkout: `features.newCheckout` becomes `true`.
4. Append `"https://admin.example.com"` as the last entry of `allowedOrigins`.

In `deploy/values.yaml` (keep every comment, the key order and the existing formatting):
1. Set `replicaCount` to `3`.
2. Set the image tag to `"1.5.0"` (quoted, like now).
3. Add a new environment variable as the last item of `env`, in the same style as the others: name `FEATURE_NEW_CHECKOUT`, value `"true"`.
4. Raise the memory limit of the main container (`resources.limits.memory`) to `1Gi`. The log shipper's limit stays at 512Mi.

Do not change anything else.

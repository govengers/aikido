Remove dead code from package `notify`. These are no longer used anywhere and must be deleted, together with their doc comments:

- the function `formatLegacySubject`
- the function `dialLegacyGateway` and the constant `legacySMSGateway`
- the function `renderTemplateV1`, plus any helper that becomes unused once it is gone

Also remove imports that become unused. Keep everything that is still used, unchanged. `go build ./...`, `go vet ./...` and `go test ./...` must pass; do not change the tests.

Answer a question about this codebase. Do not change any existing file.

Question: what is the default read timeout of the gateway's HTTP server (the `ReadTimeout` of the `http.Server` that serves clients, not the upstream client), which environment variable overrides it, and in which file is that default value defined?

Write the answer into a new file `ANSWER.md` at the repository root with exactly these three lines and nothing else:

```
timeout: <the default as a Go duration string, e.g. 3s>
env: <the full environment variable name>
file: <path of the file relative to the repository root>
```

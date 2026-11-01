# gateway

Edge gateway in front of the internal services.

## Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `SERVER_READ_TIMEOUT` | `5s` | How long the server waits for a request |
| `SERVER_WRITE_TIMEOUT` | `10s` | How long a response may take |

(This table predates the move to the `internal/config` package.)

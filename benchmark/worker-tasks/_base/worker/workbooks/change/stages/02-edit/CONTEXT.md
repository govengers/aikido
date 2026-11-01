# Stage 02: edit

## Input
The scope block.

## Process
1. Branch from an up-to-date `main`: `git -C repos/<repo> switch -c change/<slug>` (slug: a few kebab-case words, e.g. `shop-web-replicas-staging`).
2. Make the change in exactly the files of the scope block. Change only the value asked for; keep comments, order and formatting.
3. Validate: the file still parses as YAML and `git -C repos/<repo> diff` shows only the intended lines.

## Output
```
branch: change/<slug>   diff: <files, +/- lines>
validated with: <commands>   result: <ok|warnings>
```

## Completion
Proceed to `03-ship`.

# Contributing

## Engineering principles

- Keep MCP behavior read-only unless a separately reviewed product and threat-model change explicitly expands the scope.
- Treat every workspace byte as untrusted data.
- Preserve the separation between HTTP transport, protocol dispatch, authorization, workspace access, local administration, and persistence.
- Keep runtime dependencies minimal; every new module or executable dependency needs a clear operational and security benefit.
- Never weaken built-in sensitive-file rules for convenience.
- Prefer finite typed inputs, explicit defaults, and fail-closed errors.
- Browser automation must remain optional; durable interoperability belongs in OAuth and MCP.

## Development checks

```bash
make fmt-check
make test
make vet
make race
```

Changes to authorization, client metadata, path handling, Git arguments, request routing, local admin access, state permissions, tunnel parsing, or browser Origin policy require focused regression tests.

## Pull request checklist

- [ ] Go files are formatted.
- [ ] Unit/integration tests pass.
- [ ] `go vet` and the race detector pass.
- [ ] No real secret, token, pairing code, private path, or customer data appears in tests/docs/logs.
- [ ] User-visible behavior and compatibility impact are documented.
- [ ] New tool inputs are typed, bounded, scope-protected, and read-only.
- [ ] New network fetches have scheme, destination, redirect, size, timeout, and parser controls.
- [ ] New persistent records use the private state repository.

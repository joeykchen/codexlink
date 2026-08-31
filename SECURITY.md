# Security Policy

## Supported version

Security fixes target the latest released CodexLink `1.x` version.

## Private reporting

Report vulnerabilities privately to the repository maintainer. Include:

- affected version and platform;
- minimal synthetic reproduction steps;
- expected and observed behavior;
- impact and reachable trust boundary;
- a proposed fix or test, when available.

Do not include real tokens, pairing codes, private repository contents, personal host paths, tunnel credentials, account data, or customer information. Do not publish a working exploit before a fix is available.

## High-priority scope

- workspace traversal or symlink escape;
- sensitive-file, Git diff, search, or directory disclosure;
- public access to `/admin/*` or `/setup/*`;
- OAuth client-registration, PKCE, issuer, audience, scope, token, or pairing bypass;
- CIMD SSRF, redirect, DNS-rebinding, or metadata-confusion flaws;
- raw credential persistence or log leakage;
- command execution through Git, search, browser opening, or tunnel input;
- any remote file-write or shell capability;
- cryptographic randomness or token-family replay failures.

See [`docs/security.md`](docs/security.md) for the complete threat model.

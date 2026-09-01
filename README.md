# CodexLink

CodexLink gives ChatGPT read-only, authenticated access to a local coding workspace while Codex keeps responsibility for editing files, running commands, and testing changes.

## Install and start

Run one command from the workspace you want to connect:

```sh
curl -fsSL https://raw.githubusercontent.com/joeykchen/codexlink/main/install.sh | sh
```

Windows PowerShell:

```powershell
irm https://raw.githubusercontent.com/joeykchen/codexlink/main/install.ps1 | iex
```

The installer detects the operating system and CPU, downloads a checksum-verified self-contained package, safely extracts it, installs CodexLink and `cloudflared` as one recoverable transaction, updates the user PATH, provisions Git when it is absent, and starts onboarding. End users do **not** install Go, Homebrew, ripgrep, or cloudflared themselves.

The bootstrap uses basic facilities normally supplied by the operating system: a shell, an HTTPS downloader, archive extraction, and SHA-256 verification. A system permission prompt can appear when Git must be installed. Supported release targets are macOS Intel/Apple silicon, Linux AMD64/ARM64, and Windows AMD64.

After installation, any workspace starts with:

```sh
cd /path/to/project
codexlink
```

## First-time ChatGPT confirmation

Local installation and startup are automated, but **the ChatGPT account owner must approve each new MCP app once**. This confirmation verifies the workspace, read-only permissions, and OAuth authorization. CodexLink never bypasses sign-in, CAPTCHA, two-factor authentication, or an authorization prompt.

For the first connection of each workspace:

1. Run `codexlink` and keep the local Setup page open. It shows:
   - the app name, such as `CodexLink · spx`;
   - the MCP endpoint, such as `https://…/mcp`;
   - a one-time pairing code.
2. Open **ChatGPT on the web** and make sure Developer mode is enabled for the current account. The entry point is usually one of:
   - `Settings → Apps → Advanced Settings → Developer mode`; or
   - `Workspace settings → Apps → Create`.

   Availability depends on the ChatGPT plan, workspace role, and administrator policy. If Create is not visible, a workspace administrator must grant the required access.
3. Create a custom app:
   - Name: copy the app name from the Setup page;
   - MCP Endpoint: copy the endpoint from the Setup page;
   - Authentication: select `OAuth`.
4. Click `Scan Tools`.
5. When ChatGPT opens the CodexLink authorization page:
   - verify the workspace name;
   - verify that the requested permissions are read-only;
   - enter the one-time pairing code;
   - approve the connection.
6. Return to ChatGPT and wait for the tool scan to finish. Confirm that the eight read-only tools are present, then click `Create`.
7. Start a new chat, select the CodexLink app, and send:

```text
Call workspace_info and confirm the connected workspace.
```

A workspace normally needs this confirmation only once. Repeat it when:

- `codexlink unpair` was run;
- OAuth credentials were revoked or cannot be refreshed;
- the ChatGPT app was deleted or recreated;
- a temporary tunnel address changed and the Setup page reports that the app must be updated.

ChatGPT custom-app availability and UI can change. See OpenAI's current [Developer mode and MCP apps documentation](https://help.openai.com/en/articles/12584461-developer-mode-and-full-mcp-connectors-in-chatgpt) for the latest product flow.

## Minimal model

```text
ChatGPT -- OAuth + MCP --> CodexLink -- read-only --> workspace
Codex   ---------------- edit / shell / test ------> workspace
```

One workspace directory is one authorization boundary. The bridge exposes eight read-only tools:

- `workspace_info`
- `list_directory`
- `read_file`
- `search_workspace`
- `git_status`
- `git_diff`
- `test_status`
- `execution_summary`

No public tool can write files, execute commands, install packages, commit, or push.

## Common commands

```sh
codexlink          # idempotent setup/start
codexlink status
codexlink doctor
codexlink pair
codexlink unpair
codexlink stop
```

## Development

End users use the release installer above. Contributors with Go installed can build from source:

```sh
make check
make build
make install-dev
```

Architecture, protocol, security, installation, and operations documents live under [`docs/`](docs/).

CodexLink is an independent community project and is not an official OpenAI product.

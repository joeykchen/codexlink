# CodexLink Design

## 1. Product contract

CodexLink connects one local coding workspace to ChatGPT without transferring execution authority.

```text
ChatGPT: inspect, reason, plan, review
Codex:   edit, execute, test, commit
Bridge:  authenticate and expose bounded read-only tools
```

The product is intentionally not a general remote shell, coding agent, file synchronization service, or repository uploader.

## 2. Design principles

### Read-only by construction

Security does not depend on a prompt saying “do not edit.” The MCP registry contains no mutating tool. A prompt injection, scope bug, or confused model therefore cannot turn a read operation into a shell command or file write through CodexLink.

### One directory is one trust boundary

Every Bridge instance owns exactly one canonical workspace root. Workspace identity, OAuth state, endpoint metadata, execution records, tunnel configuration, and runtime state are keyed to that boundary.

### One-command local setup

The default command is the setup workflow:

```bash
codexlink
```

The workflow is idempotent. It configures Codex, ensures the daemon and tunnel are healthy, checks the saved endpoint and live authorization, and opens a local setup page only when authorization is missing, stale, explicitly forced, or bound to a changed URL.

### Standards before UI automation

The durable data plane is OAuth + MCP. Browser automation is used only as a control-plane convenience through the Codex Skill. The bridge never depends on ChatGPT page structure for authorization or tool execution.

### Explicit bounds everywhere

Files, directories, searches, diffs, HTTP bodies, pending authorization requests, token lifetimes, pairing attempts, logs, and execution history all have limits. Unbounded behavior is treated as a defect.

### Standard library first

Runtime code uses the Go standard library. External programs are optional capabilities:

- `git` for Git inspection;
- `rg` for faster search, with a built-in fallback;
- `cloudflared` for public HTTPS connectivity.

## 3. User experience layers

### Local setup layer

The CLI configures local state and opens a random, loopback-only setup URL. The page contains only the exact values needed to create the ChatGPT App. It has no remote assets, no analytics, no network calls, no form submission, and expires with the pairing session.

### Data plane

ChatGPT calls the MCP endpoint over HTTPS. OAuth scopes and tool-level checks restrict what each token can invoke. Tool results are structured and identify repository content as untrusted data.

### Control plane

The installed Codex Skill coordinates compact task states:

```text
INIT → PLAN → EXECUTED → PLAN | DONE | BLOCKED
```

Only task state, goals, decisions, and short summaries travel through the chat control plane. Files, diffs, and execution records stay on the MCP data plane.

## 4. Configuration model

Optional `.codexlink.json`:

```json
{
  "name": "workspace-name",
  "maxIterations": 12,
  "chatgptProfile": "current"
}
```

- `name` changes the human-facing workspace/App label;
- `maxIterations` bounds autonomous review loops to 1–50, default 12;
- `chatgptProfile` is a validated policy enum, not arbitrary prompt text.

Optional `.codexlinkignore` adds read denials. It cannot negate the built-in sensitive-file policy.

## 5. State model

Durable state lives outside the project directory in an OS-conventional private directory. Domain packages own schemas; `state.Repository` owns path validation, private directory creation, owner-only file permissions, atomic JSON replacement, JSONL append, and removal.

Runtime state and locks are ephemeral. OAuth clients and token hashes, endpoint metadata, tunnel configuration, session metadata, and execution records are durable.

## 6. Non-goals in 1.0

- first-class multi-repository workspace groups;
- write or command MCP tools;
- automatic package installation with administrator privileges;
- a private ChatGPT API or undocumented account-token integration;
- guaranteed browser automation across every future ChatGPT UI revision;
- unattended bypass of login, CAPTCHA, 2FA, approvals, or workspace policy.

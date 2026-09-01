# Structured control responses

Repository access remains read-only. `submit_control_response` writes only a bounded, expiring response to bridge-owned state outside the workspace; it cannot modify files or execute commands. Test strings are inert labels and are never executed by CodexLink.

```sh
codexlink control prepare --task-id cl_deadbeef --iteration 0 --json
codexlink control wait --request-id cr_... --task-id cl_deadbeef --iteration 0 --timeout 90m --json
codexlink control get --request-id cr_... --task-id cl_deadbeef --iteration 0 --json
codexlink control cancel --request-id cr_... --task-id cl_deadbeef --iteration 0 --json
```

Only the loopback admin API can prepare, read, or cancel slots. Only an OAuth token explicitly granted `control.respond` can submit. Existing grants are not silently upgraded; rescan and reconnect to approve the new scope.

Submissions contain `request_id`, `task_id`, `iteration`, `state`, required `summary`, and—for `PLAN` only—1–16 bounded steps. States are `PLAN`, `DONE`, `BLOCKED`, or `ERROR`; unknown fields are rejected. Request IDs contain 192 random bits. The first response is immutable, identical retries are idempotent, and conflicting retries fail closed. Slots default to 2 hours (allowed 1 minute–4 hours), so the standard 90-minute wait leaves recovery time before expiry; submitted responses remain for 30 minutes, and expired state is pruned.

Limits are enforced before persistence: task IDs match `cl_` plus eight lowercase hex characters; iteration is 0–1,000,000; summary is 4 KiB; each description is 2 KiB; each plan has at most 16 steps, 32 file labels per step (512 bytes each), and 16 test labels per step (1 KiB each). A canonical submission is at most 64 KiB, there are at most 16 active records, and workspace control state is at most 1 MiB. Admin request bodies are 16 KiB, each long poll is at most 25 seconds, and total CLI waiting is at most 4 hours.

Lifecycle is `pending → submitted → expired` or `pending → cancelled`. Prepare is idempotent for the task/iteration tuple, including after submission. A cancel that loses a race returns `cancelled:false` together with the submitted response. Receipts contain correlation and timestamps but never echo summary or plan content. State is a regular, non-symlink, owner-only file in the CodexLink state directory, whose real filesystem ancestry must be outside the workspace.

Browser DOM, message Copy, cropped OCR, and minimal user paste are compatibility fallbacks only. After a wait timeout, perform `control get`; while the slot remains pending and unexpired, wait again rather than cancelling it. Cancel only when explicitly abandoning the request or moving to the compatibility fallback.

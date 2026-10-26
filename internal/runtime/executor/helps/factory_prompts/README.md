# Pinned Factory Droid prompt profile

These assets are unmodified copies of the static extraction from Droid **0.249.0**.
Binary SHA-256: `ef63246cbdd93cd1f97e6d058e31d27fb135bf46b258a4ce179434f0c62e2455`.
The original extraction was recorded in `.amp/in/artifacts/droid-prompts/`.
No credentials or session transcripts were read. The binary path in provenance.json
is historical provenance only; runtime uses embedded assets, never that path or `.amp`.

## Fidelity and profile choices

The interactive and noninteractive files are full evaluated base prompts, including
the identity, not approximations or identity-only substitutes. They assume
non-airgapped operation, dedicated search tools, non-script-only execution, and
communication V2 disabled. Their source SHA-256 values are independently pinned in
the helper tests. The recovered noninteractive builder's selection at all original
runtime call sites was not exhaustively traced.

Strict mode replaces caller instructions with the selected complete base and exact
extracted model reminders. Model lookup uses exact upstream model IDs from the
recovered map; unknown models get the full base without speculative reminders.
Plain-prose, TodoWrite, and model reminders follow the recovered assembly order.
TodoWrite requires an actual tool named TodoWrite. Gemini riskLevel checks the
Execute (also accepted as execute-cli) tool's schema. Gemini search guidance selects
the dedicated-tools variant when Grep, Glob, or LS exists. Native Claude tools,
OpenAI function tools, Responses tools, and Gemini function declarations are read.

The artifacts do not establish per-model batchTodoWrite flags, so the helper does
not invent them. It also does not invent session, mission, service-principal,
prompt-space, output-style, or script-only context. Transport cache metadata is not
synthesized. This is a pinned profile, not a promise of byte-identical equivalence
to every live Droid session.

**Compatibility warning:** the copied base and reminders reference native Droid
tools (including AskUser, FetchUrl, Execute, and TodoWrite) and terminal rendering.
Those names and capabilities may not match a client's tools. Cloaking does not
install, rename, or emulate them. Non-strict mode preserves caller blocks and cache
metadata and only prepends the extracted identity.

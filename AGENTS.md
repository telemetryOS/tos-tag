# tos-tag agent guide

Read `CLAUDE.md` completely before changing this repository. It is the local
implementation contract. `architecture.md` is authoritative when design
documents differ, and `IMPLEMENTATION_CHECKLIST.md` tracks verified progress.

Current initiative constraints:

- Slack is stubbed. Do not request, load, or use Slack credentials.
- Do not enable Socket Mode or send live Slack messages.
- Normal tests and evals must be deterministic and network-free.
- MongoDB is the production authority; unit tests may use project-owned memory
  stores behind the same consumer interfaces.
- Keep secrets outside workers, prompts, fixtures, logs, and artifacts.
- Update the checklist only after the implementation and its named verification
  are present.

Run the narrowest relevant test first. Before completion, run `make verify` and
report any unavailable security or integration gate exactly.

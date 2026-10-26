# Foody

Foody is a personal calorie and macro (KBJU) tracker for a single user: a food photo or caption becomes an entry with calories, protein, fat and carbs, with daily total accuracy of ±10%.
Requirements and decisions: @docs/requirements.md
Architecture (components, flows, contracts, entry lifecycle): @docs/architecture.md
Plan and current stage: @docs/plan.md

The documents in `docs/` are written in Russian.

## Current stage

Stages 0–1: collecting the golden set, the model core and eval.
Do not touch the HTTP API, the entries database or Android until stage 1 is closed.

## Terms in code

- Confidence levels: `high` (green), `medium` (yellow), `low` (red)
- Entry states: `captured`, `uploaded`, `recognizing`, `needs_review`, `confirmed`, `failed`
- Item nutrition source: `label`, `official_menu`, `llm_estimate`, `manual`
- Quantity source: `caption`, `standard`, `estimate`; mass or volume source (`amount_source`): `label`, `menu`, `estimate`, `user`, `scale`
- Item basis (`basis`): `g` or `ml`; nutrition is stored per 100 basis units (`per_100`)

## Core principles (do not break)

- **The LLM does not calculate nutrition.** The model identifies the item and estimates the quantity; code calculates calories and macros from catalog data (per 100 g or 100 ml).
- **Recognition is catalog matching.** Search known items first; create a new item only when nothing matches.
- **Fat server, thin client.** All business logic lives in Go. The Android client only captures, queues and displays.
- **Abstraction at the task level, not the LLM level.** Code calls `LabelExtractor`, `MealRecognizer` and similar, never a model directly. Models, providers, fallback chains and prompt versions live only in `backend/config/models.yaml` and `backend/prompts/`.
- **Model answers are always validated** against a schema. An invalid answer leads to a retry or the next model in the chain; it never goes further into the system.
- **Lose nothing.** Keep the original photo, the raw model answer, user edits and the result. Every model call is logged (provider, model, prompt version, latency, tokens, cost, raw answer).
- **Ready for multiple users:** `user_id` in every table with user data, but no registration or roles.

## Stack

- Backend: Go, standard library `net/http` (routing via `http.ServeMux` patterns), no web frameworks.
- Database: SQLite in WAL mode via `modernc.org/sqlite` (no cgo, simple builds for the VPS). Migrations are SQL files embedded with `embed`.
- LLM: OpenAI-compatible HTTP API (Gemini, Mistral, Groq, OpenRouter) through a single adapter.
- Deployment: a single binary on a VPS (Finland, 1 core, 2 GB RAM); watch memory and avoid heavy dependencies.
- Client (later): Android, Kotlin + Jetpack Compose, ML Kit for barcodes, WorkManager for the offline queue.

## Layout

- `backend/cmd/server`: HTTP API and worker
- `backend/cmd/foody`: CLI with `eval`, `reprocess`, `seed`, `label`
- `backend/internal/...`: packages by domain (llm, tasks, catalog, entries, storage)
- `backend/config/models.yaml`: providers and model chains per task
- `backend/prompts/`: prompts, the file name carries the version (`meal_v3.md`)
- `data/`: photos, golden set (`data/golden/cases.yaml`), database file; not committed except `data/golden/cases.yaml`

## Commands

Run from the repository root:

- Build: `make build` (binaries go to `bin/`)
- Tests: `make test`
- Vet: `make vet`
- Lint: `make lint` (installs the pinned golangci-lint into `bin/` on first run)
- Everything before a commit: `make check`
- Read nutrition labels from photos (prompt debugging): `cd backend && go run ./cmd/foody label ../data/golden/<photo>`

## Language and message style

- Code comments, error strings, log messages, CLI output and other text in code, commit messages and PR descriptions are written **in English only**.
- Style for all of them: start with a lowercase letter, use the imperative mood (`add`, not `added` or `adds`), never end with a period.
  - Commit message: `add label normalization`, not `Added label normalization.`
  - Error string: `fmt.Errorf("read config: %w", err)`
  - Comment: `// skip models without an API key`
- Exception: Go doc comments start with the identifier name as Go requires (`// Registry picks models per task`), still without a trailing period.
- Exception: LLM prompts (`backend/prompts/`, `retryPrompt`) are in Russian because labels and food names are Russian. Test data may contain Russian label text.

## Code style (enforced by `make lint`, config in `backend/.golangci.yml`)

- Separate logical blocks with an empty line; do not cuddle assignments with other statements (`wsl_v5`).
- Wrap every returned error with context: `fmt.Errorf("what failed: %w", err)`; never return a bare error from another package (`wrapcheck`, `errorlint`).
- No Cyrillic in non-test string literals (`gosmopolitan`); prompt strings in code are marked with `//nolint:gosmopolitan`.
- `make check` (vet, lint, tests, build) must pass before every commit.

## Working rules

- Non-trivial changes: plan first, then code.
- Pure logic (nutrition math, label checks 4·P+9·F+4·C, ranges, answer validation) only with unit tests.
- External HTTP calls are mocked in tests; tests never use real keys.
- Secrets only from environment variables (`.env`, with `.env.example` in git). Never commit keys.
- New dependencies only with a justification; prefer the standard library.
- Reference requirement IDs (FR-xx, NFR-xx, D-xx) in commit messages and plans.
- A new architectural decision gets a short ADR in `docs/decisions/NNN-name.md` (context, decision, alternatives) plus edits to the affected sections of requirements.md or architecture.md.
- Closed a plan item: tick it in `docs/plan.md`; closed a stage: update "Current stage" here.
- Answer as briefly and densely as possible: what was asked, the completion criteria, done or not done, the next proposal.
- Do not touch `TODO.md`: this file is for the user only.

## Testing

- When writing any tests, you should make every effort to avoid using `mock.Anything` if it is possible to do without it.
- The test structure should be as flat as possible. You should minimize the use of branching to achieve test coverage and aim to replace any branching with flat calls using parameters from `testRow` (in table-driven tests).
- If the requirement is to write a component test, you must use actual system components (not mocks) to the greatest extent possible.
- The style of test naming should be consistent with the names of other tests that were written before.
- You should avoid writing your own helpers as much as possible and try to look for mechanisms already available in the service.
- Clarify any questions. Do not make changes without permission.
- Never write a test simply to achieve an "OK" status. The primary goal of a test is to rigorously verify a specific code execution flow. If a test fails but that failure aligns with the logic and purpose of the test, keep it and let me know. We need to highlight issues in the service's logic, not tweak tests to fit incorrect logic.
- If you are in the process of working out an implementation plan for a specific feature, do not view writing tests as a separate step. The implementation plan is always developed first, and the implementation itself is carried out in collaboration with the user; testing comes as a distinct stage following any implementation.
- Clarify any inaccuracies with the user; do not make assumptions.

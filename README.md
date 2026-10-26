# Foody

Personal calorie and macro tracker for a single user. A photo or a caption of food becomes an entry with calories, protein, fat and carbs. Recognition matches the food against a personal catalog of items with known nutrition (product labels, official chain menus); the model identifies the item and estimates the quantity, the code does the math.

The project is at stage 1: the model layer and nutrition label reading are in place, the HTTP API and the Android client come later.

## Requirements

- Go 1.26 or newer
- make
- API keys for at least one model provider (Gemini, Mistral, Groq or OpenRouter) to run against real models; tests need no keys

## Setup

```sh
git clone git@github.com:kudras3rdev/foody.git
cd foody
cp .env.example .env    # fill in the provider keys you have
make tools              # install the pinned golangci-lint into bin/
```

## Commands

Run from the repository root:

| Command | What it does |
| --- | --- |
| `make build` | build `foody` and `server` into `bin/` |
| `make test` | run the tests |
| `make vet` | run `go vet` |
| `make lint` | run golangci-lint with `backend/.golangci.yml` |
| `make check` | vet, lint, test and build; run before every commit |

Read nutrition labels from photos through the configured model chain:

```sh
cd backend
go run ./cmd/foody label ../data/golden/img-0.jpeg
```

The command loads keys from `../.env`, prints the quote of the nutrition text, the values per 100 g or 100 ml, failed checks and every model call. Calls are logged to `data/logs/model_calls.jsonl`.

## Layout

```
backend/
  cmd/foody/        CLI: label (eval, reprocess, seed are planned)
  cmd/server/       HTTP API and worker (stage 2)
  internal/llm/     model config, OpenAI-compatible client, fallback registry, call log
  internal/tasks/   recognition tasks: LabelExtractor
  internal/catalog/ label normalization and checks
  config/models.yaml  providers and model chains per task
  prompts/          versioned prompts
data/golden/        golden set: cases.yaml in git, photos local only
docs/               requirements, architecture, plan, decisions
```

## Documents

The documents are written in Russian.

- [Requirements](docs/requirements.md): goals, functional and non-functional requirements, decisions
- [Architecture](docs/architecture.md): components, flows, model layer, entry lifecycle
- [Plan](docs/plan.md): stages and their status
- [Decisions](docs/decisions/): architecture decision records
- [CLAUDE.md](CLAUDE.md): project rules, code and message style

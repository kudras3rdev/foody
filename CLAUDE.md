# Foody

Foody — личный счётчик КБЖУ для одного пользователя: фото или подпись еды → запись с КБЖУ, точность дневного итога ±10%.
Требования и решения: @docs/requirements.md
Архитектура (компоненты, потоки, контракты, жизненный цикл записи): @docs/architecture.md
План и текущий этап: @docs/plan.md

## Текущий этап

Этапы 0–1: сбор золотого набора и ядро моделей + eval.
Не трогать HTTP API, БД записей и Android, пока этап 1 не закрыт.

## Термины в коде

- Уровни достоверности: `high` (🟢), `medium` (🟡), `low` (🔴)
- Состояния записи: `captured`, `uploaded`, `recognizing`, `needs_review`, `confirmed`, `failed`
- Источник КБЖУ позиции: `label`, `official_menu`, `llm_estimate`, `manual`
- Источник количества: `caption`, `standard`, `estimate`; источник массы или объёма (`amount_source`): `label`, `menu`, `estimate`, `user`, `scale`
- База позиции (`basis`): `g` или `ml`; КБЖУ хранятся на 100 единиц базы (`per_100`)

## Главные принципы (не нарушать)

- **LLM не считает КБЖУ.** Модель опознаёт позицию и оценивает количество; калории и БЖУ считает код по данным каталога (на 100 г или 100 мл).
- **Распознавание = сопоставление с каталогом.** Сначала ищем среди известных позиций, новую позицию создаём только если совпадения нет.
- **Толстый сервер, тонкий клиент.** Вся бизнес-логика — в Go. Android-клиент только снимает, ставит в очередь и показывает.
- **Абстракция на уровне задач, а не LLM.** Код вызывает `LabelExtractor`, `MealRecognizer` и т.п., а не модель напрямую. Модели, провайдеры, цепочки fallback и версии промптов — только в `backend/config/models.yaml` и `backend/prompts/`.
- **Ответ модели всегда валидируется** по схеме. Невалидный ответ → повтор или следующая модель в цепочке; дальше по системе он не проходит.
- **Ничего не теряем.** Храним исходное фото, сырой ответ модели, правки пользователя и итог. Каждый вызов модели пишется в журнал (провайдер, модель, версия промпта, задержка, токены, стоимость, сырой ответ).
- **Задел под многопользовательность:** `user_id` во всех таблицах с данными пользователя, но регистрации и ролей нет.

## Стек

- Backend: Go, стандартная библиотека `net/http` (роутинг через паттерны `http.ServeMux`), без веб-фреймворков.
- БД: SQLite в режиме WAL через `modernc.org/sqlite` (без cgo — простая сборка под VPS). Миграции — SQL-файлы, встроенные через `embed`.
- LLM: OpenAI-совместимый HTTP API (Gemini, Mistral, Groq, OpenRouter) через один адаптер.
- Деплой: один бинарник на VPS (Финляндия, 1 ядро, 2 ГБ RAM) — следи за памятью и не добавляй тяжёлых зависимостей.
- Клиент (позже): Android, Kotlin + Jetpack Compose, ML Kit для штрихкодов, WorkManager для офлайн-очереди.

## Структура

- `backend/cmd/server` — HTTP API и воркер
- `backend/cmd/foody` — CLI: `eval`, `reprocess`, `seed`
- `backend/internal/...` — пакеты по доменам (llm, tasks, catalog, entries, storage)
- `backend/config/models.yaml` — провайдеры и цепочки моделей по задачам
- `backend/prompts/` — промпты, имя файла содержит версию (`meal_v3.md`)
- `data/` — фото, золотой набор (`data/golden/cases.yaml`), файл БД; в git не коммитится, кроме `data/golden/cases.yaml`

## Команды

- Сборка: `cd backend && go build ./...`
- Тесты: `cd backend && go test ./...`
- Линт: `cd backend && go vet ./...`
- Чтение этикеток на фото (отладка промпта): `cd backend && go run ./cmd/foody label ../data/golden/<фото>`

## Правила работы

- Нетривиальные изменения — сначала план, потом код.
- Чистая логика (подсчёт КБЖУ, проверки этикеток 4·Б+9·Ж+4·У, диапазоны, валидация ответов) — только с юнит-тестами.
- Внешние HTTP-вызовы в тестах мокаются; реальные ключи в тестах не используются.
- Секреты только из переменных окружения (`.env`, в git — `.env.example`). Никогда не коммитить ключи.
- Новые зависимости — только с обоснованием; предпочитать стандартную библиотеку.
- Ссылаться на идентификаторы требований (FR-xx, NFR-xx, D-xx) в описаниях коммитов и планов.
- Новое архитектурное решение → короткий ADR в `docs/decisions/NNN-название.md` (контекст, решение, альтернативы) и правка затронутых разделов requirements.md или architecture.md.
- Закрыл пункт плана → отметить его в `docs/plan.md`; закрыл этап → обновить раздел «Текущий этап» здесь.
- Отвечай максимально емко и кратко: что просили сделать, критерии выполнения, сделал/не сделал, следующее предложение
- Не трогай файл TODO.md - этот файл предназначен ТОЛЬКО для меня.

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

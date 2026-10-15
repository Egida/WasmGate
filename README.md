# WasmGate

> Высокопроизводительный сетевой прокси-шлюз на Go с плагинами на WebAssembly,
> управлением без простоя и встроенной защитой от DDoS.

**Статус:** research / concept (этап проектирования ВКР)
**Стек ядра:** Go (data plane) + C#/.NET (control plane) + WebAssembly (plugins)
**Кодовое имя:** WasmGate (`wasmgate`)

---

## TL;DR

`WasmGate` — это лёгкий прокси-шлюз, который закрывает три боли тяжёлых решений
(nginx, Envoy, HAProxy, Kong, OpenResty):

1. **Расширяемость «лёгким движением пальца»** — плагины пишутся на **любом языке**
   и компилируются в **WebAssembly**. Никаких C-модулей, пересборки и Lua с GC.
2. **Ноль простоя при смене логики** — **Zero-Downtime Hot Reload**: новая
   конфигурация и новые плагины подхватываются атомарно, старые соединения
   доживают и «дрейнятся».
3. **Защита в ядре** — «умная» **защита от DDoS на скользящем окне**, работающая
   в hot path без блокировок и без аллокаций на запрос.

За производительностью — отдельная дисциплина: zero-alloc fast path, отсутствие
CGO, профилирование и воспроизводимые замеры (benchstat / wrk2 / k6).

---

## Что в ядре, а что — в расширениях

| № | Фича | Роль | Состояние в коде |
|---|------|------|------------------|
| **F2** | Smart DDoS (скользящее окно) | **Ядро — первая фича** | ещё не начато (следующий шаг) |
| **F1** | Zero-Downtime Hot Reload + C# Control Plane | Усиление | каркас готов (`atomic` snapshot); Control Plane — план |
| **F3** | Deep Packet Inspection (zero-alloc JSON) | Future work | требует L7-этапа |
| **F4** | eBPF-интеграция для фильтрации | Future work | только Linux |

> Детальное обоснование выбора — в [`docs/04-diploma-features.md`](docs/04-diploma-features.md).

---

## Структура документации

| Документ | Что внутри | Кому |
|----------|------------|------|
| [`docs/01-problem.md`](docs/01-problem.md) | Проблема, персоны, user-flow AS-IS/TO-BE, гипотезы, метрики успеха | Введение в ВКР, защита |
| [`docs/02-architecture-tech.md`](docs/02-architecture-tech.md) | Архитектура, стек, библиотеки, кодстайл, ADR, плагинная модель | Гл. 2 (проектирование) |
| [`docs/03-mvp.md`](docs/03-mvp.md) | MVP (L4 TCP), скоуп, DoD, **методика замеров baseline** | Гл. 3–4 (реализация/оценка) |
| [`docs/04-diploma-features.md`](docs/04-diploma-features.md) | Разбор 4 фич, матрица impact/effort, выбор ядра | Обоснование новизны |
| [`docs/05-roadmap-thesis.md`](docs/05-roadmap-thesis.md) | План работ, milestones, структура ВКР, артефакты защиты | Планирование |
| [`docs/07-glossary.md`](docs/07-glossary.md) | Глоссарий терминов (L4, splice, RCU, WASM, sliding window…) | Памятка |
| [`DEVELOPMENT.md`](DEVELOPMENT.md) | Команды, структура кода, рецепты «как добавить X» | Ежедневная работа |

---

## Технологический стек (кратко)

- **Data Plane:** Go 1.23+, стандартный `net` (горутина на соединение, epoll под
  капотом), zero-copy `splice(2)` на Linux. `gnet` — опциональная оптимизация.
- **Plugin Runtime:** [`wazero`](https://github.com/tetratelabs/wazero) — WebAssembly
  рантайм на чистом Go, **без CGO**, sandbox по памяти/CPU/времени.
- **Control Plane:** C#/.NET 8, ASP.NET Core + **gRPC** (UDS) для управления.
- **Наблюдаемость:** Prometheus, OpenTelemetry, `zerolog`, `pprof`.
- **Замеры:** `wrk2`, `k6`, `benchstat`, `pprof`, `iperf3`.

---

## Диаграмма (одним взглядом)

```
            ┌──────────────────────── Контроль (Control Plane) ────────────────────────┐
            │  C# / .NET 8 · ASP.NET Core · gRPC                                        │
            │  • версии конфига   • реестр WASM-плагинов   • health-gate   • rollback   │
            └───────────────────────────────┬──────────────────────────────────────────┘
                                            │ gRPC over Unix Domain Socket
                                            ▼
   clients ─────────▶  ┌──────────────────────────────────────────────────────────────┐
                       │                 Data Plane (Go, fast path)                    │
                       │  net accept-loop → TLS/SNI peek → DDoS sliding window →        │
                       │  routing/balancer → upstream pool → WASM hooks (wazero)       │
                       │  atomic.Pointer[Config]  ⇒  HOT RELOAD без рестарта            │
                       └───────────────────────────────┬──────────────────────────────┘
                                                       ▼
                                                  upstreams
```

---

## Как читать эти документы

1. Начни с [`01-problem.md`](docs/01-problem.md) — понять, *какую боль* решаем.
2. Затем [`02-architecture-tech.md`](docs/02-architecture-tech.md) — *как* устроено и на чём.
3. [`03-mvp.md`](docs/03-mvp.md) — *что делаем первым* и *как меряем*.
4. [`04-diploma-features.md`](docs/04-diploma-features.md) — *чем отличаемся* и *почему*.
5. [`05-roadmap-thesis.md`](docs/05-roadmap-thesis.md) — *план и структура диплома*.
6. [`DEVELOPMENT.md`](DEVELOPMENT.md) — *как работать с кодом и командами*.

---

## Быстрый старт (MVP)

Текущее состояние: **M0/M1 — рабочий L4 TCP reverse proxy** (round-robin /
least-conn балансировка, graceful shutdown, atomic snapshot под будущий hot
reload). Сложные фичи ещё не реализованы, но под них заложены «швы».

> 📘 Полный гайд по командам, структуре кода и рецептам («как добавить X») —
> в [`DEVELOPMENT.md`](DEVELOPMENT.md).

```bash
go build -o bin/wasmgate.exe ./cmd/wasmgate     # сборка
go test ./...                                    # тесты (‑race требует CGO)
.\bin\wasmgate.exe -config configs\wasmgate.yaml # запуск
```

Направьте `pools.*.servers` в примере конфига на свои TCP-сервисы. Останов —
Ctrl+C (graceful drain). На Linux `SIGHUP` перечитывает конфиг без рестарта.

### Структура репозитория

```
cmd/wasmgate/        точка входа + обработка сигналов (unix/windows)
internal/config/     схема YAML, загрузка, валидация, тип Duration
internal/proxy/      data plane: листенеры, балансировка, copy/splice, snapshot
internal/observe/    логирование (log/slog)
configs/             примеры конфигурации
plugins/ benchmarks/ deployments/   заглушки под будущие milestone'ы
docs/                проектная документация (Obsidian-совместимая)
```

### Ключевые «швы» под продукт

- `internal/proxy/snapshot.go` — **`Runtime` + `Snapshot`** на `atomic.Pointer`:
  основа **Zero-Downtime Hot Reload**. Чтение в data path — без блокировок.
- `internal/proxy/proxy.go` — точка вставки **DDoS/rate-limit** (перед dial) и
  **WASM-хуков** (`on_accept`/`on_data`/`on_close`).
- `internal/proxy/upstream.go` — интерфейс **`Balancer`** для новых стратегий.

### Учтено по «граблям»

- **32/64-бит**: везде используются *типизированные* атомики (`atomic.Int64`,
  `atomic.Uint64`), которые гарантируют выравнивание и не падают на 386/arm.
  Сырые `atomic.AddInt64(&field, 1)` не используются.
- **Парсинг `time.Duration` из YAML**: обёртка `config.Duration` (yaml.v3 не
  умеет в Stringer/Duration «из коробки»).
- **Опечатки в конфиге**: `KnownFields(true)` → неизвестный ключ = ошибка.
- **Кросс-платформа сигналов**: `SIGHUP` только на Unix; на Windows — Ctrl+C.
- **Кэш-линии**: пока не паддим (осознанно отложено), но отмечено в коде.

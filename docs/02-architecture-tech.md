# 02. Архитектура и техническая аналитика

> Документ для **главы «Проектирование»**. Здесь — принципы, компоненты, стек,
> библиотеки, кодстайл, ключевые решения (ADR) и модель расширения.

---

## 1. Принципы проектирования

1. **Performance-first.** Быстрый путь (fast path) не аллоцирует память в steady
   state, не берёт блокировки, не использует reflection и `panic`.
2. **No CGO.** Всё ядро — чистый Go. Это упрощает кросс-сборку, статический
   бинарь и запуск в минимальных контейнерах. WASM-рантайм тоже без CGO.
3. **Extensibility без компромиссов.** Плагины изолированы (WASM sandbox) и
   подключаются через узкий ABI; оверхед на пустом плагине — единицы процентов.
4. **Zero-downtime по умолчанию.** Любое изменение — это публикация новой
   неизменяемой версии конфигурации (snapshot), а не мутация состояния.
5. **Наблюдаемость встроена.** Метрики/логи/трейсы — не «потом», а часть ядра.
6. **Безопасность по умолчанию.** Песочница для плагинов, лимиты ресурсов,
   принцип наименьших привилегий, отсутствие доверия к плагину.

---

## 2. Высокоуровневая архитектура

```
┌───────────────────────────── CONTROL PLANE (C# / .NET 8) ─────────────────────────────┐
│  ASP.NET Core + gRPC                                                                   │
│  ┌──────────────┐  ┌────────────────┐  ┌───────────────┐  ┌──────────┐  ┌───────────┐  │
│  │ Config Store │  │ Plugin Registry│  │ Validate/Pub. │  │ Audit Log│  │ Rollback  │  │
│  │ (версии)     │  │ (WASM + meta)  │  │ (health-gate) │  │          │  │ (N-1)     │  │
│  └──────────────┘  └────────────────┘  └───────────────┘  └──────────┘  └───────────┘  │
└───────────────────────────────────────────┬────────────────────────────────────────────┘
                                             │  gRPC (protobuf) over Unix Domain Socket
                                             │  push snapshot + ack + health
                                             ▼
┌──────────────────────────────── DATA PLANE (Go, pure) ─────────────────────────────────┐
│  ┌───────────────┐                                                                     │
│  │  accept loop  │  accept → SNI peek → DDoS (sliding window) → route/balance →        │
│  │  net + splice │  upstream dial → bidirectional copy/splice → WASM hooks → close     │
│  └───────┬───────┘                                                                     │
│          │  читают через  atomic.Pointer[Snapshot]  (RCU, без блокировок)              │
│  ┌───────▼────────┐  ┌──────────────┐  ┌──────────────┐  ┌───────────────┐  ┌─────────┐ │
│  │ config snapshot│  │ ratelimit    │  │ plugin host  │  │ metrics/logs  │  │ TLS     │ │
│  │ (immutable)    │  │ (ring window)│  │ (wazero)     │  │ (prom/otel)   │  │ (peek)  │ │
│  └────────────────┘  └──────────────┘  └──────────────┘  └───────────────┘  └─────────┘ │
└──────────────────────────────────────────┬─────────────────────────────────────────────┘
                                            ▼
                              ┌───────────────────────────┐
                              │  Upstream pool (backend)  │
                              └───────────────────────────┘
```

### Компоненты

| Компонент | Ответственность | Язык |
|-----------|-----------------|------|
| **Data Plane** | Приём соединений, маршрутизация, балансировка, copy/splice, вызов плагинов | Go |
| **Control Plane** | Хранение версий конфига, реестр плагинов, валидация, публикация, откат, аудит | C#/.NET |
| **Plugin Host** | Загрузка `.wasm`, sandbox, host functions, лимиты | Go (wazero) |
| **Config Snapshot** | Неизменяемый срез конфига, публикуемый атомарно | Go |
| **Rate Limiter** | Скользящее окно, счётчики, вердикт по соединению | Go |
| **Observability** | Метрики, логи, трейсы, pprof | Go |
| **Transport CP↔DP** | gRPC по Unix Domain Socket (быстро, локально, без сети) | Go + C# |

---

## 3. Жизненный цикл соединения (поток данных)

```
accept()
  │
  ├─ 1. TLS/SNI peek        (читаем первые байты, определяем host)
  ├─ 2. DDoS verdict        (sliding window по IP/подсети → allow/deny/throttle)
  ├─ 3. Route resolve       (snapshot → выбор upstream-пула)
  ├─ 4. Plugin hook:on_accept
  ├─ 5. Upstream dial       (по пулу, sticky/least-conn/RR)
  ├─ 6. Data loop           (bidirectional copy; на Linux — splice/zero-copy)
  │      └─ hook:on_data (опционально, per-chunk, с бюджетом)
  ├─ 7. Drain on reload     (старое соединение доживает на своей версии)
  └─ 8. close + metrics
```

**Важно:** на шагах 2–6 мы читаем `atomic.Pointer[Snapshot]` — одна атомарная
загрузка, ни одной блокировки. Любая последующая обработка использует
**зафиксированную версию** snapshot'а, поэтому смена конфига в середине
жизни соединения не ломает его.

---

## 4. Технологический стек

| Слой | Выбор | Почему | Альтернативы (и почему нет) |
|------|-------|--------|------------------------------|
| Networking (L4) | **стандартный `net`** (горутина на соединение) | Просто и предсказуемо; backpressure и per-conn состояние «из коробки»; под капотом epoll; `io.Copy` использует `splice(2)` | `gnet` (event-loop) — опция для миллионов лёгких соединений, но добавляет сложность; `fasthttp` — это HTTP |
| Zero-copy | `splice` (Linux) через `net.TCPConn.ReadFrom` | Копирование данных в ядре, а не в user-space | ручные `io.Copy` (медленнее) |
| WASM runtime | **`tetratelabs/wazero`** | Чистый Go, **нет CGO**, быстрый старт, sandbox, WASI | `wasmtime-go` (CGO), `wasmer-go` (CGO, тяжелее) |
| JSON (future DPI) | `valyala/fastjson` / `bytedance/sonic` | Zero/низкие аллокации, SIMD (sonic) | `encoding/json` (рефлексия, аллокации) |
| Config | `knadh/koanf` (или `yaml.v3` + `mapstructure`) | Лёгкий, без магии Viper | Viper (тяжелее) |
| Logging | **`rs/zerolog`** (или `zap`) | Структурные логи, zero-alloc | `logrus` (аллокации, медленнее) |
| Metrics | `prometheus/client_golang` + `open-telemetry/opentelemetry-go` | Стандарт индустрии | — |
| Rate limit | `golang.org/x/time/rate` + **свой sliding window** | Свой алгоритм — часть ядра | — |
| eBPF (future) | `cilium/ebpf` | Современный, чистый Go | `iovisor/gobpf` (устарел) |
| Control plane | **.NET 8 / ASP.NET Core / gRPC** | Строгий контракт, генерация кода | REST (слабее контракт) |
| Тесты | `stretchr/testify`, `google/go-cmp`, `testify/suite` | Стандарт | — |
| Бенчмарки | `go test -bench`, **`benchstat`**, `wrk2`, `vegeta`/`k6`, `iperf3` | Воспроизводимость + статистика | — |
| Линт | **`golangci-lint`** | Один запуск — много анализаторов | — |
| Сборка | `Taskfile`/`Make`, `goreleaser`, Docker | Кросс-сборка, релизы | — |

---

## 5. Кодстайл и соглашения

Базируется на: **Effective Go**, **Go Code Review Comments**, **Uber Go Style
Guide**, **Google Go Style Decisions**.

### 5.1. Форматирование и линт

- Форматтер: `gofmt` + `gofumpt` (строже).
- Линтер: `golangci-lint`. Базовый набор:

```yaml
# .golangci.yml (фрагмент)
linters:
  enable:
    - errcheck      # не проигнорированные ошибки
    - govet
    - staticcheck
    - revive        # стиль
    - gocritic      # оптимизации/подозрительное
    - ineffassign
    - unused
    - goimports
    - gosec         # безопасность
    - bodyclose     # закрытие HTTP-тел
    - noctx         # запросы без context
    - contextcheck
    - prealloc      # пред-аллокация слайсов в горячем коде
    - exhaustive    # полнота switch по enum
```

### 5.2. Раскладка проекта (standard layout)

```
wasmgate/                         # ← фактическая структура репозитория
├── cmd/wasmgate/                 # бинарь data plane (+ signals_unix/windows.go)
├── internal/
│   ├── config/                   # схема YAML, Load(), Duration, валидация
│   ├── proxy/                    # data plane: server, proxy, pool, upstream, copy, snapshot
│   └── observe/                  # логирование (log/slog)
├── configs/                      # примеры конфигурации (wasmgate.yaml, local-echo.yaml)
├── plugins/examples/             # (future) примеры плагинов: rust, csharp, tinygo
├── benchmarks/                   # (future) скрипты нагрузки (wrk2, k6, vegeta)
├── deployments/                  # (future) Dockerfile, compose, systemd
├── docs/                         # проектная документация
├── README.md / DEVELOPMENT.md
├── go.mod / .golangci.yml / Makefile
└── (future)
    ├── internal/ratelimit/       # sliding window DDoS  ← первая фича ядра
    ├── internal/plugin/          # wazero host, ABI, контракты, лимиты
    ├── internal/tlspeek/         # SNI peek, passthrough/terminate
    ├── internal/control/         # gRPC-клиент к control plane
    └── pkg/api/                  # сгенерированный protobuf (общий контракт)
```

> Control Plane (C#) — **отдельный репозиторий/решение** `wasmgate-control`
> (src/WasmGate.Control.Api, src/WasmGate.Control.Domain, tests/).

### 5.3. Именование и структура пакетов

- Пакеты: короткие, без `util`/`common`/`helpers`, без подчёркиваний.
- Интерфейсы — по роли (`Reader`, `Balancer`, `PluginHost`), определяются
  **на стороне потребителя** (accept interfaces, return structs).
- Экспорт — только необходимое; всё внутри `internal/`.

### 5.4. Обработка ошибок

```go
// Оборачиваем с контекстом, сравниваем через errors.Is/As.
if err := dialUpstream(ctx, addr); err != nil {
    return fmt.Errorf("dial upstream %s: %w", addr, err)
}
```

- Никаких `panic` в hot path. Паника — только фатальная инициализация.
- Sentinel-ошибки для ожидаемых кейсов, `%w` для обёртки, `errors.Join` для
  агрегации.

### 5.5. Конкурентность

- `context.Context` первым аргументом; отмена — везде, где есть I/O.
- `errgroup.Group` для связанных задач.
- **`sync.Pool`** для переиспользуемых буферов; никаких аллокаций на соединение
  в steady state.
- Предпочтение **атомикам** и неизменяемым снапшотам перед мьютексами в hot path.

### 5.6. Правила горячего пути (hot path)

| Правило | Почему |
|---------|--------|
| Не аллоцировать в steady state | Меньше GC-давления → ровнее p99 |
| Не использовать `reflect` | Непредсказуемые аллокации/CPU |
| Не брать мьютексы | Конкуренция → деградация под нагрузкой |
| Переиспользовать буферы (`sync.Pool`, ring) | Ноль аллокаций на соединение |
| Избегать интерфейсов там, где горячо | Меньше косвенных вызовов |
| Ограничивать работу плагинов по бюджету | Плагин не должен «съесть» соединение |

### 5.7. Тестирование

- Table-driven тесты, `go test -race`.
- **Fuzz** для парсеров (SNI, конфиг, будущий JSON).
- Benchmarks с `-benchmem` + `benchstat` для регрессий.
- Интеграционные тесты с реальным upstream (`httptest`, TCP-эхо).

---

## 6. Модель плагинов (WebAssembly)

**Киллер-фича:** плагин пишется на **любом языке**, компилируется в `.wasm`,
исполняется в песочнице `wazero`.

### 6.1. Контракт/A BI

- Целевой ABI: **WASI** (`wasip1`), вызовы на границе — числовые индексы +
  линейная память.
- **Host functions** (предоставляет Go-хост): доступ к метаданным соединения,
  вердикты (`allow`/`deny`/`throttle`), логирование, чтение конфига плагина.
- Хук-контракты по этапам:
  - `on_accept(conn_meta) -> verdict`
  - `on_data(chunk_ptr, chunk_len) -> action` (с бюджетом на вызов)
  - `on_close(conn_meta, stats)`

### 6.2. Безопасность песочницы

- Изоляция памяти (линейная память WASM).
- Лимиты: CPU-время (fuel/timeout), память (max pages), размер входа.
- Нет прямого доступа к сети/ФС из плагина — только через host functions.
- Версионирование ABI; несовместимость ловится на этапе публикации.

### 6.3. SDK и примеры

| Язык | Как собирать | Пример |
|------|--------------|--------|
| Rust | `cargo build --target wasm32-wasip1` | guard-плагин |
| C# | .NET WASI workload / `wasm-tools` | rate-правило |
| TinyGo / Go | `tinygo build -target=wasi` | логгер |
| AssemblyScript / JS | `asc` | трансформация метаданных |

---

## 7. Zero-Downtime Hot Reload (механика ядра)

**Идея:** конфигурация — это **неизменяемый снапшот**; переключение — атомарная
замена указателя. Читатели (обработчики соединений) не блокируются никогда.

```go
// internal/proxy/snapshot.go — текущая (упрощённая) реализация
type Snapshot struct {
    Version uint64
    Config  *config.Config     // листенеры + пулы (данные из YAML)
    Pools   map[string]*Pool   // скомпилированные пулы + живые счётчики
}

type Runtime struct{ cur atomic.Pointer[Snapshot] }

// Load — один атомарный load; вызывается на КАЖДОМ соединении.
func (r *Runtime) Load() *Snapshot { return r.cur.Load() }

// Reload строит новый снапшот и публикует его (вызывает SIGHUP / Control Plane).
func (r *Runtime) Reload(cfg *config.Config) uint64 {
    next := buildSnapshot(r.cur.Load().Version+1, cfg)
    r.cur.Store(next) // RCU: старые читатели держат прошлый, новые видят новый
    return next.Version
}
```

> Целевая (более богатая) форма `Snapshot` добавит `Routes` (маршруты по SNI),
> `Limits` (политики DDoS) и `Plugins` (активные версии WASM). Сейчас там только
> `Config` + `Pools`, но механизм ровно тот же.

**Drain-политика:**

- новые соединения получают **новый** snapshot;
- уже установленные — доживают на **своём** snapshot'е (без принудительного
  разрыва), с опциональным grace-таймаутом;
- при ошибке применения — **откат** на N-1 (snapshot всегда доступен).

**Поток применения:**

```
CP: validate → build snapshot → publish → ждёт ack + health от DP
DP: Apply(snapshot) → atomic store → ack → метрика wasmgate_config_version
```

**Транспорт CP→DP:** gRPC по Unix Domain Socket (низкая латентность, локально,
без сетевого стека), push-модель + периодический heartbeat.

---

## 8. «Умная» защита от DDoS (скользящее окно)

**Задача:** в hot path считать частоту событий (accept/conns/data) по ключу
(IP / /24-подсеть) за окно времени и принимать вердикт **без блокировок**.

**Алгоритм:** *sliding window* на кольцевых бакетах (bucketed ring counter) —
комбинация точности и скорости.

```go
// internal/ratelimit/window.go
type Window struct {
    bucketMs int64            // размер бакета (например, 1000 мс)
    slots    []atomic.Uint64  // кольцо бакетов, сумма = счётчик за окно
}
```

- Ключ → шардированная таблица (sharding по хешу ключа) со **сбросом (decay)**
  и LRU-эвикцией, чтобы память не росла неограниченно.
- Вердикты: `allow` / `throttle` (token-bucket поверх окна) / `deny` (на N сек,
  с экспоненциальным backoff при повторах).
- Дополнительные сигналы: доля ошибок/полуоткрытых соединений, всплеск по
  подсети (агрегация /24), аномалия относительно EWMA.
- Allow-list (белый список) проверяется до фильтра.

**Почему в фаст-пасе это не бьёт по перфу:** одна атомарная инкрементация +
деление на кольцо; свод по окну — O(1) на вердикт; никаких блокировок.

---

## 9. Безопасность

- **TLS:** passthrough (SNI-peek) и опциональная терминация; mTLS к control plane.
- **Плагины:** песочница WASM + лимиты ресурсов (см. §6).
- **Секреты:** вне конфиг-файлов, через env/secret store; не логируются.
- **OWASP API Security Top-10** — учитываем как чек-лист для L7-этапа.
- **Supply chain:** фиксированные версии, `govulncheck`, подпись артефактов.

---

## 10. Ключевые решения (ADR)

| ID | Решение | Альтернативы | Обоснование | Статус |
|----|---------|--------------|-------------|--------|
| ADR-1 | **`net`** (горутина на соединение) как L4-движок | `gnet`, `fasthttp` | прост, `splice`/backpressure бесплатны, достаточно быстр; `gnet` — опция после замеров | Accepted (было: gnet) |
| ADR-2 | wazero как WASM-рантайм | wasmtime/wasmer | чистый Go, **нет CGO**, быстро, sandbox | Accepted |
| ADR-3 | Snapshot + `atomic.Pointer` для reload | double-buffer с мьютексом | non-blocking RCU, 0 разрывов | Accepted |
| ADR-4 | C# control plane + gRPC/UDS | REST, файл+watch, Go control plane | строгий контракт, быстрый локальный канал | Accepted |
| ADR-5 | Стек стартует как **L4 (TCP)** | сразу L7 HTTP | быстрее получить baseline-перф | Accepted |
| ADR-6 | В ядро первой волной — **DDoS sliding window**; Hot Reload-каркас уже заложен; DPI JSON и eBPF — future work | всё сразу | фокус и измеримость | Accepted |

---

## 11. Бюджет производительности (perf budget)

| Показатель | Бюджет |
|------------|--------|
| Аллокации на fast path (steady state) | **0 alloc / conn** |
| Оверхед пустого плагина | ≤ 5 % p99 |
| Оверхед включённого DDoS-фильтра | ≤ 5 % throughput |
| Латентность прокси (L4, мкс) | +≤ 100–200 мкс |
| Память на снапшот конфига | O(числа маршрутов/плагинов) |
| Время применения конфига | < 100 мс (локально) |
| Время загрузки плагина | < 50 мс на плагин |

> Методика измерения — в [`03-mvp.md`](03-mvp.md), §8.

# DEVELOPMENT.md — гайд по репозиторию WasmGate

Практическое руководство: команды, структура кода, как ориентироваться и куда
вставлять новые фичи. Дополняет [`README.md`](README.md) и проектную
документацию в [`docs/`](docs/).

---

## Текущий статус и следующий шаг

**Реализовано (M0/M1 + каркас M2):** L4 TCP reverse proxy на `net` — приём
соединений, балансировка `round_robin`/`least_conn`, проксирование
(`io.Copy` → `splice` на Linux), `dial_timeout`, half-close, graceful drain,
reload конфига по SIGHUP через атомарный snapshot, структурные логи, сборка без
CGO. Покрыто тестами (`go test ./...`).

**Ещё не реализовано:** DDoS/rate-limit, WASM-плагины, SNI/TLS-peek, health-checks,
метрики/pprof, CLI, нагрузочные бенчмарки. Куда встраивать — §10 «Рецепты».

**Следующий шаг:** пакет `internal/ratelimit` (DDoS на скользящем окне — первая
фича ядра). Чек-лист и точка вставки — §10.3; в коде это метка `FUTURE` в
`internal/proxy/proxy.go`.

**Milestones:** M0 ✅ · M1 ✅ · M2 🟡 (каркас) · M3 ⏳ · M4–M6 ⏳ (см. `docs/05`).

---

## 0. Шпаргалка команд

| Задача               | Команда                                                                                                  |
| -------------------- | -------------------------------------------------------------------------------------------------------- |
| Собрать              | `go build -o bin/wasmgate.exe ./cmd/wasmgate`                                                            |
| Запустить без сборки | `go run ./cmd/wasmgate -config configs/wasmgate.yaml`                                                    |
| Все тесты            | `go test ./...`                                                                                          |
| Тесты без кэша       | `go test -count=1 ./...`                                                                                 |
| Покрытие             | `go test -cover ./...`                                                                                   |
| Тесты с гонками      | `go test -race ./...` *(нужен CGO + C-компилятор)*                                                       |
| Бенчмарки            | `go test -run '^$' -bench . -benchmem ./...`                                                             |
| Форматирование       | `gofmt -l .` (проверить) / `gofmt -w .` (исправить)                                                      |
| Статический анализ   | `go vet ./...`                                                                                           |
| Линтер               | `golangci-lint run`                                                                                      |
| Зависимости          | `go mod tidy`                                                                                            |
| Кросс-сборка (Linux) | `$env:CGO_ENABLED=0;$env:GOOS="linux";$env:GOARCH="amd64";go build -o bin/wasmgate-linux ./cmd/wasmgate` |

> В репозитории есть `Makefile` (`make build`, `make test`, `make bench`, …), но
> на Windows `make` обычно не установлен — используй команды `go` напрямую.

---

## 1. Требования

- **Go 1.23+** (проверено на 1.27). `go version`.
- **C-компилятор** — только для `-race` на Windows (CGO). Варианты:
  - mingw-w64 / MSYS2, либо запускать race-тесты в WSL/Linux;
  - либо пропускать `-race` локально (в CI гоняем на Linux).
- (опционально) **golangci-lint** для линта:
  `go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest`
- (опционально) **benchstat** для сравнения бенчмарков:
  `go install golang.org/x/perf/cmd/benchstat@latest`
- (опционально) **wrk2 / k6 / iperf3** для нагрузочных тестов (см. `benchmarks/`).

---

## 2. Карта репозитория

```
WasmGate/
├── cmd/wasmgate/
│   ├── main.go              точка входа: флаги, сборка графа, сигналы
│   ├── signals_unix.go      SIGINT/SIGTERM + SIGHUP(reload)  [//go:build !windows]
│   └── signals_windows.go   только Ctrl+C                    [//go:build windows]
├── internal/
│   ├── config/
│   │   ├── config.go        схема YAML, Load(), валидация, дефолты
│   │   ├── duration.go      тип Duration (парсит "3s" из YAML)
│   │   └── config_test.go   тесты загрузки/валидации
│   ├── proxy/               ← СЕРДЦЕ data plane
│   │   ├── doc.go           обзор пакета (архитектура, швы расширения)
│   │   ├── server.go        Server: листенеры, accept-loop, graceful shutdown
│   │   ├── proxy.go         Proxy.Handle: обработка одного соединения
│   │   ├── pool.go          Pool: upstream'ы + балансировщик
│   │   ├── upstream.go      upstream + интерфейс Balancer (RR, least-conn)
│   │   ├── copy.go          join(): двунаправленный copy (splice)
│   │   ├── snapshot.go      Runtime/Snapshot: atomic-конфиг (шов hot reload)
│   │   └── proxy_test.go    e2e-тест + тесты балансировщиков
│   └── observe/
│       └── log.go           конструктор логгера (log/slog)
├── configs/
│   ├── wasmgate.yaml        основной пример
│   └── local-echo.yaml      конфиг для smoke-теста (см. §5)
├── plugins/ benchmarks/ deployments/   заглушки под будущие milestone'ы
├── docs/                    01–07: проектная документация (Obsidian)
├── go.mod / go.sum
├── Makefile / .golangci.yml / .gitignore
└── README.md / DEVELOPMENT.md
```

Правило: **всё внутреннее живёт в `internal/`** (нельзя импортировать извне
модуля). `cmd/` — только «сборка проводки», без бизнес-логики.

---

## 3. Быстрый цикл разработки

Типовой цикл после правки кода:

```powershell
gofmt -w .          # 1. формат
go vet ./...        # 2. статический анализ
go test ./...       # 3. тесты
go build ./...      # 4. компиляция всего
```

Перед коммитом дополнительно:
```powershell
golangci-lint run   # линт (если установлен)
go test -race ./... # гонки (если есть C-компилятор / в WSL)
```

---

## 4. Команды подробно

### 4.1. Сборка и запуск

```powershell
# Сборка одного бинаря в bin/
go build -o bin/wasmgate.exe ./cmd/wasmgate

# Запуск (см. флаги ниже)
.\bin\wasmgate.exe -config configs\wasmgate.yaml -log-level debug

# Либо сразу без сборки
go run ./cmd/wasmgate -config configs\wasmgate.yaml
```

Флаги `cmd/wasmgate`:

| Флаг | По умолчанию | Назначение |
|------|--------------|------------|
| `-config` | `configs/wasmgate.yaml` | путь к конфигу |
| `-log-level` | `info` | `debug`/`info`/`warn`/`error` |
| `-shutdown-grace` | `15s` | сколько ждать активные соединения при останове |

Остановка: на Unix — `Ctrl+C`, `SIGTERM` (graceful), `SIGHUP` (reload конфига
без рестарта). На Windows — `Ctrl+C`.

### 4.2. Тесты

```powershell
go test ./...                                   # всё
go test -count=1 ./...                           # игнорировать кэш результатов
go test ./internal/proxy -v                      # один пакет, подробно
go test ./internal/proxy -run TestProxyForwardsBytes -v   # один тест
go test -cover ./...                             # покрытие
go test -coverprofile=cover.out ./... ; go tool cover -html=cover.out  # HTML-отчёт
go test -race ./...                              # детектор гонок (CGO!)
```

### 4.3. Бенчмарки

```powershell
# все бенчмарки с аллокациями
go test -run '^$' -bench . -benchmem ./...

# конкретный пакет + профили
go test -run '^$' -bench . -benchmem -cpuprofile cpu.out -memprofile mem.out ./internal/proxy

# сравнить «до/после» через benchstat
go test -run '^$' -bench . -benchmem ./... > new.txt
benchstat old.txt new.txt
```

Ключевые метрики: `ns/op`, `allocs/op`, `B/op`. Для fast path цель — **`0 allocs/op`** в steady state.

### 4.4. Качество кода

```powershell
gofmt -l .          # список неотформатированных файлов (пусто = ок)
gofmt -w .          # переформатировать
go vet ./...        # встроенный анализатор
golangci-lint run   # конфиг в .golangci.yml
```

### 4.5. Зависимости

```powershell
go mod tidy         # привести go.mod/go.sum в порядок (после смены импортов)
go list -m all      # список модулей
```

### 4.6. Кросс-сборка (ядро — без CGO)

```powershell
$env:CGO_ENABLED="0"
$env:GOOS="linux";  $env:GOARCH="amd64"; go build -o bin/wasmgate-linux-amd64 ./cmd/wasmgate
$env:GOOS="linux";  $env:GOARCH="arm64"; go build -o bin/wasmgate-linux-arm64 ./cmd/wasmgate
$env:GOOS="windows";$env:GOARCH="amd64"; go build -o bin/wasmgate.exe ./cmd/wasmgate
```

### 4.7. Профилирование (pprof)

Живого HTTP-эндпоинта `/debug/pprof` пока нет (появится в milestone M3).
Сейчас профилируем через тесты:

```powershell
go test -run '^$' -bench . -cpuprofile cpu.out ./internal/proxy
go tool pprof -http=:8080 cpu.out      # откроет браузер с флеймграфом
```

---

## 5. Ручной smoke-тест (без тестового кода)

Нужны два окна PowerShell: **эхо-сервер** и **клиент**. Прокси запускается между ними.

**Окно 1 — эхо-сервер на 9001:**
```powershell
$listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 9001)
$listener.Start(); Write-Host "echo on 9001"
while ($true) {
    $client = $listener.AcceptTcpClient(); $stream = $client.GetStream()
    $buffer = New-Object byte[] 4096
    while (($n = $stream.Read($buffer, 0, $buffer.Length)) -gt 0) {
        $stream.Write($buffer, 0, $n); $stream.Flush()
    }
    $client.Close()
}
```

**Окно 2 — прокси** (конфиг `configs/local-echo.yaml`: `:8080 → :9001`):
```powershell
go run ./cmd/wasmgate -config configs\local-echo.yaml -log-level debug
```

**Окно 3 — клиент через прокси:**
```powershell
$client = [System.Net.Sockets.TcpClient]::new('127.0.0.1', 8080)
$stream = $client.GetStream()
$bytes  = [Text.Encoding]::ASCII.GetBytes('hello wasmgate')
$stream.Write($bytes, 0, $bytes.Length); $stream.Flush()
$buffer = New-Object byte[] 64; $n = $stream.Read($buffer, 0, $buffer.Length)
[Text.Encoding]::ASCII.GetString($buffer, 0, $n)   # ожидаем: hello wasmgate
$client.Close()
```

Если увидел эхо — проксирование работает end-to-end.

---

## 6. Как устроен код: путь одного соединения

```
main.run()
  ├─ config.Load(path)                     схема + валидация
  ├─ proxy.NewRuntime(cfg)                 Snapshot v1 + компиляция пулов
  ├─ proxy.NewServer(log, rt)
  ├─ srv.Start(ctx)                        bind листенеров + accept-loop на каждый
  └─ ждём сигналов (Ctrl+C / SIGHUP)

Соединение:
  acceptLoop()          server.go   Accept() → горутина на соединение
    └─ Proxy.Handle()   proxy.go    rt.Load() → найти listener → взять Pool
        ├─ (FUTURE) DDoS-проверка
        ├─ pool.Next()  pool.go     балансировщик выбирает upstream (active++)
        ├─ DialContext  proxy.go    подключение к upstream (с dial_timeout)
        └─ join()       copy.go     io.Copy в обе стороны (splice), half-close
        └─ pool.Release(up)         active--
```

Ключевая идея: **конфиг читается один раз на соединение** из `Snapshot`, и это
чтение — один атомарный `Load()` без блокировок. Поэтому reload посреди
соединения его не ломает.

---

## 7. Разбор файлов и ключевых типов

### `internal/config` — чистая конфигурация

- `Config` — корень (`version`, `listeners`, `pools`).
- `ListenerConfig` — фронтальный TCP-листенер.
- `PoolConfig` — группа бэкендов (`balance`, `servers`).
- `Duration` — обёртка над `time.Duration`, чтобы YAML понимал `"3s"`.
- `Load(path)` — открыть → распарсить (`KnownFields(true)`) → дефолты → валидация.

Никакого runtime-состояния здесь нет — только данные.

### `internal/proxy` — data plane

| Файл | Тип / функция | Роль |
|------|---------------|------|
| `snapshot.go` | `Snapshot`, `Runtime` | иммутабельный конфиг + `atomic.Pointer`; **шов hot reload** |
| `server.go` | `Server` | bind листенеров, `acceptLoop`, `Shutdown` (drain) |
| `proxy.go` | `Proxy.Handle` | обработка одного соединения |
| `pool.go` | `Pool` | `Next()` (reserve) / `Release()` |
| `upstream.go` | `upstream`, `Balancer` | эндпоинт + счётчик; `round_robin`, `least_conn` |
| `copy.go` | `join` | двунаправленный pump, zero-copy `splice` |

`Runtime`:
```go
func (r *Runtime) Load() *Snapshot                 // lock-free чтение
func (r *Runtime) Reload(cfg *config.Config) uint64 // публикация новой версии
```

### `internal/observe`

`NewLogger(level)` — единая точка создания логгера (stdlib `log/slog`).

### `cmd/wasmgate`

`main.go` собирает граф, `signals_unix.go`/`signals_windows.go` дают набор
сигналов под платформу.

---

## 8. Модель конкурентности

- **1 горутина на листенер** (accept-loop).
- **1 горутина на соединение** (`Handle`).
- **+2 горутины** внутри `join` (по одной на направление copy).
- `sync.WaitGroup` в `Server` считает и accept-loop'ы, и обработчики — поэтому
  `Shutdown` дожидается полного drain.
- **Конфиг** — `atomic.Pointer`, на горячем пути **нет блокировок**.
- **Счётчики upstream** — типизированные атомики (`atomic.Int64`), безопасны на 32/64-бит.
- `sync.Mutex` только там, где не горячо: список листенеров.

---

## 9. Справка по конфигу

```yaml
version: 1

listeners:
  - name: edge              # уникальное имя (используется в логах/роутинге)
    addr: "127.0.0.1:8080"  # адрес прослушивания
    upstream_pool: web      # имя пула из pools
    max_conns: 10000        # ЗАРЕЗЕРВИРОВАНО (M2), сейчас не применяется
    dial_timeout: 3s        # таймаут подключения к бэкенду (по умолчанию 5s)
    idle_timeout: 60s       # ЗАРЕЗЕРВИРОВАНО (M2), сейчас не применяется

pools:
  web:
    balance: round_robin    # round_robin (по умолчанию) | least_conn
    servers:
      - "127.0.0.1:9001"
      - "127.0.0.1:9002"
```

Правила: имена listener'ов уникальны; `upstream_pool` обязан существовать; пул
не может быть пустым; неизвестные ключи = ошибка загрузки.

---

## 10. Рецепты: «как добавить X»

### 10.1. Новое поле конфига
1. Добавь поле с yaml-тегом в `internal/config/config.go`.
2. Задай дефолт/проверку в `applyDefaultsAndValidate`.
3. Используй в `proxy` (значение едет внутри `Snapshot`).
4. Обнови `configs/wasmgate.yaml` и `docs/`.

### 10.2. Новая стратегия балансировки
1. В `internal/proxy/upstream.go` реализуй `Balancer` (`Next`, `Name`).
2. Добавь ветку в `newBalancer`.
3. Разреши значение в валидации (`config.go`, switch `Balance`).
4. Добавь тест в `proxy_test.go`.

### 10.3. Rate-limit на скользящем окне (первая фича ядра)
- Создай `internal/ratelimit` (ring-бакеты + шарды, без аллокаций).
- В `proxy.go` на метке `// --- FUTURE: DDoS ... ---` вызови проверку **до** dial.
- Конфиг: `listeners[].rate_limit` (порог, окно) → поле в `ListenerConfig`.
- Бенчмарк: `go test -bench -benchmem ./internal/ratelimit` → цель `0 allocs/op`.

### 10.4. WASM-хуки плагинов (киллер-фича)
- Определи интерфейс хуков и хост на `wazero` в `internal/plugin`.
- Оборачивай `join` в `Handle`: `on_accept` → data loop → `on_close`.
- Плагин вызывай на **уровне соединения**, не на каждый чанк (см. `docs/02`, §6).

---

## 11. Как писать тесты

- Юнит-тесты — **table-driven**, там же, где код (`*_test.go`).
- Внешние тесты (через публичный API) — пакет `foo_test`.
- Интеграция: `startEcho(t)` поднимает TCP-эхо, дальше проверяем проксирование
  (см. `TestProxyForwardsBytes`).
- Для конфигов — `t.TempDir()` + временный файл (см. `config_test.go`).
- Всегда закрывай ресурсы через `t.Cleanup`.
- Полезно: `go test -run ... -count=1 -v`.

---

## 12. Соглашения

- **Коммиты:** Conventional Commits — `feat(proxy): …`, `fix(config): …`,
  `test(ratelimit): …`, `docs: …`, `chore: …`.
- **Ветки:** `feat/<краткое>`, `fix/<краткое>`.
- **Стиль:** Effective Go + Uber Go Style; ошибки оборачиваем через `%w`;
  `panic` в hot path запрещён; в hot path — ноль аллокаций.
- **Перед пушем:** `gofmt -w .`, `go vet ./...`, `go test ./...`, `golangci-lint run`.

---

## 13. Частые грабли

| Симптом | Причина / решение |
|---------|-------------------|
| `-race requires cgo` | Нет CGO/компилятора. `$env:CGO_ENABLED="1"` + gcc, либо гоняй `-race` в WSL/Linux |
| `read config: open ...: no such file` | Неверный `-config` (путь от рабочей директории) |
| `unknown field` при загрузке конфига | Опечатка в ключе YAML (`KnownFields(true)` специально падает) |
| `unknown upstream_pool` | Листенер ссылается на несуществующий пул |
| `invalid duration "60"` | Нужна единица: `60s`, `1500ms` |
| Импорты не находятся после смены пути модуля | `go mod tidy` |
| `SIGHUP` ничего не делает на Windows | Ожидаемо — reload только на Unix |
| Тест «висит» | Полу-открытое соединение; `idle_timeout` ещё не реализован (см. `copy.go` TODO) |

---

## 14. Куда смотреть дальше

- Проектная логика и обоснование — [`docs/`](docs/), особенно `docs/03-mvp.md`
  (методика замеров) и `docs/02-architecture-tech.md` (стек, кодстайл, ADR).
- Незнакомый термин — [`docs/07-glossary.md`](docs/07-glossary.md).
- Пакет `internal/proxy` целиком описан в `internal/proxy/doc.go`.
- Точки расширения помечены в коде комментариями `FUTURE` / `TODO`.

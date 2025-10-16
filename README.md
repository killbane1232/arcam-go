### Apex Trader (arcam-go)

Краткая документация по устройству и запуску проекта.

## Назначение
- **apex-trader**: CLI-утилита/демо, подключающаяся к BitMEX и выгружающая последние N свечей в CSV (через WebSocket; в коде есть и HTTP-вариант).
- **telebot**: заготовка Telegram-бота для управления торговым движком.
- **core**: каркас клиента биржи (BitMEX), потоков маркет-данных, индикаторов и торгового движка.

## Архитектура
- `cmd/apex-trader`: точка входа CLI, чтение `configs/config.yaml`, выгрузка свечей (WS).
- `cmd/telebot`: заглушка запуска бота.
- `pkg/config`: загрузка YAML-конфига в типизированные структуры.
- `internal/adapters/exchange/bitmex`: REST-клиент BitMEX с ретраями, лимитированием и подписью запросов.
- `internal/service/market_data`: каркас подписок на маркет-данные (BitMEX WS).
- `internal/service/indicator`: индикаторы (EMA, Aroon) и конструктор торговых сигналов.
- `internal/service/trading`: каркас торгового движка, риск-менеджера и аудитора.
- `internal/domain`: доменные сущности (`Order`, `Position`, `Trade`) и value-объекты (деньги, проценты, цены, количество).

## Поток данных (apex-trader, выгрузка свечей)
1. При старте читается конфиг `configs/config.yaml`.
2. Если `candlesDump.enabled: true`, выполняется подписка на BitMEX WS `tradeBin{binSize}:{symbol}` (`wss://www.bitmex.com/realtime`), собираются свечи до `count`.
3. Свечи пишутся в CSV (по умолчанию `candles.csv`) с заголовком: `timestamp, open, high, low, close, volume`.

Примечание: В коде есть HTTP-вариант (`/api/v1/trade/bucketed`), но `apex-trader` по умолчанию использует WS-реализацию.

## Конфигурация
Файл: `configs/config.yaml`

```yaml
app:
  env: development
  logLevel: info

bitmex:
  baseURL: https://www.bitmex.com
  wsURL: wss://www.bitmex.com/realtime
  timeoutMs: 15000

trading:
  symbol: XBTUSDT
  leverage: 5

candlesDump:
  enabled: true
  symbol: XBTUSD
  binSize: 1m
  count: 10
  output: candles.csv
  reverse: true
```

- **app.env / app.logLevel**: окружение и уровень логирования.
- **bitmex.baseURL / wsURL / timeoutMs**: REST/WS endpoints и таймауты.
- **trading.symbol / leverage**: торговые настройки для движка (движок пока не интегрирован в CLI).
- **candlesDump.enabled**: включает выгрузку свечей из WS при старте `apex-trader`.
- **candlesDump.symbol**: инструмент BitMEX (например, `XBTUSD`).
- **candlesDump.binSize**: размер свечи (`1m`, `5m`, `1h`, `1d`, ...).
- **candlesDump.count**: сколько последних свечей собрать.
- **candlesDump.output**: путь к CSV файлу.
- **candlesDump.reverse**: влияет на порядок в HTTP-методе; для WS не критично.

## Переменные окружения
Для REST-клиента BitMEX предусмотрены `apiKey`/`apiSecret` (см. `internal/adapters/exchange/bitmex/client.go`), но в текущем CLI они не используются. Если будете интегрировать торговлю, задайте ключи через окружение и передайте в клиент при инициализации.

Пример (PowerShell):
```powershell
$env:BITMEX_API_KEY="..."; $env:BITMEX_API_SECRET="..."
```

## Установка и запуск
Требуется Go 1.21+.

- Установка зависимостей:
```bash
go mod download
```

- Запуск `apex-trader` (выгрузка свечей в CSV):
```bash
go run ./cmd/apex-trader
```
Результат по умолчанию — `candles.csv` в корне проекта.

- Сборка бинарника:
```bash
go build -o apex-trader.exe ./cmd/apex-trader
```

- Запуск заглушки `telebot`:
```bash
go run ./cmd/telebot
```

## Основные компоненты (быстрый обзор)
- Клиент BitMEX REST: `internal/adapters/exchange/bitmex/client.go` — подписанные запросы, ретраи, token bucket rate-limit.
- Поток WS (каркас): `internal/service/market_data/bitmex_stream.go` — интерфейсы подписок и событий.
- Торговый движок: `internal/service/trading/engine.go` — каналы сигналов/ордеров, контроль, риск-валидация, аудит.
- Telegram-бот (каркас): `internal/service/telebot/bot.go` — авторизация пользователей, команды (`/start`, `/status`, `/pause`, ...), меню.

## Ограничения и планы
- `cmd/telebot` сейчас заглушка; в `internal/service/telebot` есть логика бота, но она не интегрирована в CLI.
- Торговый движок и BitMEX-клиент пока не “сшиты” в исполняемом приложении; текущий CLI демонстрирует сбор свечей.
- Подписки на маркет-данные и адаптер WS отмечены как каркас для последующей реализации.

## Диагностика
- Проверьте доступность `wss://www.bitmex.com/realtime` из вашей сети.
- Убедитесь, что `candlesDump.symbol` и `binSize` валидны для BitMEX.
- При ошибках записи CSV проверьте путь в `candlesDump.output`.
- Если нужно быстро получить свечи без WS, можно временно использовать HTTP-метод (функция `dumpLastCandles` в `cmd/apex-trader/main.go`).



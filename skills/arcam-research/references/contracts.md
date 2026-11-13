# Контракт Arcam 0.1.0

Сверяй параметры с текущим `tools/list`: это памятка к реализации, а не замена discovery. JSON-RPC может завершиться успешно на HTTP-уровне и вернуть `isError=true` внутри результата инструмента. Сначала проверь ошибку, затем читай `structuredContent`; если клиент показывает только `content[].text`, распарси содержащийся там JSON.

## Инструменты

| Инструмент | Аргументы | Результат и особенности |
|---|---|---|
| `get_catalog` | `{}` | `example`, `position_example`, `d1_example`, `llm_example`, индикаторы, операции, интервалы. `live_enabled` описывает сервис и не добавляет MCP-инструментов торговли. |
| `list_strategies` | `{}` | `{versions:[{id,version,hash,strategy,...}]}`; все версии, не только последние. |
| `validate_strategy` | `{strategy_json:string}` | `{valid,fields,warmup}`; ошибка также приходит как tool error. Не сохраняет. |
| `save_strategy` | `{id,strategy_json:string}` | `{id,version,hash,strategy,...}`; всегда новая версия. |
| `list_datasets` | `{}` | `{datasets:[...]}` с источником, парой, интервалом, числом свечей и датами. Крайние даты не гарантируют отсутствие внутренних пробелов. |
| `import_history` | `{source,symbols,interval,from,to}` | `{imported:[...]}`; синхронная загрузка публичной истории Binance. Не принимает CSV-текст. |
| `run_backtest` | Объект ниже | Run с `id` и промежуточным статусом. Не ожидает окончания. |
| `list_backtests` | `{}` | `{runs:[...]}`; метаданные без больших массивов. |
| `get_backtest` | `{id}` | `{run,pairs}`; `run.strategy` — Version, `run.request` — условия. В `pairs` метрики, `data_hash`, **число** `decisions`, `complete`, состояние позиции; массивов свечей нет. |
| `get_decisions` | `{id,symbol,offset?,limit?}` | `{total,offset,decisions,candles,fills}`. Offset ≥ 0; limit 1–200, по умолчанию 50. Используй `offset + len(decisions)` для следующей страницы. При работающем расчёте `total` может расти. |
| `compare_backtests` | `{ids:[...]}` | От 2 до 10 завершённых запусков; `{same_inputs,runs}`. |
| `cancel_backtest` | `{id}` | `{cancel_requested:true}` подтверждает запрос; терминальный статус читай отдельно. |

Поля минимального явного запуска (после проверки существования версии и покрытия):

```json
{
  "strategy_id": "trend-demo",
  "version": 1,
  "source": "demo",
  "symbols": ["BTCUSDT"],
  "interval": "1h",
  "from": "2025-01-05T00:00:00Z",
  "to": "2025-01-06T00:00:00Z",
  "capital": 1000,
  "fee_bps": 4,
  "slippage_bps": 2,
  "allow_llm": false,
  "max_llm_calls": 0
}
```

Это функциональный пример на синтетике, не рекомендуемые рыночные параметры. `trend-demo@1` появляется при стандартном seed; не предполагается при `-seed-demo=false`.

## Strategy и правила

Strategy содержит `name`, `description`, `indicators`, `rules`, `risk` и необязательный `llm`. Неизвестные JSON-поля отклоняются. Пример полной небольшой стратегии:

```json
{
  "name": "EMA cross long-only",
  "description": "Гипотеза: пересечение EMA 12/26 ловит устойчивый тренд; проверять после расходов и на отложенном периоде.",
  "indicators": [
    {"id":"fast","type":"ema","period":12},
    {"id":"slow","type":"ema","period":26}
  ],
  "rules": {
    "enter_long": {"all":[
      {"position":"none"},
      {"left":{"field":"fast.value"},"op":"crosses_above","right":{"field":"slow.value"}}
    ]},
    "exit_long": {"left":{"field":"fast.value"},"op":"crosses_below","right":{"field":"slow.value"}}
  },
  "risk": {"position_fraction":0.5,"stop_loss":0.03,"take_profit":0.06}
}
```

Сериализуй этот объект один раз: `{"id":"ema-cross","strategy_json": JSON.stringify(strategy)}` — иллюстрация построения аргументов, не буквальный JSON. В Python используй `json.dumps(strategy)`.

`position_fraction` — доля текущего капитала пары `(0,1]`. `stop_loss` и `take_profit` — доли цены, например `0.03` = 3%; `0` отключает соответствующий порог. `fee_bps`/`slippage_bps` — другая единица: `4` = 0.04% на каждую сторону.

| Тип | Параметры | Доступные поля |
|---|---|---|
| `ema`, `sma`, `rsi`, `atr` | `period` | `id.value` |
| `aroon` | `period` | `id.up`, `id.down`, `id.oscillator` |
| `ichimoku` | `fast`, `slow`, `period`, `displacement` | `id.tenkan`, `id.kijun`, `id.span_a`, `id.span_b` |
| `macd` | `fast`, `slow`, `signal` | `id.line`, `id.signal`, `id.histogram` |
| `bollinger` | `period`, `multiplier` | `id.middle`, `id.upper`, `id.lower` |

Базовые поля: `open`, `high`, `low`, `close`, `volume`. Операнд — **либо** `{"field":"..."}`, **либо** `{"value":70}`. Операторы: `gt`, `gte`, `lt`, `lte`, `eq`, `crosses_above`, `crosses_below`. Проверка `gt` не равнозначна однократному пересечению.

Один узел Rule задаёт одну форму: сравнение, `all:[...]`, `any:[...]`, `not:{...}`, `position` либо `signal`. Группы вложенные. Каждому включённому входу нужен соответствующий выход; отключённую сторону убери из `rules`.

`position` принимает `none`, `long`, `short`. `signal` разрешён только в выходах и ссылается на существующее `enter_long` или `enter_short`. Например:

```json
{"all":[{"position":"long"},{"signal":"enter_short"}]}
```

Ссылка вычисляет вход как для виртуального `position=none`, сохраняя текущие индикаторы и оценки LLM. Это закрывает long по встречному сигналу; short не открывается в том же решении. Одновременные сигналы входа long/short дают hold.

`validate_strategy.fields` и `warmup` авторитетнее ручного подсчёта. Непрогретое условие остаётся неготовым даже под `not`. Короткое замыкание `all`/`any` может пропустить ветки; `skipped` не означает вычисленное false.

Ограничения текущей реализации: 32 индикатора, глубина правил 16, 256 узлов, 10 пар, 50 000 тестовых свечей на пару, 2 параллельных бэктеста, до 8 активных/ожидающих заданий. Не заполняй очередь массовым перебором без согласованного объёма исследования.

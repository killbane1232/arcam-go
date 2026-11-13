package core

import "time"

type Candle struct {
	Time   time.Time `json:"time"`
	Open   float64   `json:"open"`
	High   float64   `json:"high"`
	Low    float64   `json:"low"`
	Close  float64   `json:"close"`
	Volume float64   `json:"volume"`
}

type Indicator struct {
	ID           string  `json:"id"`
	Type         string  `json:"type"`
	Period       int     `json:"period,omitempty"`
	Fast         int     `json:"fast,omitempty"`
	Slow         int     `json:"slow,omitempty"`
	Signal       int     `json:"signal,omitempty"`
	Displacement int     `json:"displacement,omitempty"`
	Multiplier   float64 `json:"multiplier,omitempty"`
}

type Operand struct {
	Field string   `json:"field,omitempty"`
	Value *float64 `json:"value,omitempty"`
}
type Rule struct {
	Position string   `json:"position,omitempty"`
	Signal   string   `json:"signal,omitempty"`
	All      []Rule   `json:"all,omitempty"`
	Any      []Rule   `json:"any,omitempty"`
	Not      *Rule    `json:"not,omitempty"`
	Left     *Operand `json:"left,omitempty"`
	Op       string   `json:"op,omitempty"`
	Right    *Operand `json:"right,omitempty"`
}
type Rules struct {
	EnterLong  *Rule `json:"enter_long,omitempty"`
	ExitLong   *Rule `json:"exit_long,omitempty"`
	EnterShort *Rule `json:"enter_short,omitempty"`
	ExitShort  *Rule `json:"exit_short,omitempty"`
}
type Risk struct {
	PositionFraction float64 `json:"position_fraction"`
	StopLoss         float64 `json:"stop_loss"`
	TakeProfit       float64 `json:"take_profit"`
}
type LLMConfig struct {
	Provider     string `json:"provider"`
	Protocol     string `json:"protocol,omitempty"`
	Model        string `json:"model"`
	Instructions string `json:"instructions"`
	Lookback     int    `json:"lookback"`
}
type Strategy struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Indicators  []Indicator `json:"indicators"`
	Rules       Rules       `json:"rules"`
	Risk        Risk        `json:"risk"`
	LLM         *LLMConfig  `json:"llm,omitempty"`
}
type Version struct {
	ID        string    `json:"id"`
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	Hash      string    `json:"hash"`
	Strategy  Strategy  `json:"strategy"`
}
type Trace struct {
	Rule     string   `json:"rule"`
	Ready    bool     `json:"ready"`
	Result   bool     `json:"result"`
	NeedsLLM bool     `json:"needs_llm,omitempty"`
	Skipped  bool     `json:"skipped,omitempty"`
	Reason   string   `json:"reason,omitempty"`
	Left     *float64 `json:"left,omitempty"`
	Right    *float64 `json:"right,omitempty"`
	Children []Trace  `json:"children,omitempty"`
}
type Verdict struct {
	EnterLong  float64 `json:"enter_long"`
	ExitLong   float64 `json:"exit_long"`
	EnterShort float64 `json:"enter_short"`
	ExitShort  float64 `json:"exit_short"`
	Reason     string  `json:"reason"`
}
type LLMAudit struct {
	Verdict     Verdict `json:"verdict"`
	RequestHash string  `json:"request_hash"`
	Model       string  `json:"model"`
	Cached      bool    `json:"cached"`
	ResponseMS  float64 `json:"response_ms,omitempty"`
}
type Decision struct {
	LLMStatus string             `json:"llm_status,omitempty"`
	Position  string             `json:"position,omitempty"`
	Time      time.Time          `json:"time"`
	Action    string             `json:"action"`
	Reason    string             `json:"reason"`
	Features  map[string]float64 `json:"features"`
	Rules     map[string]Trace   `json:"rules"`
	LLM       *LLMAudit          `json:"llm,omitempty"`
}
type Trade struct {
	Side      string    `json:"side"`
	EntryTime time.Time `json:"entry_time"`
	ExitTime  time.Time `json:"exit_time"`
	Entry     float64   `json:"entry"`
	Exit      float64   `json:"exit"`
	Quantity  float64   `json:"quantity"`
	Fees      float64   `json:"fees"`
	PnL       float64   `json:"pnl"`
	Reason    string    `json:"reason"`
}
type Fill struct {
	Time     time.Time `json:"time"`
	Action   string    `json:"action"`
	Price    float64   `json:"price"`
	Quantity float64   `json:"quantity"`
	Reason   string    `json:"reason"`
}
type EquityPoint struct {
	Time  time.Time `json:"time"`
	Value float64   `json:"value"`
}
type Metrics struct {
	Initial        float64 `json:"initial"`
	Final          float64 `json:"final"`
	ReturnPct      float64 `json:"return_pct"`
	MaxDrawdownPct float64 `json:"max_drawdown_pct"`
	Trades         int     `json:"trades"`
	WinRatePct     float64 `json:"win_rate_pct"`
	Fees           float64 `json:"fees"`
}
type PairResult struct {
	Complete      bool          `json:"complete"`
	OpenPosition  *Position     `json:"open_position,omitempty"`
	PendingAction string        `json:"pending_action,omitempty"`
	Balance       float64       `json:"balance"`
	UnrealizedPnL float64       `json:"unrealized_pnl"`
	Symbol        string        `json:"symbol"`
	DataHash      string        `json:"data_hash"`
	Candles       []Candle      `json:"candles"`
	Decisions     []Decision    `json:"decisions"`
	Fills         []Fill        `json:"fills"`
	Trades        []Trade       `json:"trades"`
	Equity        []EquityPoint `json:"equity"`
	Metrics       Metrics       `json:"metrics"`
}
type BacktestRequest struct {
	StrategyID  string    `json:"strategy_id"`
	Version     int       `json:"version"`
	Source      string    `json:"source"`
	Symbols     []string  `json:"symbols"`
	Interval    string    `json:"interval"`
	From        time.Time `json:"from"`
	To          time.Time `json:"to"`
	Capital     float64   `json:"capital"`
	FeeBPS      float64   `json:"fee_bps"`
	SlippageBPS float64   `json:"slippage_bps"`
	AllowLLM    bool      `json:"allow_llm"`
	MaxLLMCalls int       `json:"max_llm_calls"`
}
type Run struct {
	LLMRequests      int                 `json:"llm_requests"`
	ResumeCount      int                 `json:"resume_count"`
	LastResumedAt    *time.Time          `json:"last_resumed_at,omitempty"`
	ResumeNote       string              `json:"resume_note,omitempty"`
	Failures         []RunFailure        `json:"failures,omitempty"`
	BacktestProgress *BacktestProgress   `json:"backtest_progress,omitempty"`
	LLMStats         *LLMStats           `json:"llm_stats,omitempty"`
	DataLoads        []DataLoad          `json:"data_loads,omitempty"`
	Progress         string              `json:"progress,omitempty"`
	ID               string              `json:"id"`
	Status           string              `json:"status"`
	Error            string              `json:"error,omitempty"`
	CreatedAt        time.Time           `json:"created_at"`
	CompletedAt      *time.Time          `json:"completed_at,omitempty"`
	Request          BacktestRequest     `json:"request"`
	Strategy         Version             `json:"strategy"`
	InputData        map[string][]Candle `json:"input_data,omitempty"`
	Pairs            []PairResult        `json:"pairs,omitempty"`
	Metrics          Metrics             `json:"metrics"`
}

type RunFailure struct {
	At    time.Time `json:"at"`
	Error string    `json:"error"`
}

// BacktestProgress counts evaluated candles across all pairs, excluding warmup.
type BacktestProgress struct {
	ProcessedCandles int        `json:"processed_candles"`
	TotalCandles     int        `json:"total_candles"`
	CurrentSymbol    string     `json:"current_symbol,omitempty"`
	LastCandleTime   *time.Time `json:"last_candle_time,omitempty"`
}

// LLMStats measures successful uncached responses in this run only.
type LLMStats struct {
	Responses         int     `json:"responses"`
	CacheHits         int     `json:"cache_hits"`
	Skipped           int     `json:"skipped"`
	TotalResponseMS   float64 `json:"total_response_ms"`
	AverageResponseMS float64 `json:"average_response_ms"`
}

type DataLoad struct {
	Symbol  string    `json:"symbol"`
	From    time.Time `json:"from"`
	To      time.Time `json:"to"`
	Candles int       `json:"candles"`
}

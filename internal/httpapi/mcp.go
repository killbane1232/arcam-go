package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"arcam/internal/app"
	"arcam/internal/core"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type emptyInput struct{}
type strategyInput struct {
	ID           string `json:"id" jsonschema:"Stable strategy ID"`
	StrategyJSON string `json:"strategy_json" jsonschema:"A JSON encoded Strategy. Get catalog for an example."`
}
type validateInput struct {
	StrategyJSON string `json:"strategy_json"`
}
type idInput struct {
	ID string `json:"id"`
}
type decisionsInput struct {
	ID     string `json:"id"`
	Symbol string `json:"symbol"`
	Offset int    `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}
type compareInput struct {
	IDs []string `json:"ids"`
}

func add[I any](s *mcp.Server, name, description string, fn func(context.Context, I) (any, error)) {
	mcp.AddTool(s, &mcp.Tool{Name: name, Description: description}, func(ctx context.Context, r *mcp.CallToolRequest, input I) (*mcp.CallToolResult, any, error) {
		out, e := fn(ctx, input)
		return nil, out, e
	})
}
func (s *Server) MCPServer() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "arcam", Version: "0.1.0"}, &mcp.ServerOptions{Instructions: "Research strategies using immutable versions. Missing exchange history including warmup is downloaded automatically in preparing status; demo/CSV cannot be downloaded. Position rules use none/long/short. Exit rules may reference enter_long/enter_short signals evaluated as if flat; no immediate position reversal. All dates are UTC, from inclusive, to exclusive. Demo data is synthetic. Specify explicit dates and costs; compare on out-of-sample periods. LLM inference requires allow_llm and a call budget. There are no real-order tools in this research MCP."})
	add(server, "get_catalog", "List supported indicators, operators, exchanges, example strategy and LLM configuration.", func(ctx context.Context, in emptyInput) (any, error) { return s.Catalog(), nil })
	add(server, "list_strategies", "List every immutable strategy version.", func(ctx context.Context, in emptyInput) (any, error) {
		v, e := s.App.Store.Strategies()
		return map[string]any{"versions": v}, e
	})
	add(server, "validate_strategy", "Validate a candidate without saving it. Returns available fields and warmup bars.", func(ctx context.Context, in validateInput) (any, error) {
		var v core.Strategy
		if e := Decode(strings.NewReader(in.StrategyJSON), &v); e != nil {
			return nil, e
		}
		e := core.ValidateStrategy(v)
		return map[string]any{"valid": e == nil, "fields": v.Fields(), "warmup": v.Warmup()}, e
	})
	add(server, "save_strategy", "Create an immutable version; put the hypothesis in description.", func(ctx context.Context, in strategyInput) (any, error) {
		var v core.Strategy
		if e := Decode(strings.NewReader(in.StrategyJSON), &v); e != nil {
			return nil, e
		}
		return s.App.Store.SaveVersion(in.ID, v)
	})
	add(server, "list_datasets", "List downloaded history with dates and candle counts.", func(ctx context.Context, in emptyInput) (any, error) {
		v, e := s.App.Store.Datasets()
		return map[string]any{"datasets": v}, e
	})
	add(server, "import_history", "Download public historical candles. Include warmup before the testing range; at most 50000 candles per pair per import.", func(ctx context.Context, in app.ImportRequest) (any, error) {
		v, e := s.App.Import(ctx, in)
		return map[string]any{"imported": v}, e
	})
	add(server, "run_backtest", "Queue a reproducible backtest; automatically download missing selected-source history including warmup; returns an ID. No real orders. Poll get_backtest for completion.", func(ctx context.Context, in core.BacktestRequest) (any, error) {
		v, e := s.App.StartRun(in)
		v.InputData = nil
		return v, e
	})
	add(server, "list_backtests", "List experiment status and metrics.", func(ctx context.Context, in emptyInput) (any, error) {
		v, e := s.App.Runs()
		return map[string]any{"runs": v}, e
	})
	add(server, "resume_backtest", "Resume a failed experiment using the same ID, frozen inputs, strategy and execution configuration. Completed candles and pairs are not evaluated again; the original LLM budget is preserved.", func(ctx context.Context, in idInput) (any, error) {
		v, e := s.App.ResumeRun(in.ID)
		v.InputData, v.Pairs = nil, nil
		return v, e
	})
	add(server, "get_backtest", "Get status, candle progress, average uncached LLM response time, strategy and metrics without large candle arrays.", func(ctx context.Context, in idInput) (any, error) {
		v, e := s.App.Run(in.ID)
		summaries := []map[string]any{}
		for _, p := range v.Pairs {
			summaries = append(summaries, map[string]any{"symbol": p.Symbol, "metrics": p.Metrics, "data_hash": p.DataHash, "decisions": len(p.Decisions), "complete": p.Complete, "open_position": p.OpenPosition, "pending_action": p.PendingAction, "unrealized_pnl": p.UnrealizedPnL})
		}
		v.Pairs = nil
		v.InputData = nil
		return map[string]any{"run": v, "pairs": summaries}, e
	})
	add(server, "get_decisions", "Inspect evaluated signals, rule traces, LLM rationale and fills for a pair, including while a run is still running. Maximum 200 decisions.", func(ctx context.Context, in decisionsInput) (any, error) {
		v, e := s.App.Run(in.ID)
		if e != nil {
			return nil, e
		}
		limit := in.Limit
		if limit == 0 {
			limit = 50
		}
		if limit < 1 || limit > 200 || in.Offset < 0 {
			return nil, fmt.Errorf("limit 1..200, offset >=0 required")
		}
		for _, p := range v.Pairs {
			if p.Symbol == in.Symbol {
				start := min(in.Offset, len(p.Decisions))
				end := min(start+limit, len(p.Decisions))
				fills := []core.Fill{}
				if end > start {
					for _, f := range p.Fills {
						if !f.Time.Before(p.Candles[start].Time) && !f.Time.After(p.Decisions[end-1].Time) {
							fills = append(fills, f)
						}
					}
				}
				return map[string]any{"total": len(p.Decisions), "offset": start, "decisions": p.Decisions[start:end], "candles": p.Candles[start:end], "fills": fills}, nil
			}
		}
		return nil, fmt.Errorf("symbol not found")
	})
	add(server, "compare_backtests", "Compare completed experiments. Reports whether data hashes, dates, symbols and execution assumptions match.", func(ctx context.Context, in compareInput) (any, error) {
		if len(in.IDs) < 2 || len(in.IDs) > 10 {
			return nil, fmt.Errorf("2..10 run IDs required")
		}
		out := []map[string]any{}
		baseline := ""
		comparable := true
		for _, id := range in.IDs {
			var r core.Run
			if e := s.App.Store.Get("runs", id, &r); e != nil {
				return nil, e
			}
			if r.Status != "completed" {
				return nil, fmt.Errorf("run %s is not completed", id)
			}
			signature := fmt.Sprintf("%s|%v|%s|%s|%s|%g|%g|%g", r.Request.Source, r.Request.Symbols, r.Request.Interval, r.Request.From, r.Request.To, r.Request.Capital, r.Request.FeeBPS, r.Request.SlippageBPS)
			for _, p := range r.Pairs {
				signature += "|" + p.DataHash
			}
			if baseline == "" {
				baseline = signature
			} else {
				comparable = comparable && baseline == signature
			}
			out = append(out, map[string]any{"id": id, "strategy_id": r.Strategy.ID, "version": r.Strategy.Version, "metrics": r.Metrics, "request": r.Request})
		}
		return map[string]any{"same_inputs": comparable, "runs": out}, nil
	})
	add(server, "cancel_backtest", "Cancel a queued or running research job.", func(ctx context.Context, in idInput) (any, error) {
		e := s.App.CancelRun(in.ID)
		return map[string]bool{"cancel_requested": e == nil}, e
	})
	return server
}
func (s *Server) MCPHandler() http.Handler {
	server := s.MCPServer()
	return mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
}

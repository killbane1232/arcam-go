package httpapi

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"arcam/internal/app"
	"arcam/internal/core"
	"arcam/internal/llm"
	"arcam/internal/market"
)

//go:embed web/*
var assets embed.FS

type Server struct {
	App          *app.Service
	Token        string
	AllowedHosts []string
}
type saveStrategy struct {
	ID       string        `json:"id"`
	Strategy core.Strategy `json:"strategy"`
}

func (s *Server) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, map[string]any{"status": "ok"}) })
	m.HandleFunc("GET /api/catalog", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, s.Catalog()) })
	m.HandleFunc("GET /api/settings/llm", func(w http.ResponseWriter, r *http.Request) { v, e := s.App.LLM.Settings(); result(w, v, e) })
	m.HandleFunc("POST /api/settings/llm/{provider}", func(w http.ResponseWriter, r *http.Request) {
		var input llm.ConnectionUpdate
		if !decode(w, r, &input) {
			return
		}
		v, e := s.App.LLM.SaveSettings(r.PathValue("provider"), input)
		result(w, v, e)
	})
	m.HandleFunc("POST /api/settings/llm/{provider}/check", func(w http.ResponseWriter, r *http.Request) {
		var input llm.ConnectionUpdate
		if !decode(w, r, &input) {
			return
		}
		v, e := s.App.LLM.CheckConnection(r.Context(), r.PathValue("provider"), input)
		result(w, v, e)
	})
	m.HandleFunc("GET /api/storage", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.App.Store.Shards()
		result(w, map[string]any{"engine": "sqlite", "partition_by": "symbol", "directory": "candles", "shards": v}, e)
	})
	m.HandleFunc("GET /api/strategies", func(w http.ResponseWriter, r *http.Request) { v, e := s.App.Store.Strategies(); result(w, v, e) })
	m.HandleFunc("POST /api/strategies", func(w http.ResponseWriter, r *http.Request) {
		var v saveStrategy
		if !decode(w, r, &v) {
			return
		}
		out, e := s.App.Store.SaveVersion(v.ID, v.Strategy)
		result(w, out, e)
	})
	m.HandleFunc("POST /api/strategies/validate", func(w http.ResponseWriter, r *http.Request) {
		var v core.Strategy
		if !decode(w, r, &v) {
			return
		}
		if e := core.ValidateStrategy(v); e != nil {
			failure(w, 400, e)
			return
		}
		respond(w, 200, map[string]any{"valid": true, "fields": v.Fields(), "warmup": v.Warmup()})
	})
	m.HandleFunc("GET /api/datasets", func(w http.ResponseWriter, r *http.Request) { v, e := s.App.Store.Datasets(); result(w, v, e) })
	m.HandleFunc("GET /api/symbols", func(w http.ResponseWriter, r *http.Request) {
		source := s.App.Sources[r.URL.Query().Get("source")]
		if source == nil {
			failure(w, 400, fmt.Errorf("unknown source"))
			return
		}
		v, e := source.Symbols(r.Context())
		result(w, v, e)
	})
	m.HandleFunc("POST /api/import", func(w http.ResponseWriter, r *http.Request) {
		var v app.ImportRequest
		if !decode(w, r, &v) {
			return
		}
		out, e := s.App.Import(r.Context(), v)
		if e != nil {
			respond(w, 400, map[string]any{"error": e.Error(), "imported": out})
			return
		}
		respond(w, 200, out)
	})
	m.HandleFunc("POST /api/import/csv", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		source := q.Get("source")
		if source == "" {
			source = "csv"
		}
		if source == "demo" || s.App.Sources[source] != nil {
			failure(w, 400, fmt.Errorf("CSV must use a separate source name"))
			return
		}
		d, e := market.ReadCSV(http.MaxBytesReader(w, r.Body, 64<<20), source, q.Get("symbol"), q.Get("interval"))
		if e != nil {
			failure(w, 400, e)
			return
		}
		out, e := s.App.Store.Merge(d)
		result(w, out, e)
	})
	m.HandleFunc("GET /api/candles", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var from, to time.Time
		for key, target := range map[string]*time.Time{"from": &from, "to": &to} {
			if value := q.Get(key); value != "" {
				parsed, e := time.Parse(time.RFC3339, value)
				if e != nil {
					failure(w, 400, fmt.Errorf("%s must be RFC3339", key))
					return
				}
				*target = parsed
			}
		}
		d, e := s.App.Store.DatasetRange(r.Context(), q.Get("source"), q.Get("symbol"), q.Get("interval"), from, to)
		if e != nil {
			result(w, nil, e)
			return
		}
		if q.Get("format") == "csv" {
			w.Header().Set("Content-Type", "text/csv; charset=utf-8")
			w.Header().Set("Content-Disposition", "attachment; filename=candles.csv")
			_ = market.WriteCSV(w, d.Candles)
			return
		}
		respond(w, 200, d)
	})
	m.HandleFunc("GET /api/runs", func(w http.ResponseWriter, r *http.Request) { v, e := s.App.Runs(); result(w, v, e) })
	m.HandleFunc("POST /api/runs", func(w http.ResponseWriter, r *http.Request) {
		var v core.BacktestRequest
		if !decode(w, r, &v) {
			return
		}
		out, e := s.App.StartRun(v)
		out.InputData = nil
		if e != nil {
			failure(w, 400, e)
			return
		}
		respond(w, 202, out)
	})
	m.HandleFunc("GET /api/runs/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.App.Run(r.PathValue("id"))
		result(w, v, e)
	})
	m.HandleFunc("GET /api/runs/{id}/progress", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.App.RunSummary(r.PathValue("id"))
		result(w, v, e)
	})
	m.HandleFunc("GET /api/runs/{id}/updates", func(w http.ResponseWriter, r *http.Request) {
		after := map[string]app.PairCursor{}
		raw := r.URL.Query().Get("after")
		if len(raw) > 8192 {
			failure(w, 400, fmt.Errorf("cursor too large"))
			return
		}
		if raw != "" {
			if !json.Valid([]byte(raw)) {
				failure(w, 400, fmt.Errorf("invalid cursor JSON"))
				return
			}
			dec := json.NewDecoder(strings.NewReader(raw))
			dec.DisallowUnknownFields()
			if e := dec.Decode(&after); e != nil {
				failure(w, 400, fmt.Errorf("invalid cursor JSON"))
				return
			}
		}
		v, e := s.App.RunUpdates(r.PathValue("id"), after)
		result(w, v, e)
	})
	m.HandleFunc("POST /api/runs/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		e := s.App.CancelRun(r.PathValue("id"))
		result(w, map[string]bool{"cancel_requested": e == nil}, e)
	})
	m.HandleFunc("POST /api/runs/{id}/resume", func(w http.ResponseWriter, r *http.Request) {
		var input struct{}
		if !decode(w, r, &input) {
			return
		}
		v, e := s.App.ResumeRun(r.PathValue("id"))
		if e != nil {
			failure(w, 400, e)
			return
		}
		v.InputData, v.Pairs = nil, nil
		respond(w, 202, v)
	})
	m.HandleFunc("GET /api/sessions", func(w http.ResponseWriter, r *http.Request) { v, e := s.App.Sessions.List(); result(w, v, e) })
	m.HandleFunc("POST /api/sessions", func(w http.ResponseWriter, r *http.Request) {
		var v app.SessionRequest
		if !decode(w, r, &v) {
			return
		}
		out, e := s.App.Sessions.Start(r.Context(), v)
		result(w, out, e)
	})
	m.HandleFunc("GET /api/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		var v app.Session
		e := s.App.Store.Get("sessions", r.PathValue("id"), &v)
		result(w, v, e)
	})
	m.HandleFunc("POST /api/sessions/{id}/{action}", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.App.Sessions.Command(r.Context(), r.PathValue("id"), r.PathValue("action"))
		result(w, v, e)
	})
	m.Handle("/mcp", s.MCPHandler())
	sub, _ := fs.Sub(assets, "web")
	m.Handle("/", http.FileServer(http.FS(sub)))
	return s.guard(m)
}
func (s *Server) Catalog() map[string]any {
	return map[string]any{"name": "Arcam", "version": "0.1.0", "live_enabled": s.App.Sessions.LiveEnabled, "max_live_order_notional": s.App.Sessions.MaxLiveNotional, "sources": []string{"binance_spot", "binance_futures"}, "execution_exchanges": []string{"binance_futures"}, "intervals": []string{"1m", "3m", "5m", "15m", "30m", "1h", "2h", "4h", "6h", "8h", "12h", "1d"}, "indicators": []string{"ema", "sma", "rsi", "atr", "aroon", "ichimoku", "macd", "bollinger"}, "operators": []string{"gt", "gte", "lt", "lte", "eq", "crosses_above", "crosses_below"}, "rule_types": []string{"comparison", "all", "any", "not", "position", "signal"}, "position_states": []string{"none", "long", "short"}, "signal_conditions": []string{"enter_long", "enter_short"}, "history_auto_download": true, "position_example": core.PositionStrategy(), "example": core.DemoStrategy(), "llm_example": core.D1Strategy().LLM, "d1_example": core.D1Strategy(), "llm_protocols": []string{"chat", "systemone"}}
}
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'")
		host := r.Host
		if h, _, e := net.SplitHostPort(host); e == nil {
			host = h
		}
		allowed := host == "127.0.0.1" || host == "localhost" || host == "::1"
		for _, h := range s.AllowedHosts {
			allowed = allowed || host == h
		}
		if !allowed {
			failure(w, 403, fmt.Errorf("Host not allowed"))
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, e := url.Parse(origin)
			if e != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
				failure(w, 403, fmt.Errorf("cross-origin request denied"))
				return
			}
		}
		api := strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/mcp"
		if api {
			w.Header().Set("Cache-Control", "no-store")
			if s.Token != "" {
				token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				if subtle.ConstantTimeCompare([]byte(token), []byte(s.Token)) != 1 {
					failure(w, 401, fmt.Errorf("API token required"))
					return
				}
			}
		}
		if api && r.Method == "POST" && !strings.HasSuffix(r.URL.Path, "/csv") {
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				failure(w, 415, fmt.Errorf("Content-Type application/json required"))
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
		}
		next.ServeHTTP(w, r)
	})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if e := Decode(r.Body, v); e != nil {
		failure(w, 400, e)
		return false
	}
	return true
}
func Decode(r io.Reader, v any) error {
	d := json.NewDecoder(r)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return e
	}
	var trailing any
	if e := d.Decode(&trailing); e != io.EOF {
		return fmt.Errorf("expected a single JSON object")
	}
	return nil
}
func respond(w http.ResponseWriter, status int, v any) {
	b, e := json.Marshal(v)
	if e != nil {
		failure(w, 500, e)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}
func failure(w http.ResponseWriter, status int, e error) {
	respond(w, status, map[string]string{"error": e.Error()})
}
func result(w http.ResponseWriter, v any, e error) {
	if e != nil {
		status := 400
		if errors.Is(e, os.ErrNotExist) {
			status = 404
		}
		failure(w, status, e)
		return
	}
	respond(w, 200, v)
}

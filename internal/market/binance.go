package market

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"arcam/internal/core"
	"arcam/internal/store"
)

// DataSource is independent of execution: additional exchanges implement this interface.
type DataSource interface {
	Candles(context.Context, string, string, time.Time, time.Time) ([]core.Candle, error)
	Symbols(context.Context) ([]string, error)
}
type Binance struct {
	BaseURL string
	Prefix  string
	Client  *http.Client
}

func NewBinance(futures bool) *Binance {
	b := &Binance{BaseURL: "https://data-api.binance.vision", Prefix: "/api/v3", Client: &http.Client{Timeout: 20 * time.Second}}
	if futures {
		b.BaseURL = "https://fapi.binance.com"
		b.Prefix = "/fapi/v1"
	}
	return b
}
func (b *Binance) get(ctx context.Context, path string, q url.Values, out any) error {
	for attempt := 0; attempt < 4; attempt++ {
		req, e := http.NewRequestWithContext(ctx, http.MethodGet, b.BaseURL+b.Prefix+path+"?"+q.Encode(), nil)
		if e != nil {
			return e
		}
		res, e := b.Client.Do(req)
		if e != nil {
			return e
		}
		if res.StatusCode == 429 || res.StatusCode >= 500 {
			res.Body.Close()
			if attempt == 3 {
				return fmt.Errorf("market data HTTP %d after retries", res.StatusCode)
			}
			wait := time.Duration(1<<attempt) * time.Second
			if n, _ := strconv.Atoi(res.Header.Get("Retry-After")); n > 0 {
				wait = time.Duration(n) * time.Second
			}
			if wait > 30*time.Second {
				return fmt.Errorf("market data rate limit: retry later")
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
			continue
		}
		if res.StatusCode != 200 {
			res.Body.Close()
			return fmt.Errorf("market data HTTP %d", res.StatusCode)
		}
		e = json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(out)
		res.Body.Close()
		return e
	}
	return fmt.Errorf("market data unavailable")
}
func (b *Binance) Candles(ctx context.Context, symbol, interval string, from, to time.Time) ([]core.Candle, error) {
	step, e := core.Interval(interval)
	if e != nil {
		return nil, e
	}
	if !core.SymbolPattern.MatchString(symbol) || !to.After(from) || to.Sub(from)/step > 50000 {
		return nil, fmt.Errorf("invalid import range/symbol (max 50000 candles)")
	}
	if to.After(time.Now().UTC().Truncate(step)) {
		return nil, fmt.Errorf("only closed candles can be imported")
	}
	out := []core.Candle{}
	cursor := from.UnixMilli()
	for cursor < to.UnixMilli() {
		var rows [][]json.RawMessage
		e = b.get(ctx, "/klines", url.Values{"symbol": {symbol}, "interval": {interval}, "startTime": {strconv.FormatInt(cursor, 10)}, "endTime": {strconv.FormatInt(to.UnixMilli()-1, 10)}, "limit": {"1000"}}, &rows)
		if e != nil {
			return nil, e
		}
		if len(rows) == 0 {
			break
		}
		next := cursor
		for _, row := range rows {
			if len(row) < 7 {
				return nil, fmt.Errorf("invalid kline response")
			}
			var ts int64
			if e = json.Unmarshal(row[0], &ts); e != nil {
				return nil, e
			}
			v := make([]float64, 5)
			for i := 1; i <= 5; i++ {
				var str string
				if e = json.Unmarshal(row[i], &str); e != nil {
					return nil, e
				}
				v[i-1], e = strconv.ParseFloat(str, 64)
				if e != nil {
					return nil, e
				}
			}
			if ts < cursor || ts >= to.UnixMilli() {
				return nil, fmt.Errorf("out-of-range kline")
			}
			out = append(out, core.Candle{Time: time.UnixMilli(ts).UTC(), Open: v[0], High: v[1], Low: v[2], Close: v[3], Volume: v[4]})
			next = ts + step.Milliseconds()
		}
		if next <= cursor {
			return nil, fmt.Errorf("market pagination did not advance")
		}
		cursor = next
		if len(out) > 50000 {
			return nil, fmt.Errorf("import candle limit exceeded")
		}
		if cursor < to.UnixMilli() {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(150 * time.Millisecond):
			}
		}
	}
	if e = core.ValidateCandles(out, interval); e != nil {
		return nil, e
	}
	return out, nil
}
func (b *Binance) Symbols(ctx context.Context) ([]string, error) {
	var data struct {
		Symbols []struct {
			Symbol       string `json:"symbol"`
			Status       string `json:"status"`
			ContractType string `json:"contractType"`
		} `json:"symbols"`
	}
	if e := b.get(ctx, "/exchangeInfo", nil, &data); e != nil {
		return nil, e
	}
	out := []string{}
	for _, s := range data.Symbols {
		if s.Status == "TRADING" && (s.ContractType == "" || s.ContractType == "PERPETUAL") {
			out = append(out, s.Symbol)
		}
	}
	sort.Strings(out)
	return out, nil
}
func ReadCSV(r io.Reader, source, symbol, interval string) (store.Dataset, error) {
	d := store.Dataset{Source: source, Symbol: symbol, Interval: interval}
	reader := csv.NewReader(r)
	header, e := reader.Read()
	if e != nil {
		return d, e
	}
	if strings.Join(header, ",") != "time,open,high,low,close,volume" {
		return d, fmt.Errorf("CSV header must be time,open,high,low,close,volume")
	}
	for line := 2; ; line++ {
		row, e := reader.Read()
		if e == io.EOF {
			break
		}
		if e != nil {
			return d, e
		}
		if len(row) != 6 {
			return d, fmt.Errorf("line %d: expected six columns", line)
		}
		t, e := time.Parse(time.RFC3339, row[0])
		if e != nil {
			return d, fmt.Errorf("line %d: RFC3339 time required", line)
		}
		values := make([]float64, 5)
		for i := range values {
			values[i], e = strconv.ParseFloat(row[i+1], 64)
			if e != nil {
				return d, fmt.Errorf("line %d: invalid number", line)
			}
		}
		d.Candles = append(d.Candles, core.Candle{Time: t.UTC(), Open: values[0], High: values[1], Low: values[2], Close: values[3], Volume: values[4]})
		if len(d.Candles) > 250000 {
			return d, fmt.Errorf("too many candles")
		}
	}
	return d, core.ValidateCandles(d.Candles, interval)
}
func WriteCSV(w io.Writer, c []core.Candle) error {
	out := csv.NewWriter(w)
	if e := out.Write([]string{"time", "open", "high", "low", "close", "volume"}); e != nil {
		return e
	}
	for _, v := range c {
		row := []string{v.Time.UTC().Format(time.RFC3339)}
		for _, x := range []float64{v.Open, v.High, v.Low, v.Close, v.Volume} {
			row = append(row, strconv.FormatFloat(x, 'g', -1, 64))
		}
		if e := out.Write(row); e != nil {
			return e
		}
	}
	out.Flush()
	return out.Error()
}

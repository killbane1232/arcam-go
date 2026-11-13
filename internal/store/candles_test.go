package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"arcam/internal/core"
)

func TestSQLiteShardsRangeAndIsolation(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	bars := core.DemoCandles(100)
	for _, symbol := range []string{"BTCUSDT", "ETHUSDT"} {
		for _, source := range []string{"binance_spot", "binance_futures"} {
			for _, interval := range []string{"1h", "30m"} {
				if _, err = s.Merge(Dataset{Source: source, Symbol: symbol, Interval: interval, Candles: bars[:10]}); err != nil {
					t.Fatal(err)
				}
			}
		}
		data, err := os.ReadFile(filepath.Join(dir, "candles", symbol+".sqlite"))
		if err != nil || !strings.HasPrefix(string(data), "SQLite format 3") {
			t.Fatalf("not SQLite: %v", err)
		}
	}
	corrected := bars[4]
	corrected.Volume = 123456
	if _, err = s.Merge(Dataset{Source: "binance_spot", Symbol: "BTCUSDT", Interval: "1h", Candles: []core.Candle{corrected}}); err != nil {
		t.Fatal(err)
	}
	d, err := s.DatasetRange(context.Background(), "binance_spot", "BTCUSDT", "1h", bars[4].Time, bars[6].Time)
	if err != nil || len(d.Candles) != 2 || d.Candles[0].Volume != corrected.Volume || !d.Candles[1].Time.Equal(bars[5].Time) {
		t.Fatalf("range/upsert: %+v %v", d, err)
	}
	untouched, err := s.Dataset("binance_futures", "BTCUSDT", "1h")
	if err != nil || untouched.Candles[4].Volume != bars[4].Volume {
		t.Fatalf("source isolation: %v", err)
	}
	untouched, err = s.Dataset("binance_spot", "ETHUSDT", "1h")
	if err != nil || untouched.Candles[4].Volume != bars[4].Volume {
		t.Fatalf("pair isolation: %v", err)
	}
	untouched, err = s.Dataset("binance_spot", "BTCUSDT", "30m")
	if err != nil || untouched.Candles[4].Volume != bars[4].Volume {
		t.Fatalf("interval isolation: %v", err)
	}
	shards, err := s.Shards()
	if err != nil || len(shards) != 2 || shards[0].Datasets != 4 || shards[0].Candles != 40 {
		t.Fatalf("metadata: %+v %v", shards, err)
	}
	empty, err := s.DatasetRange(context.Background(), "binance_spot", "BTCUSDT", "1h", bars[40].Time, bars[50].Time)
	if err != nil || len(empty.Candles) != 0 {
		t.Fatalf("empty range: %v", err)
	}
	if _, err = s.Dataset("csv", "SOLUSDT", "1h"); !os.IsNotExist(err) {
		t.Fatalf("missing dataset: %v", err)
	}
	if _, err = s.Merge(Dataset{Source: "csv", Symbol: "../escape", Interval: "1h", Candles: bars}); err == nil {
		t.Fatal("path traversal accepted")
	}
}

func TestSQLiteMigrationReopenAndNoOverwrite(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "datasets")
	if err := os.MkdirAll(legacy, 0700); err != nil {
		t.Fatal(err)
	}
	d := Dataset{Source: "demo", Symbol: "BTCUSDT", Interval: "1h", Candles: core.DemoCandles(100)[:10]}
	original, _ := json.Marshal(d)
	path := filepath.Join(legacy, DatasetKey(d.Source, d.Symbol, d.Interval)+".json")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path + ".migrated"); err != nil {
		t.Fatal("backup missing", err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("legacy file still active")
	}
	d.Candles[0].Volume = 99999
	if _, err = s.Merge(d); err != nil {
		t.Fatal(err)
	}
	s.Close()
	// Simulate a crash after SQL commit but before renaming the old JSON.
	if err = os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	actual, err := s.Dataset(d.Source, d.Symbol, d.Interval)
	if err != nil || len(actual.Candles) != 10 || actual.Candles[0].Volume != 99999 {
		t.Fatalf("reimport overwrote newer data: %v", err)
	}
	fi, err := os.Stat(filepath.Join(dir, "candles", "BTCUSDT.sqlite"))
	if err != nil || fi.Mode().Perm() != 0600 {
		t.Fatal("wrong DB permissions", err)
	}
}

func TestSQLiteConcurrentImportsAndRollback(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	bars := core.DemoCandles(100)
	var wg sync.WaitGroup
	for _, symbol := range []string{"BTCUSDT", "ETHUSDT"} {
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(symbol string, i int) {
				defer wg.Done()
				_, err := s.Merge(Dataset{Source: "csv", Symbol: symbol, Interval: "1h", Candles: bars[i*5 : i*5+10]})
				if err != nil {
					t.Error(err)
				}
			}(symbol, i)
		}
	}
	wg.Wait()
	for _, symbol := range []string{"BTCUSDT", "ETHUSDT"} {
		d, err := s.Dataset("csv", symbol, "1h")
		if err != nil || len(d.Candles) != 55 {
			t.Fatalf("lost concurrent writes: %d %v", len(d.Candles), err)
		}
	}
	// Force a database error after the first insert to prove transaction rollback.
	db, err := s.shard("BTCUSDT", false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("CREATE TRIGGER reject_test BEFORE INSERT ON candles WHEN NEW.open_time=" + fmtTime(bars[61].Time) + " BEGIN SELECT RAISE(ABORT, 'test rejection'); END")
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Merge(Dataset{Source: "csv", Symbol: "BTCUSDT", Interval: "1h", Candles: bars[60:62]})
	if err == nil {
		t.Fatal("database failure not propagated")
	}
	d, err := s.Dataset("csv", "BTCUSDT", "1h")
	if err != nil || len(d.Candles) != 55 {
		t.Fatalf("partial transaction persisted: %v", err)
	}
}
func fmtTime(v time.Time) string { b, _ := json.Marshal(v.UnixMilli()); return string(b) }

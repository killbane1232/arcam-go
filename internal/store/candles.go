package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"arcam/internal/core"
	_ "modernc.org/sqlite"
)

const candleSchema = `
CREATE TABLE IF NOT EXISTS datasets (
 source TEXT NOT NULL, timeframe TEXT NOT NULL, updated_at INTEGER NOT NULL,
 candle_count INTEGER NOT NULL, first_time INTEGER NOT NULL, last_time INTEGER NOT NULL,
 PRIMARY KEY (source, timeframe)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS candles (
 source TEXT NOT NULL, timeframe TEXT NOT NULL, open_time INTEGER NOT NULL,
 price_open REAL NOT NULL, price_high REAL NOT NULL, price_low REAL NOT NULL,
 price_close REAL NOT NULL, volume REAL NOT NULL,
 PRIMARY KEY (source, timeframe, open_time),
 CHECK (price_low > 0 AND price_high >= price_low AND price_open BETWEEN price_low AND price_high
 AND price_close BETWEEN price_low AND price_high AND volume >= 0)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS legacy_imports (id TEXT PRIMARY KEY, imported_at INTEGER NOT NULL) WITHOUT ROWID;
PRAGMA user_version = 1;
`

// A physical SQLite database per trading pair. Connections live only for the
// operation, so an unbounded symbol catalogue cannot exhaust file descriptors.
func (s *Store) shard(symbol string, create bool) (*sql.DB, error) {
	if !core.SymbolPattern.MatchString(symbol) {
		return nil, fmt.Errorf("invalid symbol")
	}
	s.shardInit.Lock()
	defer s.shardInit.Unlock()
	path, err := filepath.Abs(filepath.Join(s.root, "candles", symbol+".sqlite"))
	if err != nil {
		return nil, err
	}
	if create {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, err
		}
		if err = f.Close(); err != nil {
			return nil, err
		}
	} else if _, err = os.Stat(path); err != nil {
		return nil, err
	}
	if err = os.Chmod(path, 0600); err != nil {
		return nil, err
	}
	q := url.Values{"_pragma": {"busy_timeout(5000)", "journal_mode(WAL)", "synchronous(FULL)"}, "_txlock": {"immediate"}, "mode": {"rw"}}
	dsn := url.URL{Scheme: "file", Path: path, RawQuery: q.Encode()}
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	fail := func(e error) (*sql.DB, error) { db.Close(); return nil, e }
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fail(err)
	}
	if version > 1 {
		return fail(fmt.Errorf("%s: unsupported candle schema version %d", symbol, version))
	}
	if version == 0 {
		if !create {
			return fail(fmt.Errorf("%s: uninitialized candle database", symbol))
		}
		tx, err := db.Begin()
		if err != nil {
			return fail(err)
		}
		if _, err = tx.Exec(candleSchema); err != nil {
			tx.Rollback()
			return fail(err)
		}
		if err = tx.Commit(); err != nil {
			return fail(err)
		}
	}
	return db, nil
}

func validDataset(source, symbol, interval string) error {
	if !core.IDPattern.MatchString(source) || !core.SymbolPattern.MatchString(symbol) {
		return fmt.Errorf("invalid source/symbol")
	}
	_, err := core.Interval(interval)
	return err
}
func (s *Store) Dataset(source, symbol, interval string) (Dataset, error) {
	return s.DatasetRange(context.Background(), source, symbol, interval, time.Time{}, time.Time{})
}

// DatasetRange reads only the requested [from,to) range using the primary key.
// Zero bounds mean an unbounded export; metadata still refers to the whole set.
func (s *Store) DatasetRange(ctx context.Context, source, symbol, interval string, from, to time.Time) (Dataset, error) {
	d := Dataset{Source: source, Symbol: symbol, Interval: interval, Candles: []core.Candle{}}
	if err := validDataset(source, symbol, interval); err != nil {
		return d, err
	}
	if !from.IsZero() && !to.IsZero() && !to.After(from) {
		return d, fmt.Errorf("to must be after from")
	}
	db, err := s.shard(symbol, false)
	if err != nil {
		return d, err
	}
	defer db.Close()
	var updated int64
	err = db.QueryRowContext(ctx, "SELECT updated_at FROM datasets WHERE source=? AND timeframe=?", source, interval).Scan(&updated)
	if errors.Is(err, sql.ErrNoRows) {
		return d, os.ErrNotExist
	}
	if err != nil {
		return d, err
	}
	d.UpdatedAt = time.UnixMilli(updated).UTC()
	query := "SELECT open_time,price_open,price_high,price_low,price_close,volume FROM candles WHERE source=? AND timeframe=?"
	args := []any{source, interval}
	if !from.IsZero() {
		query += " AND open_time>=?"
		args = append(args, from.UnixMilli())
	}
	if !to.IsZero() {
		query += " AND open_time<?"
		args = append(args, to.UnixMilli())
	}
	query += " ORDER BY open_time"
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return d, err
	}
	defer rows.Close()
	for rows.Next() {
		var c core.Candle
		var ts int64
		if err = rows.Scan(&ts, &c.Open, &c.High, &c.Low, &c.Close, &c.Volume); err != nil {
			return d, err
		}
		c.Time = time.UnixMilli(ts).UTC()
		d.Candles = append(d.Candles, c)
	}
	return d, rows.Err()
}
func (s *Store) Merge(d Dataset) (DatasetInfo, error) { return s.mergeCandles(d, "") }
func (s *Store) mergeCandles(d Dataset, legacyID string) (DatasetInfo, error) {
	if err := validDataset(d.Source, d.Symbol, d.Interval); err != nil {
		return DatasetInfo{}, err
	}
	if err := core.ValidateCandles(d.Candles, d.Interval); err != nil {
		return DatasetInfo{}, err
	}
	db, err := s.shard(d.Symbol, true)
	if err != nil {
		return DatasetInfo{}, err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return DatasetInfo{}, err
	}
	defer tx.Rollback()
	if legacyID != "" {
		var exists int
		err = tx.QueryRow("SELECT 1 FROM legacy_imports WHERE id=?", legacyID).Scan(&exists)
		if err == nil {
			return datasetInfo(tx, d.Source, d.Symbol, d.Interval)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return DatasetInfo{}, err
		}
	}
	conflict := "DO UPDATE SET price_open=excluded.price_open,price_high=excluded.price_high,price_low=excluded.price_low,price_close=excluded.price_close,volume=excluded.volume"
	// Backups must never overwrite more recent data already in SQLite.
	if legacyID != "" {
		conflict = "DO NOTHING"
	}
	stmt, err := tx.Prepare("INSERT INTO candles(source,timeframe,open_time,price_open,price_high,price_low,price_close,volume) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(source,timeframe,open_time) " + conflict)
	if err != nil {
		return DatasetInfo{}, err
	}
	defer stmt.Close()
	for _, c := range d.Candles {
		if _, err = stmt.Exec(d.Source, d.Interval, c.Time.UnixMilli(), c.Open, c.High, c.Low, c.Close, c.Volume); err != nil {
			return DatasetInfo{}, err
		}
	}
	var count int
	var first, last int64
	if err = tx.QueryRow("SELECT COUNT(*),MIN(open_time),MAX(open_time) FROM candles WHERE source=? AND timeframe=?", d.Source, d.Interval).Scan(&count, &first, &last); err != nil {
		return DatasetInfo{}, err
	}
	if count > 250000 {
		return DatasetInfo{}, fmt.Errorf("dataset limit 250000 candles exceeded")
	}
	_, err = tx.Exec(`INSERT INTO datasets(source,timeframe,updated_at,candle_count,first_time,last_time) VALUES(?,?,?,?,?,?)
 ON CONFLICT(source,timeframe) DO UPDATE SET updated_at=excluded.updated_at,candle_count=excluded.candle_count,first_time=excluded.first_time,last_time=excluded.last_time`, d.Source, d.Interval, time.Now().UTC().UnixMilli(), count, first, last)
	if err != nil {
		return DatasetInfo{}, err
	}
	if legacyID != "" {
		if _, err = tx.Exec("INSERT INTO legacy_imports(id,imported_at) VALUES(?,?)", legacyID, time.Now().UTC().UnixMilli()); err != nil {
			return DatasetInfo{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return DatasetInfo{}, err
	}
	step, _ := core.Interval(d.Interval)
	return DatasetInfo{Source: d.Source, Symbol: d.Symbol, Interval: d.Interval, Count: count, From: time.UnixMilli(first).UTC(), To: time.UnixMilli(last).UTC().Add(step)}, nil
}
func datasetInfo(tx *sql.Tx, source, symbol, interval string) (DatasetInfo, error) {
	info := DatasetInfo{Source: source, Symbol: symbol, Interval: interval}
	var first, last int64
	err := tx.QueryRow("SELECT candle_count,first_time,last_time FROM datasets WHERE source=? AND timeframe=?", source, interval).Scan(&info.Count, &first, &last)
	step, _ := core.Interval(interval)
	info.From = time.UnixMilli(first).UTC()
	info.To = time.UnixMilli(last).UTC().Add(step)
	return info, err
}
func (s *Store) shardSymbols() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, "candles"))
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sqlite") {
			continue
		}
		symbol := strings.TrimSuffix(e.Name(), ".sqlite")
		if core.SymbolPattern.MatchString(symbol) {
			out = append(out, symbol)
		}
	}
	return out, nil
}
func (s *Store) Datasets() ([]DatasetInfo, error) {
	symbols, err := s.shardSymbols()
	if err != nil {
		return nil, err
	}
	out := []DatasetInfo{}
	for _, symbol := range symbols {
		items, err := s.shardDatasets(symbol)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	return out, nil
}
func (s *Store) shardDatasets(symbol string) ([]DatasetInfo, error) {
	db, err := s.shard(symbol, false)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query("SELECT source,timeframe,candle_count,first_time,last_time FROM datasets ORDER BY source,timeframe")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DatasetInfo{}
	for rows.Next() {
		info := DatasetInfo{Symbol: symbol}
		var first, last int64
		if err = rows.Scan(&info.Source, &info.Interval, &info.Count, &first, &last); err != nil {
			return nil, err
		}
		step, err := core.Interval(info.Interval)
		if err != nil {
			return nil, err
		}
		info.From = time.UnixMilli(first).UTC()
		info.To = time.UnixMilli(last).UTC().Add(step)
		out = append(out, info)
	}
	return out, rows.Err()
}

type ShardInfo struct {
	Symbol   string `json:"symbol"`
	File     string `json:"file"`
	Bytes    int64  `json:"bytes"`
	Datasets int    `json:"datasets"`
	Candles  int    `json:"candles"`
}

func (s *Store) Shards() ([]ShardInfo, error) {
	symbols, err := s.shardSymbols()
	if err != nil {
		return nil, err
	}
	out := []ShardInfo{}
	for _, symbol := range symbols {
		file := symbol + ".sqlite"
		info, err := os.Stat(filepath.Join(s.root, "candles", file))
		if err != nil {
			return nil, err
		}
		sets, err := s.shardDatasets(symbol)
		if err != nil {
			return nil, err
		}
		item := ShardInfo{Symbol: symbol, File: file, Bytes: info.Size(), Datasets: len(sets)}
		for _, d := range sets {
			item.Candles += d.Count
		}
		out = append(out, item)
	}
	return out, nil
}
func (s *Store) migrateCandles() error {
	entries, err := os.ReadDir(filepath.Join(s.root, "datasets"))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(s.root, "datasets", entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var d Dataset
		if err = json.Unmarshal(data, &d); err != nil {
			return fmt.Errorf("migrate %s: %w", entry.Name(), err)
		}
		if entry.Name() != DatasetKey(d.Source, d.Symbol, d.Interval)+".json" {
			return fmt.Errorf("legacy dataset filename mismatch: %s", entry.Name())
		}
		if _, err = s.mergeCandles(d, entry.Name()); err != nil {
			return fmt.Errorf("migrate %s: %w", entry.Name(), err)
		}
		// Retain the original bytes, and do not re-read backups on subsequent boots.
		backup := path + ".migrated"
		if _, err = os.Stat(backup); os.IsNotExist(err) {
			if err = os.Rename(path, backup); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
	}
	return nil
}

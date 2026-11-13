package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"

	"arcam/internal/core"
)

type Store struct {
	root      string
	mu        sync.RWMutex
	lock      *os.File
	shardInit sync.Mutex
}

func Open(root string) (*Store, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(root, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("data directory already in use: %w", err)
	}
	s := &Store{root: root, lock: f}
	for _, d := range []string{"strategies", "datasets", "runs", "llm", "sessions", "settings", "candles"} {
		if err = os.MkdirAll(filepath.Join(root, d), 0700); err != nil {
			s.Close()
			return nil, err
		}
	}
	if err = s.migrateCandles(); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) Close() error {
	syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
	return s.lock.Close()
}
func Hash(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Clone prevents callers from mutating maps/slices owned by background workers.
func Clone[T any](v T) (T, error) {
	var out T
	b, err := json.Marshal(v)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(b, &out)
	return out, err
}
func (s *Store) path(kind, id string) (string, error) {
	switch kind {
	case "strategies", "runs", "llm", "sessions", "settings":
	default:
		return "", fmt.Errorf("invalid collection")
	}
	if !core.IDPattern.MatchString(id) {
		return "", fmt.Errorf("invalid id")
	}
	return filepath.Join(s.root, kind, id+".json"), nil
}
func (s *Store) Get(kind, id string, v any) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, e := s.path(kind, id)
	if e != nil {
		return e
	}
	b, e := os.ReadFile(p)
	if e != nil {
		return e
	}
	return json.Unmarshal(b, v)
}
func (s *Store) Put(kind, id string, v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, e := s.path(kind, id)
	if e != nil {
		return e
	}
	return atomic(p, v)
}
func atomic(path string, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(b); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = os.Rename(f.Name(), path); e != nil {
		return e
	}
	d, e := os.Open(filepath.Dir(path))
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
func (s *Store) IDs(kind string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, e := s.path(kind, "index")
	if e != nil {
		return nil, e
	}
	entries, e := os.ReadDir(filepath.Dir(p))
	if e != nil {
		return nil, e
	}
	ids := []string{}
	for _, v := range entries {
		if filepath.Ext(v.Name()) == ".json" {
			ids = append(ids, v.Name()[:len(v.Name())-5])
		}
	}
	sort.Strings(ids)
	return ids, nil
}
func (s *Store) SaveVersion(id string, strategy core.Strategy) (core.Version, error) {
	if e := core.ValidateStrategy(strategy); e != nil {
		return core.Version{}, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, e := s.path("strategies", id)
	if e != nil {
		return core.Version{}, e
	}
	versions := []core.Version{}
	b, e := os.ReadFile(p)
	if e == nil {
		if e = json.Unmarshal(b, &versions); e != nil {
			return core.Version{}, e
		}
	} else if !os.IsNotExist(e) {
		return core.Version{}, e
	}
	v := core.Version{ID: id, Version: len(versions) + 1, CreatedAt: time.Now().UTC(), Hash: Hash(strategy), Strategy: strategy}
	versions = append(versions, v)
	return v, atomic(p, versions)
}
func (s *Store) Version(id string, n int) (core.Version, error) {
	var versions []core.Version
	if e := s.Get("strategies", id, &versions); e != nil {
		return core.Version{}, e
	}
	if n < 1 || n > len(versions) {
		return core.Version{}, os.ErrNotExist
	}
	return versions[n-1], nil
}
func (s *Store) Strategies() ([]core.Version, error) {
	ids, e := s.IDs("strategies")
	if e != nil {
		return nil, e
	}
	out := []core.Version{}
	for _, id := range ids {
		var versions []core.Version
		if e = s.Get("strategies", id, &versions); e != nil {
			return nil, e
		}
		out = append(out, versions...)
	}
	return out, nil
}

type Dataset struct {
	Source    string        `json:"source"`
	Symbol    string        `json:"symbol"`
	Interval  string        `json:"interval"`
	UpdatedAt time.Time     `json:"updated_at"`
	Candles   []core.Candle `json:"candles"`
}
type DatasetInfo struct {
	Source   string    `json:"source"`
	Symbol   string    `json:"symbol"`
	Interval string    `json:"interval"`
	Count    int       `json:"count"`
	From     time.Time `json:"from"`
	To       time.Time `json:"to"`
}

func DatasetKey(source, symbol, interval string) string {
	return Hash([]string{source, symbol, interval})
}
func Info(d Dataset) DatasetInfo {
	v := DatasetInfo{Source: d.Source, Symbol: d.Symbol, Interval: d.Interval, Count: len(d.Candles)}
	if len(d.Candles) > 0 {
		v.From = d.Candles[0].Time
		step, _ := core.Interval(d.Interval)
		v.To = d.Candles[len(d.Candles)-1].Time.Add(step)
	}
	return v
}

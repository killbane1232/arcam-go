package store

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRecoverStatusPreservesCheckpointBytes(t *testing.T) {
	for _, kind := range []string{"runs", "sessions"} {
		for _, source := range []string{
			`{"status":"running","error":"old","input_data":{"BTCUSDT":[{"volume":9007199254740993}]},"pending":{"plan":{"stop_price":90.1234567890123456789}},"future_field":{"status":"running","text":"brace } and quote \""}}`,
			"{\n \"pending\":{\"plan\": {\"stop_price\": 9e+1}}, \"error\" : \"old\", \"status\": \"running\" \n}",
			`{"status":"running","unknown":1e1000}`,
			`{"sta\u0074us":"running","error":null}`,
		} {
			t.Run(kind+"/"+source[:min(25, len(source))], func(t *testing.T) {
				st, err := Open(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				defer st.Close()
				path := filepath.Join(st.root, kind, "test.json")
				if err = os.WriteFile(path, []byte(source), 0600); err != nil {
					t.Fatal(err)
				}
				updated, err := st.RecoverStatus(kind, "test", []string{"running"}, "paused", "restart\nrequires \"reconcile\"")
				if err != nil || !updated {
					t.Fatal("recovery failed", updated, err)
				}
				got, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var before, after map[string]json.RawMessage
				if err = json.Unmarshal([]byte(source), &before); err != nil {
					t.Fatal(err)
				}
				if err = json.Unmarshal(got, &after); err != nil {
					t.Fatalf("invalid replacement: %s: %v", got, err)
				}
				if string(after["status"]) != `"paused"` || string(after["error"]) != `"restart\nrequires \"reconcile\""` {
					t.Fatalf("metadata not updated: %s", got)
				}
				for _, key := range []string{"status", "error"} {
					delete(before, key)
					delete(after, key)
				}
				if !reflect.DeepEqual(before, after) {
					t.Fatal("checkpoint numbers, unknown fields or payload bytes changed")
				}
				updated, err = st.RecoverStatus(kind, "test", []string{"running"}, "paused", "another restart")
				again, readErr := os.ReadFile(path)
				if err != nil || readErr != nil || updated || !bytes.Equal(got, again) {
					t.Fatal("second restart rewrote an inactive record", err, readErr)
				}
			})
		}
	}
}

func TestRecoverStatusDoesNotReplaceInvalidActiveRecord(t *testing.T) {
	for _, source := range []string{
		`{"status":"running","input_data":[1,`,
		`{"status":"running","input_data":1} {}`,
		`{"status":"running","status":"paused"}`,
		`{"status":"running","error":"a","error":"b"}`,
		`{"status":123}`,
		`{"input_data":{}}`,
		`[]`,
	} {
		t.Run(source, func(t *testing.T) {
			st, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			path := filepath.Join(st.root, "runs", "test.json")
			if err = os.WriteFile(path, []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			updated, err := st.RecoverStatus("runs", "test", []string{"running"}, "interrupted", "restart")
			got, readErr := os.ReadFile(path)
			if err == nil || !strings.Contains(err.Error(), "runs/test") || updated || readErr != nil || string(got) != source {
				t.Fatal("invalid checkpoint was replaced or lacks file context", err, readErr)
			}
		})
	}
}

type recoveryReader struct {
	io.Reader
	read, maxBuffer int
}

func (r *recoveryReader) Read(p []byte) (int, error) {
	r.maxBuffer = max(r.maxBuffer, len(p))
	n, err := r.Reader.Read(p)
	r.read += n
	return n, err
}

func TestInactiveRecoveryDoesNotReadReportBody(t *testing.T) {
	r := &recoveryReader{Reader: io.MultiReader(
		strings.NewReader(`{"status":"completed","pairs":[`),
		strings.NewReader(strings.Repeat(`{"features":{"close":100}},`, 100000)),
		strings.NewReader(`{}]}`),
	)}
	edits, err := recoveryEdits(r, []string{"running", "queued", "preparing"}, "interrupted", "restart")
	if err != nil || len(edits) != 0 || r.read > 4096 {
		t.Fatalf("inactive report body was loaded: read=%d, edits=%d, err=%v", r.read, len(edits), err)
	}
}

func TestActiveRecoveryStreamsPayloadBeforeStatus(t *testing.T) {
	// Older sessions place input_data before status. The decoder must keep a
	// small read buffer instead of buffering the full object even in that layout.
	r := &recoveryReader{Reader: io.MultiReader(
		strings.NewReader(`{"input_data":[`),
		strings.NewReader(strings.Repeat(`{"close":100,"volume":9007199254740993},`, 100000)),
		strings.NewReader(`{}],"status":"running"}`),
	)}
	edits, err := recoveryEdits(r, []string{"running"}, "paused", "restart")
	if err != nil || len(edits) != 2 || r.maxBuffer > 64<<10 || r.read < 1<<20 {
		t.Fatalf("recovery buffered the document: largest read=%d, bytes=%d, edits=%d, err=%v", r.maxBuffer, r.read, len(edits), err)
	}
}

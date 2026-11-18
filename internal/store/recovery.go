package store

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
)

type jsonEdit struct {
	start, end int64
	value      []byte
}

// RecoverStatus updates only lifecycle metadata after a restart. Completed
// archives are not decoded into candle/decision graphs. Active records are
// streamed into an atomic replacement, retaining all other bytes, including
// unknown fields and exact numbers in checkpoints and pending order plans.
func (s *Store) RecoverStatus(kind, id string, from []string, status, reason string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.path(kind, id)
	if err != nil {
		return false, err
	}
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	edits, err := recoveryEdits(f, from, status, reason)
	if err != nil {
		return false, fmt.Errorf("recover %s/%s: %w", kind, id, err)
	}
	if len(edits) == 0 {
		return false, nil
	}
	info, err := f.Stat()
	if err != nil {
		return false, err
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
	err = atomicWrite(path, func(w io.Writer) error {
		var offset int64
		for _, edit := range edits {
			if _, e := io.Copy(w, io.NewSectionReader(f, offset, edit.start-offset)); e != nil {
				return e
			}
			if _, e := w.Write(edit.value); e != nil {
				return e
			}
			offset = edit.end
		}
		_, e := io.Copy(w, io.NewSectionReader(f, offset, info.Size()-offset))
		return e
	})
	return err == nil, err
}

func recoveryEdits(r io.Reader, from []string, status, reason string) ([]jsonEdit, error) {
	dec := json.NewDecoder(r)
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if tok != json.Delim('{') {
		return nil, fmt.Errorf("expected record object")
	}
	edits := []jsonEdit{}
	seenStatus, seenError := false, false
	for dec.More() {
		tok, err = dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, fmt.Errorf("expected record field")
		}
		// InputOffset is immediately after the field name, before its colon.
		start := dec.InputOffset()
		value := ""
		switch key {
		case "status":
			if seenStatus {
				return nil, fmt.Errorf("duplicate status field")
			}
			seenStatus = true
			var current string
			if err = dec.Decode(&current); err != nil {
				return nil, err
			}
			active := false
			for _, candidate := range from {
				active = active || current == candidate
			}
			if !active {
				// Atomic records need no payload validation just to leave their
				// status unchanged. Payloads are validated when actually loaded.
				return nil, nil
			}
			value = status
		case "error":
			if seenError {
				return nil, fmt.Errorf("duplicate error field")
			}
			seenError = true
			if err = skipJSONValue(dec); err != nil {
				return nil, err
			}
			value = reason
		default:
			if err = skipJSONValue(dec); err != nil {
				return nil, err
			}
			continue
		}
		encoded, _ := json.Marshal(value)
		edits = append(edits, jsonEdit{start, dec.InputOffset(), append([]byte(":"), encoded...)})
	}
	end := dec.InputOffset()
	if tok, err = dec.Token(); err != nil {
		return nil, err
	} else if tok != json.Delim('}') {
		return nil, fmt.Errorf("expected record end")
	}
	if _, err = dec.Token(); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("unexpected data after record")
		}
		return nil, err
	}
	if !seenStatus {
		return nil, fmt.Errorf("missing status field")
	}
	if !seenError {
		encoded, _ := json.Marshal(reason)
		edits = append(edits, jsonEdit{end, end, append([]byte(`,"error":`), encoded...)})
	}
	return edits, nil
}

// Token traversal retains at most one scalar and the decoder's nesting stack;
// Decode into RawMessage or a partial struct would buffer an entire report.
func skipJSONValue(dec *json.Decoder) error {
	depth := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		if d, ok := tok.(json.Delim); ok {
			switch d {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
		}
		if depth == 0 {
			return nil
		}
	}
}

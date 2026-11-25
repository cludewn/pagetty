package capture

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func newTestRecording(columns, height, limit int) *recording {
	r := newRecording(columns, height)
	if limit > 0 {
		r.maxRows = limit
	}
	return r
}

func TestRecordingRowBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, data string
		want       []string
		limited    bool
	}{
		{"exact", "1\r\n2\r\n3\r\n", []string{"1", "2", "3"}, false},
		{"one more", "1\r\n2\r\n3\r\n4\r\n5", []string{"1", "2", "3"}, true},
		{"blank", "\r\n\r\n\r\n\r\n", []string{"", "", ""}, true},
		{"wrap", "abcdefghijklm", []string{"abcd", "efgh", "ijkl"}, true},
		{"bulk scroll", "1\r\n2\r\n3\x1b[99Smore", []string{"1", "2", "3"}, true},
		{"overwrites", strings.Repeat("\r\x1b[31mx\x1b[0m\x1b[K", 500), []string{"x"}, false},
	} {
		for _, height := range []int{1, 2, 5} {
			t.Run(tc.name+string(rune('0'+height)), func(t *testing.T) {
				r := newTestRecording(4, height, 3)
				defer r.screen.terminal.Close()
				if err := r.consume(context.Background(), []byte(tc.data), nil); err != nil {
					t.Fatal(err)
				}
				doc, err := r.document(context.Background())
				if err != nil || (r.limited != "") != tc.limited || !reflect.DeepEqual(rowTexts(doc), tc.want) {
					t.Fatalf("rows=%q limit=%q err=%v", rowTexts(doc), r.limited, err)
				}
			})
		}
	}
}

func TestRecordingByteBoundaryAndReplies(t *testing.T) {
	for _, tc := range []struct{ prefix, rest, text string }{
		{"日本e\u0301", "", "日本e\u0301"},
		{"日本e\u0301", "more\x1b[6n", "日本e\u0301"},
		{"A\xe6\x97", "\xa5\x1b[6n", "A"},
		{"A\x1b[3", "1mB\x1b[6n", "A"},
	} {
		r := newTestRecording(40, 4, 0)
		r.remaining = len(tc.prefix)
		var responses strings.Builder
		reply := func(p []byte) error { _, _ = responses.Write(p); return nil }
		for _, data := range []string{tc.prefix, tc.rest} {
			if err := r.consume(context.Background(), []byte(data), reply); err != nil {
				t.Fatal(err)
			}
		}
		doc, err := r.document(context.Background())
		if err != nil || strings.Join(rowTexts(doc), "") != tc.text || (r.limited != "") != (tc.rest != "") {
			t.Fatalf("case %q rows=%q limit=%q err=%v", tc.prefix, rowTexts(doc), r.limited, err)
		}
		if tc.rest != "" && (!strings.HasSuffix(responses.String(), "R") || r.screen.terminal.ScrollbackLen() != 0) {
			t.Fatalf("query response=%q history=%d", responses.String(), r.screen.terminal.ScrollbackLen())
		}
		_ = r.screen.terminal.Close()
	}
}

func TestRecordingConfiguredBoundaries(t *testing.T) {
	for _, tc := range []struct {
		columns, rows int
		valid         bool
	}{
		{3273, 20, true}, {3273, 21, false}, {3274, 1, false}, {256, 256, true}, {256, 257, false},
	} {
		if err := ValidateSize(tc.columns, tc.rows); (err == nil) != tc.valid {
			t.Errorf("size %d×%d: %v", tc.columns, tc.rows, err)
		}
	}
	for _, columns := range []int{20, 80} {
		r := newTestRecording(columns, 1, 0)
		expected := min(maxRecordRows, maxRecordCells/columns)
		if r.maxRows != expected || r.remaining != maxRecordBytes {
			t.Fatal("incorrect recording limits")
		}
		if err := r.consume(context.Background(), []byte(strings.Repeat("x\r\n", expected)), nil); err != nil || r.limited != "" {
			t.Fatalf("equal boundary: limit=%q err=%v", r.limited, err)
		}
		if err := r.consume(context.Background(), []byte("y"), nil); err != nil {
			t.Fatal(err)
		}
		doc, err := r.document(context.Background())
		if err != nil || r.limited == "" || len(doc.Rows) != expected {
			t.Fatalf("exceeded boundary: limit=%q rows=%d err=%v", r.limited, len(doc.Rows), err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if doc, err := r.document(ctx); !errors.Is(err, context.Canceled) || len(doc.Rows) != 0 {
			t.Fatalf("canceled conversion: rows=%d err=%v", len(doc.Rows), err)
		}
		_ = r.screen.terminal.Close()
	}
}

func TestRecordingActualByteBudget(t *testing.T) {
	r := newTestRecording(80, 24, 0)
	defer r.screen.terminal.Close()
	ctx := context.Background()
	first := bytes.Repeat([]byte{'\r'}, maxRecordBytes-1)
	first[0] = 'X'
	for _, data := range [][]byte{first, {'\r'}} {
		if err := r.consume(ctx, data, nil); err != nil || r.limited != "" {
			t.Fatalf("limit=%q before or at boundary: %v", r.limited, err)
		}
	}
	if r.remaining != 0 {
		t.Fatalf("remaining=%d at byte boundary", r.remaining)
	}
	if err := r.consume(ctx, []byte("dropped"), nil); err != nil || r.limited != "受信データ量" {
		t.Fatalf("limit=%q after boundary: %v", r.limited, err)
	}
	doc, err := r.document(ctx)
	if err != nil || !reflect.DeepEqual(rowTexts(doc), []string{"X"}) {
		t.Fatalf("rows=%q err=%v", rowTexts(doc), err)
	}
}

func TestRecordingScreenChangesBeforeLimitIsFinal(t *testing.T) {
	for _, tc := range []struct {
		name    string
		changes []string
		want    []string
	}{
		{"cursor", []string{"old", "\x1b[4;1H", "\x1b[1;1Hnew"}, []string{"new"}},
		{"temporary visible row", []string{"old", "\x1b[3;1Htemporary", "\x1b[2K\x1b[1;1Hnew"}, []string{"new"}},
		{"CR with full history", []string{"one\r\ntwo", "\rNEW"}, []string{"one", "NEW"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			height, limit := 5, 1
			if tc.name == "CR with full history" {
				height, limit = 1, 2
			}
			r := newTestRecording(12, height, limit)
			defer r.screen.terminal.Close()
			for _, chunk := range tc.changes {
				if err := r.consume(t.Context(), []byte(chunk), nil); err != nil || r.limited != "" {
					t.Fatalf("chunk=%q limit=%q err=%v", chunk, r.limited, err)
				}
			}
			doc, err := r.document(t.Context())
			if err != nil || r.limited != "" || !reflect.DeepEqual(rowTexts(doc), tc.want) {
				t.Fatalf("rows=%q limit=%q err=%v", rowTexts(doc), r.limited, err)
			}
		})
	}
}

func TestRecordingStopsOnHistoryOverflow(t *testing.T) {
	r := newTestRecording(12, 2, 2)
	defer r.screen.terminal.Close()
	if err := r.consume(t.Context(), []byte("1\r\n2\r\n3\r\n"), nil); err != nil || r.limited != "" || r.screen.terminal.ScrollbackLen() != 2 {
		t.Fatalf("exact history: limit=%q history=%d err=%v", r.limited, r.screen.terminal.ScrollbackLen(), err)
	}
	if err := r.consume(t.Context(), []byte("4\r\n"), nil); err != nil || r.limited == "" {
		t.Fatalf("overflowing history: limit=%q err=%v", r.limited, err)
	}
	doc, err := r.document(t.Context())
	if err != nil || !reflect.DeepEqual(rowTexts(doc), []string{"1", "2"}) {
		t.Fatalf("rows=%q err=%v", rowTexts(doc), err)
	}
}

func TestRecordingDetectsHistoryOverwrite(t *testing.T) {
	for _, tc := range []struct {
		name            string
		columns, height int
		data            string
	}{
		{"REP", 3, 2048, "A\x1b[2048;1H界\x1b[6144b"},
		{"bulk scroll", 80, 24, "A" + strings.Repeat("\x1b[24S", 72)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestRecording(tc.columns, tc.height, 1)
			defer r.screen.terminal.Close()
			sb := r.screen.terminal.Scrollback()
			expected := min(maxRecordRows, maxRecordCells/tc.columns)
			var notices []string
			r.onLimit = func(reason string) { notices = append(notices, reason) }
			var responses strings.Builder
			reply := func(p []byte) error { _, err := responses.Write(p); return err }
			if err := r.consume(t.Context(), []byte(tc.data), reply); err != nil {
				t.Fatal(err)
			}
			if err := r.consume(t.Context(), []byte("\x1b[6n"), reply); err != nil {
				t.Fatal(err)
			}
			doc, err := r.document(t.Context())
			if want := []string{historyOverwrite}; !reflect.DeepEqual(notices, want) || r.limited != historyOverwrite ||
				len(doc.Rows) != 1 || rowTexts(doc)[0] == "A" || err != nil {
				t.Fatalf("rows=%q limit=%q notices=%q err=%v", rowTexts(doc), r.limited, notices, err)
			}
			if sb.MaxLines() != expected || sb.Len() != 0 || !strings.HasSuffix(responses.String(), "R") {
				t.Fatalf("cap=%d want=%d history=%d reply=%q", sb.MaxLines(), expected, sb.Len(), responses.String())
			}
		})
	}
}

func TestRecordingFullRingPreservesOrdinaryPrefix(t *testing.T) {
	for _, columns := range []int{3, 80} {
		t.Run(fmt.Sprint(columns), func(t *testing.T) {
			r := newTestRecording(columns, 1, 0)
			defer r.screen.terminal.Close()
			n := r.maxRows
			expected := "A\r\n" + strings.Repeat("界\r\n", n-1)
			if err := r.consume(t.Context(), []byte(expected), nil); err != nil {
				t.Fatal(err)
			}
			doc, err := r.document(t.Context())
			if len(doc.Rows) != n || r.limited != "" || err != nil {
				t.Fatalf("exact row count=%d limit=%q err=%v", len(doc.Rows), r.limited, err)
			}
			if err := r.consume(t.Context(), []byte("B\r\n"), nil); err != nil {
				t.Fatal(err)
			}
			partial, err := r.document(t.Context())
			if r.limited != r.rowCause || !reflect.DeepEqual(doc.Rows, partial.Rows) || err != nil {
				t.Fatalf("partial row count=%d limit=%q err=%v", len(partial.Rows), r.limited, err)
			}
		})
	}
}

func TestRecordingGuestCanClearFullHistory(t *testing.T) {
	r := newTestRecording(3, 1, 0)
	defer r.screen.terminal.Close()
	for _, data := range []string{strings.Repeat("A\r\n", r.maxRows), "\x1b[3JB"} {
		if err := r.consume(t.Context(), []byte(data), nil); err != nil || r.limited != "" {
			t.Fatalf("limit=%q err=%v", r.limited, err)
		}
	}
	doc, err := r.document(t.Context())
	if !reflect.DeepEqual(rowTexts(doc), []string{"B"}) || r.limited != "" || err != nil {
		t.Fatalf("rows=%q limit=%q err=%v", rowTexts(doc), r.limited, err)
	}
}

func TestRecordingREPWithinLimit(t *testing.T) {
	r := newTestRecording(5, 5, 1)
	defer r.screen.terminal.Close()
	for _, data := range []string{"界\x1b[1", "b"} {
		if err := r.consume(t.Context(), []byte(data), nil); err != nil {
			t.Fatal(err)
		}
	}
	doc, err := r.document(t.Context())
	if err != nil || r.limited != "" || !reflect.DeepEqual(rowTexts(doc), []string{"界界"}) {
		t.Fatalf("rows=%q limit=%q err=%v", rowTexts(doc), r.limited, err)
	}
}

func TestRecordingREPAtExactHistoryLimit(t *testing.T) {
	r := newTestRecording(3, 1, 1)
	defer r.screen.terminal.Close()
	// REPで最初の「界」だけが履歴へ移る。消去した現在行は記録に含めない。
	for _, data := range []string{"界\x1b[", "b", "\x1b[2J\x1b[H"} {
		if err := r.consume(t.Context(), []byte(data), nil); err != nil || r.limited != "" {
			t.Fatalf("data=%q limit=%q err=%v", data, r.limited, err)
		}
	}
	doc, err := r.document(t.Context())
	if err != nil || r.limited != "" || r.screen.terminal.ScrollbackLen() != 1 ||
		!reflect.DeepEqual(rowTexts(doc), []string{"界"}) {
		t.Fatalf("rows=%q limit=%q err=%v", rowTexts(doc), r.limited, err)
	}
}

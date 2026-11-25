package render

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cludewn/pagetty/internal/display"
)

func TestRenderPageAndDimensionLimits(t *testing.T) {
	for _, tc := range []struct {
		name                                  string
		columns, lines, n, maxPages, expected int
		blank                                 bool
		limit                                 Limit
	}{
		{"before", 80, 4, 7, 2, 2, false, ""},
		{"equal", 80, 4, 8, 2, 2, false, ""},
		{"over", 80, 4, 9, 2, 2, false, limitPageSetting},
		{"blank boundary", 80, 4, 8, 2, 2, true, limitPageSetting},
		{"height split", 2, 1200, 1200, 2, 2, false, ""},
		{"area split", 3000, 40, 40, 3, 3, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := display.Document{Columns: tc.columns, Rows: make([]display.Row, tc.n)}
			if tc.blank {
				for i := range doc.Rows {
					if i != 2 {
						doc.Rows[i].Cells = []display.Cell{{Text: "X", Width: 1}}
					}
				}
			}
			var heights []int
			got, err := RenderPages(context.Background(), doc, Options{Format: SVG, MaxLines: tc.lines, PageLimit: tc.maxPages}, func(img Image) error {
				if img.Width > maxSide || img.Height > maxSide || img.Width*img.Height > maxPixels {
					t.Fatalf("oversize image: %dx%d", img.Width, img.Height)
				}
				heights = append(heights, img.Height)
				return nil
			})
			if err != nil || got != tc.limit || len(heights) != tc.expected {
				t.Fatalf("heights=%v limit=%q err=%v", heights, got, err)
			}
			if tc.blank && heights[0] != 3*lineHeight+2*padding {
				t.Fatalf("split ignored blank row: %v", heights)
			}
		})
	}
}

func TestRenderPageBufferBoundaries(t *testing.T) {
	for _, limit := range []Limit{limitSVG, limitImage, limitTotal} {
		b := pageBuffer{ctx: context.Background(), maxBytes: 5, limit: limit}
		for _, chunk := range []string{"ab", "cde"} {
			if n, err := b.Write([]byte(chunk)); n != len(chunk) || err != nil {
				t.Fatalf("%d %v", n, err)
			}
		}
		if n, err := b.Write([]byte("f")); n != 0 || !errors.Is(err, limit) || string(b.buf) != "abcde" || cap(b.buf) > 5 {
			t.Fatalf("%d %v buf=%q cap=%d", n, err, b.buf, cap(b.buf))
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b := pageBuffer{ctx: ctx, maxBytes: 0, limit: limitTotal}
	if _, err := b.Write([]byte("x")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestRenderCancelAfterFirstPage(t *testing.T) {
	for _, format := range []Format{SVG, PNG} {
		ctx, cancel := context.WithCancel(context.Background())
		n := 0
		_, err := RenderPages(ctx, numberedDocument(4), Options{Format: format, MaxLines: 1, PageLimit: 4}, func(img Image) error {
			n++
			cancel()
			return nil
		})
		if n != 1 || !errors.Is(err, context.Canceled) {
			t.Fatalf("%s pages=%d error=%v", format, n, err)
		}
	}
}

func TestPNGCancelDuringEncoding(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	w := &cancelPNGWriter{
		cancel: cancel,
		output: pageBuffer{ctx: ctx, maxBytes: maxImageBytes, limit: limitImage},
	}
	data := []byte("<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 160 160'><rect width='160' height='160'/></svg>")
	err := encodePNG(ctx, w, data, 160, 160)
	if !errors.Is(err, context.Canceled) || !strings.HasPrefix(string(w.output.buf), "\x89PNG") || w.accepted != 1 {
		t.Fatalf("err=%v writes=%d first=%q", err, w.accepted, w.output.buf)
	}
}

type cancelPNGWriter struct {
	cancel   context.CancelFunc
	output   pageBuffer
	accepted int
}

func (w *cancelPNGWriter) Write(p []byte) (int, error) {
	n, err := w.output.Write(p)
	if err == nil {
		w.accepted++
		w.cancel()
	}
	return n, err
}

func TestRenderPNGPixelBudget(t *testing.T) {
	const columns, lines = 80, 44
	const pixels = (columns*cellWidth + 2*padding) * (lines*lineHeight + 2*padding)
	const fit = maxPNGPixels / pixels
	doc := display.Document{Columns: columns, Rows: make([]display.Row, (fit+1)*lines)}
	got := 0
	limit, err := RenderPages(context.Background(), doc, Options{Format: PNG, MaxLines: lines, PageLimit: MaxPages}, func(img Image) error {
		got++
		return nil
	})
	if limit != limitPixels || err != nil || got != fit {
		t.Fatalf("limit=%q error=%v pages=%d want=%d", limit, err, got, fit)
	}
}

func TestRenderPageChoiceUsesActualSplit(t *testing.T) {
	doc := numberedDocument(8)
	doc.Rows[2] = display.Row{}
	choices, emitted := 0, 0
	reason, err := RenderPages(t.Context(), doc, Options{
		Format: SVG, MaxLines: 4, PageLimit: 2,
		ChoosePages: func(total, available int, hard Limit) (int, error) {
			choices++
			if total != 3 || available != 3 || hard != "" || emitted != 0 {
				t.Fatalf("total=%d available=%d hard=%q emitted=%d", total, available, hard, emitted)
			}
			return total, nil
		},
	}, func(img Image) error {
		emitted++
		if emitted == 1 && img.Height != 3*lineHeight+2*padding {
			t.Fatalf("first image ignores blank line split: %dpx", img.Height)
		}
		return nil
	})
	if reason != "" || err != nil || choices != 1 || emitted != 3 {
		t.Fatalf("limit=%q err=%v choices=%d images=%d", reason, err, choices, emitted)
	}
}

func TestRenderPageChoiceKeepsHardLimits(t *testing.T) {
	const columns, lines = 80, 44
	const pixels = (columns*cellWidth + 2*padding) * (lines*lineHeight + 2*padding)
	const fit = maxPNGPixels / pixels
	doc := display.Document{Columns: columns, Rows: make([]display.Row, (fit+1)*lines)}
	options := Options{
		Format: PNG, MaxLines: lines, PageLimit: 1,
		ChoosePages: func(total, available int, hard Limit) (int, error) {
			if total != fit+1 || available != fit || hard != limitPixels {
				t.Fatalf("total=%d available=%d limit=%q", total, available, hard)
			}
			return 0, nil
		},
		Progress: func(int, int) { t.Fatal("declining must not start rendering") },
	}
	reason, err := RenderPages(t.Context(), doc, options, func(Image) error {
		t.Fatal("declining must not generate an image")
		return nil
	})
	if reason != "" || err != nil {
		t.Fatalf("limit=%q err=%v", reason, err)
	}

	count := 0
	reason, err = RenderPages(t.Context(), numberedDocument(MaxPages+1), Options{
		Format: SVG, MaxLines: 1, PageLimit: 1,
		ChoosePages: func(total, available int, hard Limit) (int, error) {
			if total != MaxPages+1 || available != MaxPages || hard != limitPages {
				t.Fatalf("total=%d available=%d limit=%q", total, available, hard)
			}
			return total, nil
		},
	}, func(Image) error {
		count++
		return nil
	})
	if reason != limitPages || err != nil || count != MaxPages {
		t.Fatalf("limit=%q err=%v count=%d", reason, err, count)
	}
}

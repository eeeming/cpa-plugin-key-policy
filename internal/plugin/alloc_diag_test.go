package plugin

import (
	"encoding/json"
	"net/http"
	"runtime"
	"testing"
)

// Diagnostic benchmarks for the request.intercept_before path.
//
// CPA hands the interceptor the FULL request body (RequestInterceptRequest.Body
// is a []byte, so it travels base64-encoded inside the JSON envelope). The
// handler only needs Headers and Metadata, but decoding into the full struct
// also materialises the body — twice over (the base64 JSON string plus the
// decoded octets). These tests quantify that cost so it cannot regress.

func interceptRequestJSON(tb testing.TB, bodyBytes int) []byte {
	tb.Helper()
	body := make([]byte, bodyBytes)
	for i := range body {
		body[i] = 'x'
	}
	raw, err := json.Marshal(RequestInterceptRequest{
		RequestID:    "req-diag",
		SourceFormat: "openai",
		ToFormat:     "openai",
		Model:        "gpt-5.6-sol",
		Stream:       true,
		Headers:      http.Header{"Authorization": []string{"Bearer sk-diag-unknown"}},
		Body:         body,
		Metadata:     map[string]any{"api_key": "sk-diag-unknown"},
	})
	if err != nil {
		tb.Fatalf("marshal: %v", err)
	}
	return raw
}

// minAlloc runs fn repeatedly and reports the smallest allocation delta, which
// is the cleanest signal: the first run pays for warm-up, later runs do not.
func minAlloc(tb testing.TB, rounds int, fn func()) uint64 {
	tb.Helper()
	best := ^uint64(0)
	for i := 0; i < rounds; i++ {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		fn()
		runtime.ReadMemStats(&after)
		if d := after.TotalAlloc - before.TotalAlloc; d < best {
			best = d
		}
	}
	return best
}

// fullStructUnmarshal mirrors what interceptBefore does today.
func fullStructUnmarshal(tb testing.TB, app *App, raw []byte) {
	tb.Helper()
	var req RequestInterceptRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		tb.Fatalf("unmarshal: %v", err)
	}
	_ = app.store.Admit(req.Headers, nil, req.Metadata)
}

// narrowStructUnmarshal decodes only the fields the gate actually reads.
type narrowInterceptRequest struct {
	Headers  http.Header    `json:"Headers"`
	Metadata map[string]any `json:"Metadata"`
}

func narrowStructUnmarshal(tb testing.TB, app *App, raw []byte) {
	tb.Helper()
	var req narrowInterceptRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		tb.Fatalf("unmarshal: %v", err)
	}
	_ = app.store.Admit(req.Headers, nil, req.Metadata)
}

func TestInterceptBeforeAllocationCost(t *testing.T) {
	app := configureApp(t)

	t.Logf("%8s %12s %16s %16s %16s %8s",
		"body", "wire JSON", "HandleMethod", "full struct", "headers only", "saving")
	for _, mb := range []int{0, 1, 2, 4, 8} {
		raw := interceptRequestJSON(t, mb*1024*1024)

		viaHandler := minAlloc(t, 3, func() {
			if _, err := app.HandleMethod(MethodRequestInterceptBefore, raw); err != nil {
				t.Fatalf("intercept: %v", err)
			}
		})
		viaFull := minAlloc(t, 3, func() { fullStructUnmarshal(t, app, raw) })
		viaNarrow := minAlloc(t, 3, func() { narrowStructUnmarshal(t, app, raw) })

		// Guard: the real handler must not decode the body. Decoding it costs
		// about one full copy of the body per in-flight request.
		if mb >= 1 && viaHandler > viaFull/10 {
			t.Errorf("body=%dMB: handler allocated %.2fMB, want < 10%% of the %.2fMB "+
				"a full-struct decode costs (is Body being decoded again?)",
				mb, float64(viaHandler)/(1<<20), float64(viaFull)/(1<<20))
		}

		saving := 0.0
		if viaFull > 0 {
			saving = 100 * (1 - float64(viaNarrow)/float64(viaFull))
		}
		t.Logf("%6dMB %9.2fMB %13.2fMB %13.2fMB %13.2fMB %7.1f%%",
			mb, float64(len(raw))/(1<<20),
			float64(viaHandler)/(1<<20), float64(viaFull)/(1<<20),
			float64(viaNarrow)/(1<<20), saving)
	}
}

// TestInterceptBeforeConcurrentRetention approximates production: many requests
// in flight at once (hung upstreams), each carrying its body through the gate.
func TestInterceptBeforeConcurrentRetention(t *testing.T) {
	app := configureApp(t)
	const (
		bodyMB  = 2
		workers = 40
		perWork = 20
	)
	raw := interceptRequestJSON(t, bodyMB*1024*1024)

	var peakFull, peakNarrow uint64
	run := func(fn func(*App, []byte)) uint64 {
		var peak uint64
		done := make(chan struct{})
		stop := make(chan struct{})
		go func() {
			defer close(done)
			var m runtime.MemStats
			for {
				select {
				case <-stop:
					return
				default:
				}
				runtime.ReadMemStats(&m)
				if m.HeapInuse > peak {
					peak = m.HeapInuse
				}
			}
		}()
		start := make(chan struct{})
		finish := make(chan struct{}, workers)
		for i := 0; i < workers; i++ {
			go func() {
				<-start
				for j := 0; j < perWork; j++ {
					fn(app, raw)
				}
				finish <- struct{}{}
			}()
		}
		runtime.GC()
		close(start)
		for i := 0; i < workers; i++ {
			<-finish
		}
		close(stop)
		<-done
		return peak
	}
	peakFull = run(func(a *App, r []byte) { fullStructUnmarshal(t, a, r) })
	peakNarrow = run(func(a *App, r []byte) { narrowStructUnmarshal(t, a, r) })

	t.Logf("body=%dMB workers=%d: full-struct peakHeapInuse=%.1fMB  headers-only peakHeapInuse=%.1fMB",
		bodyMB, workers, float64(peakFull)/(1<<20), float64(peakNarrow)/(1<<20))
}

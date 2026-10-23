package ocs

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

type pilotRequest struct {
	Status      int              `json:"status"`
	DurationMS  float64          `json:"durationMs"`
	FirstByteMS *float64         `json:"firstByteMs,omitempty"`
	Usage       map[string]int64 `json:"providerUsage,omitempty"`
	Model       string           `json:"model,omitempty"`
	Incomplete  bool             `json:"incomplete"`
}

type pilotRelay struct {
	URL      string
	server   *http.Server
	mu       sync.Mutex
	wg       sync.WaitGroup
	requests []pilotRequest
}

// Parse only numerical usage and model IDs from response frames. A bounded
// transient buffer prevents large content blocks from accumulating in memory.
type pilotObserver struct {
	body        io.ReadCloser
	start       time.Time
	record      pilotRequest
	buffer      []byte
	sse         bool
	discardLine bool
	finish      func(pilotRequest)
	once        sync.Once
}

func (o *pilotObserver) parse(data []byte) {
	if o.sse {
		if !bytes.HasPrefix(data, []byte("data:")) {
			return
		}
		data = bytes.TrimSpace(bytes.TrimPrefix(data, []byte("data:")))
	}
	var value struct {
		Type    string                     `json:"type"`
		Model   string                     `json:"model"`
		Usage   map[string]json.RawMessage `json:"usage"`
		Message *struct {
			Model string                     `json:"model"`
			Usage map[string]json.RawMessage `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(data, &value) != nil {
		return
	}
	if value.Message != nil {
		value.Model = value.Message.Model
		value.Usage = value.Message.Usage
	}
	if value.Model != "" {
		o.record.Model = value.Model
	}
	if o.record.Usage == nil {
		o.record.Usage = map[string]int64{}
	}
	for _, key := range []string{"input_tokens", "output_tokens", "cache_read_input_tokens", "cache_creation_input_tokens"} {
		if raw, ok := value.Usage[key]; ok {
			var n int64
			if json.Unmarshal(raw, &n) == nil && n >= 0 {
				o.record.Usage[key] = n
			}
		}
	}
	if value.Type == "message_stop" || (!o.sse && value.Usage != nil) {
		o.record.Incomplete = false
	}
}

func (o *pilotObserver) Read(p []byte) (int, error) {
	n, err := o.body.Read(p)
	if n > 0 {
		if o.record.FirstByteMS == nil {
			ms := float64(time.Since(o.start).Microseconds()) / 1000
			o.record.FirstByteMS = &ms
		}
		for _, b := range p[:n] {
			if o.sse && b == '\n' {
				if !o.discardLine {
					o.parse(o.buffer)
				}
				o.buffer = o.buffer[:0]
				o.discardLine = false
			} else if !o.discardLine {
				if len(o.buffer) < 1<<20 {
					o.buffer = append(o.buffer, b)
				} else {
					o.buffer = nil
					o.discardLine = true
				}
			}
		}
	}
	if err == io.EOF && !o.sse && !o.discardLine {
		o.parse(o.buffer)
	}
	return n, err
}

func (o *pilotObserver) Close() error {
	err := o.body.Close()
	o.once.Do(func() { o.record.DurationMS = float64(time.Since(o.start).Microseconds()) / 1000; o.finish(o.record) })
	return err
}

func newPilotRelay(base string) (*pilotRelay, error) {
	target, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	r := &pilotRelay{URL: "http://" + l.Addr().String()}
	proxy := httputil.NewSingleHostReverseProxy(target)
	director := proxy.Director
	proxy.Director = func(req *http.Request) {
		director(req)
		req.Host = target.Host
		// Identical transport settings in both arms keep streamed usage visible;
		// the native client otherwise advertises gzip, which obscures SSE frames.
		req.Header.Set("Accept-Encoding", "identity")
	}
	proxy.FlushInterval = -1
	proxy.ErrorLog = nil
	// Do not pick up environment HTTP proxies for loopback traffic.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	proxy.Transport = transport
	type startKey struct{}
	proxy.ModifyResponse = func(res *http.Response) error {
		if res.Request.URL.Path != "/v1/messages" {
			return nil
		}
		start, _ := res.Request.Context().Value(startKey{}).(time.Time)
		res.Body = &pilotObserver{body: res.Body, start: start, record: pilotRequest{Status: res.StatusCode, Incomplete: true}, sse: strings.Contains(res.Header.Get("Content-Type"), "text/event-stream"), finish: func(record pilotRequest) { r.mu.Lock(); defer r.mu.Unlock(); r.requests = append(r.requests, record) }}
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, req *http.Request, e error) {
		if req.URL.Path == "/v1/messages" {
			start, _ := req.Context().Value(startKey{}).(time.Time)
			r.mu.Lock()
			r.requests = append(r.requests, pilotRequest{Status: 502, Incomplete: true, DurationMS: float64(time.Since(start).Microseconds()) / 1000})
			r.mu.Unlock()
		}
		http.Error(w, "pilot proxy connection failed", http.StatusBadGateway)
	}
	r.server = &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.wg.Add(1)
		defer r.wg.Done()
		// Don't expose the sidecar's admin surfaces to the agent.
		if req.Method != "POST" || (req.URL.Path != "/v1/messages" && req.URL.Path != "/v1/messages/count_tokens") {
			http.NotFound(w, req)
			return
		}
		proxy.ServeHTTP(w, req.WithContext(context.WithValue(req.Context(), startKey{}, time.Now())))
	})}
	go func() { _ = r.server.Serve(l) }()
	return r, nil
}

func (r *pilotRelay) Close() { _ = r.server.Close(); r.wg.Wait() }
func (r *pilotRelay) Snapshot() []pilotRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]pilotRequest(nil), r.requests...)
}

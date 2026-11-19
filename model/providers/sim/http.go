package sim

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/epzero/basamento/model"
)

// Image is one output file the HTTP simulator returns.
type Image struct {
	// URL is the address the model node downloads.
	URL string
	// Data is the file's bytes, served from memory by Bytes.
	Data []byte
	// Mime is the content type; image/png when empty.
	Mime string
}

// HTTPConfig adjusts the HTTP simulator. Its zero value returns two PNG
// images, a small usage and a cost of 480 milli-points after one running
// status call.
type HTTPConfig struct {
	// RunningPolls is how many status calls report "running" before the
	// invocation succeeds. Zero means one.
	RunningPolls int
	// Images are the outputs. Nil means two deterministic images.
	Images []Image
	// Usage is the reported usage. Nil means 1000 input and 500 output tokens.
	Usage *model.Usage
	// CostMpts is the reported cost. Zero means 480.
	CostMpts int64
	// Token, when set, is the bearer token every request must carry.
	Token string
}

// HTTP is an in-process vendor speaking the generic HTTP provider's endpoints:
// POST /start, GET /status/{id}, POST /cancel/{id} and GET /lookup.
type HTTP struct {
	cfg HTTPConfig

	mu    sync.Mutex
	next  int
	jobs  map[string]*httpJob
	byRef map[string]string
	polls map[string]int
	bytes map[string][]byte
	types map[string]string
}

type httpJob struct {
	id        string
	clientRef string
	cancelled bool
}

// NewHTTP returns an HTTP simulator.
func NewHTTP(cfg HTTPConfig) *HTTP {
	s := &HTTP{
		cfg:   cfg,
		jobs:  map[string]*httpJob{},
		byRef: map[string]string{},
		polls: map[string]int{},
		bytes: map[string][]byte{},
		types: map[string]string{},
	}
	if s.cfg.RunningPolls <= 0 {
		s.cfg.RunningPolls = 1
	}
	if s.cfg.Images == nil {
		s.cfg.Images = []Image{
			{URL: "https://sim.test/image-1.png", Data: []byte("simulated image one"), Mime: "image/png"},
			{URL: "https://sim.test/image-2.png", Data: []byte("simulated image two"), Mime: "image/png"},
		}
	}
	if s.cfg.Usage == nil {
		s.cfg.Usage = &model.Usage{InputTokens: 1000, OutputTokens: 500}
	}
	if s.cfg.CostMpts == 0 {
		s.cfg.CostMpts = 480
	}
	for _, img := range s.cfg.Images {
		s.bytes[img.URL] = img.Data
		mime := img.Mime
		if mime == "" {
			mime = "image/png"
		}
		s.types[img.URL] = mime
	}
	return s
}

// Bytes returns the bytes behind an output URL, so a scenario can serve them
// through the node's Fetcher without a network call.
func (s *HTTP) Bytes(url string) ([]byte, string, bool) {
	data, ok := s.bytes[url]
	if !ok {
		return nil, "", false
	}
	return data, s.types[url], true
}

// Handler returns the simulator's HTTP handler.
func (s *HTTP) Handler() http.Handler { return http.HandlerFunc(s.serve) }

func (s *HTTP) serve(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Token != "" && r.Header.Get("Authorization") != "Bearer "+s.cfg.Token {
		http.Error(w, `{"error":"unauthenticated"}`, http.StatusUnauthorized)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	switch {
	case path == "start" && r.Method == http.MethodPost:
		s.start(w, r)
	case strings.HasPrefix(path, "status/") && r.Method == http.MethodGet:
		s.status(w, strings.TrimPrefix(path, "status/"))
	case strings.HasPrefix(path, "cancel/") && r.Method == http.MethodPost:
		s.cancel(w, strings.TrimPrefix(path, "cancel/"))
	case path == "lookup" && r.Method == http.MethodGet:
		s.lookup(w, r.URL.Query().Get("client_ref"))
	default:
		http.Error(w, `{"error":"not_found"}`, http.StatusNotFound)
	}
}

func (s *HTTP) start(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	ref, _ := body["client_ref"].(string)
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.byRef[ref]; ok {
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "client_ref": ref})
		return
	}
	s.next++
	id := fmt.Sprintf("job-%d", s.next)
	s.jobs[id] = &httpJob{id: id, clientRef: ref}
	s.byRef[ref] = id
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "client_ref": ref})
}

func (s *HTTP) status(w http.ResponseWriter, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[id]
	if !ok {
		http.Error(w, `{"error":"not_found"}`, http.StatusNotFound)
		return
	}
	if job.cancelled {
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "state": "cancelled", "progress": 1.0})
		return
	}
	s.polls[id]++
	if s.polls[id] <= s.cfg.RunningPolls {
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "state": "running", "progress": 0.5})
		return
	}
	outputs := make([]map[string]any, 0, len(s.cfg.Images))
	for _, img := range s.cfg.Images {
		mime := img.Mime
		if mime == "" {
			mime = "image/png"
		}
		outputs = append(outputs, map[string]any{"url": img.URL, "media": "image", "mime": mime})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": id, "state": "succeeded", "progress": 1.0,
		"outputs": outputs, "usage": s.cfg.Usage, "cost_mpts": s.cfg.CostMpts,
	})
}

func (s *HTTP) cancel(w http.ResponseWriter, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[id]
	if !ok {
		http.Error(w, `{"error":"not_found"}`, http.StatusNotFound)
		return
	}
	job.cancelled = true
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "state": "cancelled"})
}

func (s *HTTP) lookup(w http.ResponseWriter, ref string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.byRef[ref]
	if !ok {
		http.Error(w, `{"error":"not_found"}`, http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "client_ref": ref})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

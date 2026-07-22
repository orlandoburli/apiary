// Package queuehttp exposes the durable queue contract to remote workers over
// an authenticated, versioned HTTP/JSON protocol.
package queuehttp

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/orlandoburli/apiary/internal/queue"
)

const Prefix = "/v1/queue"

type Server struct {
	Store         queue.Store
	Token         string
	LeaseDuration time.Duration
	WorkerTimeout time.Duration
	Policy        queue.ConcurrencyPolicy
	OnTerminal    func(context.Context, queue.Job) error
}

func (s Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(Prefix+"/health", s.health)
	mux.HandleFunc(Prefix+"/workers/register", s.register)
	mux.HandleFunc(Prefix+"/workers/", s.worker)
	mux.HandleFunc(Prefix+"/claim", s.claim)
	mux.HandleFunc(Prefix+"/jobs", s.jobs)
	mux.HandleFunc(Prefix+"/jobs/", s.job)
	return s.authenticate(mux)
}

func (s Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSpace(s.Token) == "" {
			http.Error(w, "remote worker protocol is not configured", http.StatusServiceUnavailable)
			return
		}
		provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if len(provided) != len(s.Token) || subtle.ConstantTimeCompare([]byte(provided), []byte(s.Token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"protocol_version": queue.WorkerProtocolVersion, "ready": s.Store != nil})
}

func (s Server) register(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) {
		return
	}
	var worker queue.Worker
	if !decode(w, r, &worker) {
		return
	}
	if worker.ProtocolVersion != queue.WorkerProtocolVersion {
		http.Error(w, fmt.Sprintf("unsupported protocol version %d", worker.ProtocolVersion), http.StatusUnprocessableEntity)
		return
	}
	if err := s.Store.RegisterWorker(r.Context(), &worker); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, worker)
}

func (s Server) worker(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, Prefix+"/workers/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 1 && parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 1 && r.Method == http.MethodGet {
		workers, err := s.Store.ListWorkers(r.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		for _, worker := range workers {
			if worker.ID == parts[0] {
				writeJSON(w, http.StatusOK, worker)
				return
			}
		}
		http.NotFound(w, r)
		return
	}
	if len(parts) != 2 || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Ready    bool `json:"ready"`
		Draining bool `json:"draining"`
	}
	if !decode(w, r, &body) {
		return
	}
	var err error
	switch parts[1] {
	case "heartbeat":
		err = s.Store.HeartbeatWorker(r.Context(), parts[0], body.Ready)
	case "drain":
		err = s.Store.SetWorkerDrain(r.Context(), parts[0], body.Draining)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s Server) claim(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) {
		return
	}
	var body struct {
		WorkerID string `json:"worker_id"`
	}
	if !decode(w, r, &body) {
		return
	}
	claim, err := s.Store.Claim(r.Context(), queue.ClaimRequest{WorkerID: body.WorkerID, LeaseDuration: s.LeaseDuration, WorkerTimeout: s.WorkerTimeout, Policy: s.Policy})
	if errors.Is(err, queue.ErrNoJob) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, claim)
}

func (s Server) jobs(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodGet) {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	jobs, err := s.Store.ListJobs(r.Context(), queue.JobState(r.URL.Query().Get("state")), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, jobs)
}

func (s Server) job(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, Prefix+"/jobs/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) == 1 && r.Method == http.MethodGet {
		job, err := s.Store.GetJob(r.Context(), parts[0])
		if err != nil {
			writeError(w, err)
			return
		}
		if job == nil {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, job)
		return
	}
	if len(parts) == 2 && parts[1] == "cancel" && r.Method == http.MethodPost {
		if err := s.Store.RequestCancel(r.Context(), parts[0]); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	if len(parts) != 4 || parts[1] != "attempts" || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	jobID, attemptID, action := parts[0], parts[2], parts[3]
	var body struct {
		Token     string             `json:"token"`
		Extension time.Duration      `json:"extension"`
		Result    queue.FinishResult `json:"result"`
	}
	if !decode(w, r, &body) {
		return
	}
	switch action {
	case "heartbeat":
		result, err := s.Store.Heartbeat(r.Context(), jobID, attemptID, body.Token, body.Extension)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	case "finish":
		if err := s.Store.Finish(r.Context(), jobID, attemptID, body.Token, body.Result); err != nil {
			writeError(w, err)
			return
		}
		if s.OnTerminal != nil {
			job, err := s.Store.GetJob(r.Context(), jobID)
			if err != nil {
				writeError(w, err)
				return
			}
			if job != nil && (job.State == queue.JobSucceeded || job.State == queue.JobFailed || job.State == queue.JobCanceled) {
				if err := s.OnTerminal(r.Context(), *job); err != nil {
					writeError(w, err)
					return
				}
			}
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	default:
		http.NotFound(w, r)
	}
}

func method(w http.ResponseWriter, r *http.Request, expected string) bool {
	if r.Method == expected {
		return true
	}
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	return false
}
func decode(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 4<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	if errors.Is(err, queue.ErrStaleClaim) || errors.Is(err, queue.ErrWorkerUnknown) {
		status = http.StatusConflict
	}
	if errors.Is(err, queue.ErrWorkerDraining) || errors.Is(err, queue.ErrWorkerUnhealthy) {
		status = http.StatusServiceUnavailable
	}
	http.Error(w, err.Error(), status)
}

type Client struct {
	BaseURL, Token string
	HTTPClient     *http.Client
}

func (c *Client) do(ctx context.Context, method, path string, request, response any) (int, error) {
	var body io.Reader
	if request != nil {
		encoded, err := json.Marshal(request)
		if err != nil {
			return 0, err
		}
		body = strings.NewReader(string(encoded))
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+Prefix+path, body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if request != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNoContent {
		return res.StatusCode, nil
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return res.StatusCode, fmt.Errorf("queue protocol: %s", strings.TrimSpace(string(message)))
	}
	if response != nil {
		if err := json.NewDecoder(res.Body).Decode(response); err != nil {
			return res.StatusCode, err
		}
	}
	return res.StatusCode, nil
}

func (c *Client) RegisterWorker(ctx context.Context, w *queue.Worker) error {
	_, err := c.do(ctx, http.MethodPost, "/workers/register", w, w)
	return err
}
func (c *Client) HeartbeatWorker(ctx context.Context, id string, ready bool) error {
	_, err := c.do(ctx, http.MethodPost, "/workers/"+url.PathEscape(id)+"/heartbeat", map[string]any{"ready": ready}, nil)
	return err
}
func (c *Client) SetWorkerDrain(ctx context.Context, id string, draining bool) error {
	_, err := c.do(ctx, http.MethodPost, "/workers/"+url.PathEscape(id)+"/drain", map[string]any{"draining": draining}, nil)
	return err
}
func (c *Client) Claim(ctx context.Context, request queue.ClaimRequest) (*queue.Claim, error) {
	var claim queue.Claim
	status, err := c.do(ctx, http.MethodPost, "/claim", map[string]string{"worker_id": request.WorkerID}, &claim)
	if status == http.StatusNoContent {
		return nil, queue.ErrNoJob
	}
	return &claim, err
}
func (c *Client) Heartbeat(ctx context.Context, job, attempt, token string, extension time.Duration) (*queue.HeartbeatResult, error) {
	var result queue.HeartbeatResult
	_, err := c.do(ctx, http.MethodPost, "/jobs/"+url.PathEscape(job)+"/attempts/"+url.PathEscape(attempt)+"/heartbeat", map[string]any{"token": token, "extension": extension}, &result)
	return &result, err
}
func (c *Client) Finish(ctx context.Context, job, attempt, token string, result queue.FinishResult) error {
	_, err := c.do(ctx, http.MethodPost, "/jobs/"+url.PathEscape(job)+"/attempts/"+url.PathEscape(attempt)+"/finish", map[string]any{"token": token, "result": result}, nil)
	return err
}
func (c *Client) RequestCancel(ctx context.Context, id string) error {
	_, err := c.do(ctx, http.MethodPost, "/jobs/"+url.PathEscape(id)+"/cancel", map[string]any{}, nil)
	return err
}
func (c *Client) GetJob(ctx context.Context, id string) (*queue.Job, error) {
	var job queue.Job
	status, err := c.do(ctx, http.MethodGet, "/jobs/"+url.PathEscape(id), nil, &job)
	if status == http.StatusNotFound {
		return nil, nil
	}
	return &job, err
}
func (c *Client) ListJobs(ctx context.Context, state queue.JobState, limit int) ([]queue.Job, error) {
	var jobs []queue.Job
	query := "?state=" + url.QueryEscape(string(state)) + "&limit=" + strconv.Itoa(limit)
	_, err := c.do(ctx, http.MethodGet, "/jobs"+query, nil, &jobs)
	return jobs, err
}

// Reclaim and affinity/cross-job cancellation remain control-plane operations.
func (c *Client) Enqueue(context.Context, *queue.Job) (bool, error) {
	return false, errors.New("enqueue is a control-plane operation")
}
func (c *Client) RequestCancelFor(context.Context, string, string) (int, error) {
	return 0, errors.New("bulk cancellation is a control-plane operation")
}
func (c *Client) ReleaseAffinity(context.Context, string) error {
	return errors.New("affinity release is a control-plane operation")
}
func (c *Client) ReclaimExpired(context.Context, time.Time) (int, error) { return 0, nil }
func (c *Client) ListWorkers(context.Context) ([]queue.Worker, error) {
	return nil, errors.New("worker listing is a control-plane operation")
}

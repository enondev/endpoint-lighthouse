package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/enondev/endpoint-lighthouse-api/internal/cloudflare"
	"github.com/enondev/endpoint-lighthouse-api/internal/handlers"
	"github.com/enondev/endpoint-lighthouse-api/internal/redis"
	"github.com/enondev/endpoint-lighthouse-api/internal/sse"
)

// recordedD1Query captures each SQL statement and parameter slice sent to the mock D1 server.
type recordedD1Query struct {
	SQL    string `json:"sql"`
	Params []any  `json:"params"`
}

// mockD1Server simulates Cloudflare's D1 REST query endpoint.
type mockD1Server struct {
	Server  *httptest.Server
	mu      sync.Mutex
	Queries []recordedD1Query
	// Handler allows tests to return custom rows or errors based on the SQL query.
	Handler func(sql string, params []any) ([]map[string]any, int, error)
}

func newMockD1Server(t *testing.T) *mockD1Server {
	t.Helper()
	m := &mockD1Server{}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			SQL    string `json:"sql"`
			Params []any  `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}

		m.mu.Lock()
		m.Queries = append(m.Queries, recordedD1Query{SQL: req.SQL, Params: req.Params})
		handler := m.Handler
		m.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if handler != nil {
			rows, status, err := handler(req.SQL, req.Params)
			if err != nil {
				if status == 0 {
					status = http.StatusInternalServerError
				}
				w.WriteHeader(status)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"success": false,
					"errors":  []map[string]string{{"message": err.Error()}},
				})
				return
			}
			if status != 0 {
				w.WriteHeader(status)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"result":  []map[string]any{{"results": rows}},
			})
			return
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"result":  []map[string]any{{"results": []map[string]any{}}},
		})
	}))
	t.Cleanup(m.Server.Close)
	return m
}

func (m *mockD1Server) RecordedQueries() []recordedD1Query {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]recordedD1Query, len(m.Queries))
	copy(cp, m.Queries)
	return cp
}

// recordedWebhook captures each POST /analyze request received by the mock SnapDeploy worker.
type recordedWebhook struct {
	Path   string
	Secret string
	Body   []byte
}

// mockSnapDeployServer simulates the SnapDeploy ai-worker-py /analyze endpoint.
type mockSnapDeployServer struct {
	Server   *httptest.Server
	mu       sync.Mutex
	Requests []recordedWebhook
	// StatusQueue pops one HTTP status per attempt; defaults to 202 Accepted when empty.
	StatusQueue []int
	notifyCh    chan struct{}
}

func newMockSnapDeployServer(t *testing.T) *mockSnapDeployServer {
	t.Helper()
	m := &mockSnapDeployServer{
		notifyCh: make(chan struct{}, 64),
	}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		m.mu.Lock()
		m.Requests = append(m.Requests, recordedWebhook{
			Path:   r.URL.Path,
			Secret: r.Header.Get("X-Internal-Secret"),
			Body:   body,
		})
		status := http.StatusAccepted
		if len(m.StatusQueue) > 0 {
			status = m.StatusQueue[0]
			m.StatusQueue = m.StatusQueue[1:]
		}
		m.mu.Unlock()

		select {
		case m.notifyCh <- struct{}{}:
		default:
		}

		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, `{"status":%d}`, status)
	}))
	t.Cleanup(m.Server.Close)
	return m
}

func (m *mockSnapDeployServer) WaitForRequests(t *testing.T, count int, timeout time.Duration) []recordedWebhook {
	t.Helper()
	deadline := time.After(timeout)
	for {
		m.mu.Lock()
		got := len(m.Requests)
		if got >= count {
			cp := make([]recordedWebhook, got)
			copy(cp, m.Requests)
			m.mu.Unlock()
			return cp
		}
		m.mu.Unlock()

		select {
		case <-m.notifyCh:
		case <-deadline:
			t.Fatalf("timed out waiting for %d snapdeploy requests (got %d)", count, got)
		}
	}
}

type fakeRedisEntry struct {
	val       string
	expiresAt time.Time
}

// fakeRESPServer implements a minimal thread-safe TCP RESP server supporting AUTH, PING, and SET NX EX.
type fakeRESPServer struct {
	listener net.Listener
	password string
	mu       sync.Mutex
	store    map[string]fakeRedisEntry
	FailNext bool
}

func newFakeRESPServer(t *testing.T, password string) *fakeRESPServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start fake RESP server: %v", err)
	}
	s := &fakeRESPServer{
		listener: ln,
		password: password,
		store:    make(map[string]fakeRedisEntry),
	}
	go s.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *fakeRESPServer) Addr() string {
	return s.listener.Addr().String()
}

func (s *fakeRESPServer) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handleConn(conn)
	}
}

func (s *fakeRESPServer) handleConn(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	authed := s.password == ""

	for {
		args, err := readRESPArray(r)
		if err != nil || len(args) == 0 {
			return
		}

		s.mu.Lock()
		fail := s.FailNext
		s.FailNext = false
		s.mu.Unlock()
		if fail {
			_, _ = conn.Write([]byte("-ERR simulated redis failure\r\n"))
			return
		}

		cmd := strings.ToUpper(args[0])
		switch cmd {
		case "AUTH":
			if len(args) >= 2 && args[1] == s.password {
				authed = true
				_, _ = conn.Write([]byte("+OK\r\n"))
			} else {
				_, _ = conn.Write([]byte("-WRONGPASS invalid password\r\n"))
			}
		case "PING":
			_, _ = conn.Write([]byte("+PONG\r\n"))
		case "SET":
			if !authed {
				_, _ = conn.Write([]byte("-NOAUTH Authentication required\r\n"))
				continue
			}
			if len(args) < 3 {
				_, _ = conn.Write([]byte("-ERR wrong number of arguments\r\n"))
				continue
			}
			key, val := args[1], args[2]
			nx := false
			var ttl time.Duration
			for i := 3; i < len(args); i++ {
				opt := strings.ToUpper(args[i])
				if opt == "NX" {
					nx = true
				} else if opt == "EX" && i+1 < len(args) {
					sec, _ := strconv.Atoi(args[i+1])
					ttl = time.Duration(sec) * time.Second
					i++
				}
			}

			s.mu.Lock()
			entry, exists := s.store[key]
			if exists && !entry.expiresAt.IsZero() && time.Now().After(entry.expiresAt) {
				delete(s.store, key)
				exists = false
			}
			if nx && exists {
				s.mu.Unlock()
				_, _ = conn.Write([]byte("$-1\r\n"))
				continue
			}
			var exp time.Time
			if ttl > 0 {
				exp = time.Now().Add(ttl)
			}
			s.store[key] = fakeRedisEntry{val: val, expiresAt: exp}
			s.mu.Unlock()
			_, _ = conn.Write([]byte("+OK\r\n"))
		default:
			_, _ = conn.Write([]byte("-ERR unknown command\r\n"))
		}
	}
}

func readRESPArray(r *bufio.Reader) ([]string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "*") {
		return nil, fmt.Errorf("expected array, got %q", line)
	}
	count, err := strconv.Atoi(line[1:])
	if err != nil {
		return nil, err
	}
	args := make([]string, 0, count)
	for i := 0; i < count; i++ {
		hdr, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		hdr = strings.TrimSpace(hdr)
		if !strings.HasPrefix(hdr, "$") {
			return nil, fmt.Errorf("expected bulk string, got %q", hdr)
		}
		size, err := strconv.Atoi(hdr[1:])
		if err != nil {
			return nil, err
		}
		buf := make([]byte, size+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		args = append(args, string(buf[:size]))
	}
	return args, nil
}

// testEnv bundles all mocked dependencies for a single test case.
type testEnv struct {
	D1         *mockD1Server
	SnapDeploy *mockSnapDeployServer
	Redis      *fakeRESPServer
	RDB        *redis.Client
	Handler    *handlers.APIHandler
	Hub        *sse.Hub
}

func setupTestEnv(t *testing.T) *testEnv {
	t.Helper()

	d1Mock := newMockD1Server(t)
	snapMock := newMockSnapDeployServer(t)
	redisMock := newFakeRESPServer(t, "test-redis-pass")

	t.Setenv("CF_API_BASE", d1Mock.Server.URL)

	origBackoffs := snapDeployBackoffs
	origClient := snapDeployHTTPClient
	snapDeployBackoffs = []time.Duration{0, 5 * time.Millisecond, 10 * time.Millisecond}
	snapDeployHTTPClient = &http.Client{Timeout: 2 * time.Second}
	t.Cleanup(func() {
		snapDeployBackoffs = origBackoffs
		snapDeployHTTPClient = origClient
	})

	d1Client := cloudflare.NewD1Client("acc-test", "db-test", "tok-test")
	rdb, err := redis.NewClient("redis://:test-redis-pass@" + redisMock.Addr())
	if err != nil {
		t.Fatalf("failed to create test redis client: %v", err)
	}
	hub := sse.NewHub()
	apiHandler := handlers.NewAPIHandler(d1Client, hub)

	return &testEnv{
		D1:         d1Mock,
		SnapDeploy: snapMock,
		Redis:      redisMock,
		RDB:        rdb,
		Handler:    apiHandler,
		Hub:        hub,
	}
}

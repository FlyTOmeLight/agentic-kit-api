//go:build linux

// EXPERIMENT ONLY: one immutable lease per disposable PID namespace.
// This is not the Sandbox Center service or its production Runtime adapter.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"
	"syscall"
	"time"
)

const (
	stateDir = "/state"
	socket   = stateDir + "/control.sock"
)

type receipt struct {
	ID        string
	Hash      string
	State     string // Pending, Exited, Unknown. Exited is NOT process-tree-empty.
	ExitCode  int
	Truncated bool
}

type lease struct {
	Deadline time.Time
	Closed   bool
}

type server struct {
	mu      sync.Mutex
	lease   lease
	records map[string]receipt
}

var validID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// File and parent directory are synchronized before acknowledging durable state.
func save(name string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(stateDir, ".receipt-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(f.Name(), name); err != nil {
		return err
	}
	dir, err := os.Open(stateDir)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func load(name string, value any) error {
	data, err := os.ReadFile(name)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}

func receiptPath(id string) string { return stateDir + "/" + id + ".json" }

func (s *server) open() bool {
	return !s.lease.Closed && !s.lease.Deadline.IsZero() && time.Now().Before(s.lease.Deadline)
}

func fail(w http.ResponseWriter, status int, err error) { http.Error(w, err.Error(), status) }

func (s *server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		fail(w, 405, errors.New("POST required"))
		return
	}
	var input struct {
		ID      string
		Script  string
		Seconds int
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		fail(w, 400, err)
		return
	}
	switch r.URL.Path {
	case "/bind":
		// Deliberately no active renewal or cross-epoch rebind in this experiment.
		if !s.lease.Deadline.IsZero() || s.lease.Closed || input.Seconds < 1 || input.Seconds > 60 {
			fail(w, 409, errors.New("lease is immutable or duration invalid"))
			return
		}
		s.lease.Deadline = time.Now().Add(time.Duration(input.Seconds) * time.Second)
		// Start the local monotonic timer before any blocking persistence call.
		deadline := s.lease.Deadline
		go func() { time.Sleep(time.Until(deadline)); os.Exit(0) }()
		if err := save(stateDir+"/lease.json", s.lease); err != nil {
			log.Fatal(err)
		}
		json.NewEncoder(w).Encode(s.lease)
	case "/start", "/query":
		if !validID.MatchString(input.ID) || input.ID == "lease" {
			fail(w, 400, errors.New("invalid execution ID"))
			return
		}
		prior, exists := s.records[input.ID]
		if r.URL.Path == "/query" {
			if !exists {
				fail(w, 404, errors.New("unknown ID; absence is not permission to replay"))
				return
			}
			output, err := os.ReadFile(stateDir + "/" + input.ID + ".log")
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				fail(w, 500, err)
				return
			}
			json.NewEncoder(w).Encode(struct {
				Receipt receipt
				Output  string
			}{prior, string(output)})
			return
		}
		if !s.open() {
			fail(w, 409, errors.New("admission closed"))
			return
		}
		hash := sha256.Sum256([]byte(input.Script))
		hashText := hex.EncodeToString(hash[:])
		if exists {
			if prior.Hash != hashText {
				fail(w, 409, errors.New("ID payload conflict"))
				return
			}
			json.NewEncoder(w).Encode(prior)
			return
		}
		record := receipt{ID: input.ID, Hash: hashText, State: "Pending"}
		if err := save(receiptPath(input.ID), record); err != nil {
			log.Fatal(err)
		}
		s.records[input.ID] = record
		out, err := os.OpenFile(stateDir+"/"+input.ID+".log", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			log.Fatal(err)
		}
		writer := &boundedOutput{file: out, remaining: 65536}
		cmd := exec.Command("/bin/sh", "-c", input.Script)
		cmd.Dir = "/workspace"
		cmd.Env = []string{"PATH=/bin:/usr/bin", "HOME=/workspace"}
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Setsid:     true,
			Credential: &syscall.Credential{Uid: 65534, Gid: 65534, Groups: []uint32{}},
		}
		// A writer creates pipes: the user receives no writable FD to the ledger.
		cmd.Stdout = writer
		cmd.Stderr = writer
		if !s.open() {
			out.Close()
			fail(w, 409, errors.New("expired before launch"))
			return
		}
		if err = cmd.Start(); err != nil {
			out.Close()
			fail(w, 500, err)
			return
		}
		go func() {
			waitErr := cmd.Wait()
			syncErr := out.Sync()
			closeErr := out.Close()
			s.mu.Lock()
			defer s.mu.Unlock()
			// An output/drain failure is not a complete result.
			var exitErr *exec.ExitError
			if writer.err != nil || syncErr != nil || closeErr != nil ||
				(waitErr != nil && !errors.As(waitErr, &exitErr)) {
				return
			}
			record.State = "Exited"
			record.ExitCode = cmd.ProcessState.ExitCode()
			record.Truncated = writer.truncated
			if err := save(receiptPath(record.ID), record); err != nil {
				log.Fatal(err)
			}
			s.records[record.ID] = record
		}()
		json.NewEncoder(w).Encode(record)
	default:
		fail(w, 404, errors.New("unsupported operation"))
	}
}

// Drain excess output without filling the protected volume. Single command's
// stdout/stderr use the same comparable writer, serialized by os/exec.
type boundedOutput struct {
	file      *os.File
	remaining int
	err       error
	truncated bool
}

func (w *boundedOutput) Write(data []byte) (int, error) {
	n := len(data)
	keep := min(n, w.remaining)
	if keep < n {
		w.truncated = true
	}
	if keep > 0 {
		written, err := w.file.Write(data[:keep])
		w.remaining -= written
		if err == nil && written != keep {
			err = io.ErrShortWrite
		}
		if err != nil {
			w.err = err
			return written, err
		}
	}
	return n, nil
}

func run() error {
	if os.Getpid() != 1 || os.Geteuid() != 0 {
		return errors.New("requires root PID 1 in a disposable private PID namespace")
	}
	if err := os.Chmod(stateDir, 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(stateDir+"/lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return err
	}
	s := &server{records: make(map[string]receipt)}
	if err = load(stateDir+"/lease.json", &s.lease); err == nil {
		// Any restart closes the old lease, even before its original deadline.
		s.lease.Closed = true
		if err = save(stateDir+"/lease.json", s.lease); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	files, err := filepath.Glob(stateDir + "/*.json")
	if err != nil {
		return err
	}
	for _, name := range files {
		if name == stateDir+"/lease.json" {
			continue
		}
		var record receipt
		if err = load(name, &record); err != nil {
			return err
		}
		if !validID.MatchString(record.ID) || receiptPath(record.ID) != name {
			return errors.New("invalid stored identity")
		}
		if record.State != "Exited" {
			record.State = "Unknown"
			if err = save(name, record); err != nil {
				return err
			}
		}
		s.records[record.ID] = record
	}
	if err = os.Remove(socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	defer listener.Close()
	if err = os.Chmod(socket, 0o600); err != nil {
		return err
	}
	return (&http.Server{Handler: http.HandlerFunc(s.serve), ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 5 * time.Second}).Serve(
		listener,
	)
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "serve" {
		if err := run(); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) != 4 || os.Args[1] != "call" {
		log.Fatal("usage: probe serve | probe call /path JSON")
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	r, err := client.Post("http://supervisor"+os.Args[2], "application/json", bytes.NewBufferString(os.Args[3]))
	if err != nil {
		log.Fatal(err)
	}
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 131072))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%d %s", r.StatusCode, body)
	if r.StatusCode >= 400 {
		os.Exit(1)
	}
}

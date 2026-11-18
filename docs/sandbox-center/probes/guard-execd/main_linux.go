//go:build linux

// This executable is a boundary probe, not a production Guard. The trusted
// caller reaches it through a private root-only socket. It exercises the phase
// 1b minimum: renewable admission, durable operation receipts, explicit Unknown
// outcomes, and read-back of the accepted lease. It still has no remote mTLS,
// upstream RunAuthorization, orphan reaper, or full file API.
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
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	stateDir       = "/state"
	guardSocket    = stateDir + "/guard.sock"
	execdSocket    = stateDir + "/execd.sock"
	admissionFile  = stateDir + "/admission.json"
	operationsDir  = stateDir + "/operations"
	maxRequestSize = 16384
	maxOutputSize  = 1 << 20
)

const (
	statePending         = "Pending"
	stateDispatchStarted = "DispatchStarted"
	stateSucceeded       = "Succeeded"
	stateFailed          = "Failed"
	stateUnknown         = "Unknown"
)

var operationIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)

func socketClient(socket string, timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		}},
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func decode(w http.ResponseWriter, r *http.Request, value any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestSize))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("expected exactly one JSON value")
	}
	return nil
}

type binding struct {
	SandboxID      string `json:"sandboxId"`
	RunID          string `json:"runId"`
	WorkerID       string `json:"workerId"`
	Epoch          int64  `json:"epoch"`
	LeaseID        string `json:"leaseId"`
	FencingToken   int64  `json:"fencingToken"`
	AdmissionEpoch int64  `json:"admissionEpoch"`
	Generation     int64  `json:"generation"`
	RuntimeBootID  string `json:"runtimeBootId"`
}

func (b binding) valid() bool {
	return b.SandboxID != "" && b.RunID != "" && b.WorkerID != "" &&
		b.Epoch > 0 && b.LeaseID != "" && b.FencingToken > 0 &&
		b.AdmissionEpoch > 0 && b.Generation > 0 && b.RuntimeBootID != ""
}

type admissionRequest struct {
	SandboxID      string    `json:"sandboxId"`
	RunID          string    `json:"runId"`
	WorkerID       string    `json:"workerId"`
	Epoch          int64     `json:"epoch"`
	LeaseID        string    `json:"leaseId"`
	FencingToken   int64     `json:"fencingToken"`
	AdmissionEpoch int64     `json:"admissionEpoch"`
	Generation     int64     `json:"generation"`
	RuntimeBootID  string    `json:"runtimeBootId"`
	Revision       int64     `json:"revision"`
	Deadline       time.Time `json:"deadline"`
	RequestKey     string    `json:"requestKey"`
}

func (r admissionRequest) binding() binding {
	return binding{
		SandboxID: r.SandboxID, RunID: r.RunID, WorkerID: r.WorkerID,
		Epoch: r.Epoch, LeaseID: r.LeaseID, FencingToken: r.FencingToken,
		AdmissionEpoch: r.AdmissionEpoch, Generation: r.Generation,
		RuntimeBootID: r.RuntimeBootID,
	}
}

type admissionRecord struct {
	Binding    binding   `json:"binding"`
	Revision   int64     `json:"revision"`
	Deadline   time.Time `json:"deadline"`
	RequestKey string    `json:"requestKey"`
	AcceptedAt time.Time `json:"acceptedAt"`
}

type admissionState struct {
	mu           sync.Mutex
	bound        bool
	closed       bool
	binding      binding
	revision     int64
	deadline     time.Time
	requestKey   string
	acceptedAt   time.Time
	hardDeadline time.Time
	wake         chan struct{}
}

type admissionSnapshot struct {
	bound      bool
	closed     bool
	binding    binding
	revision   int64
	deadline   time.Time
	requestKey string
	acceptedAt time.Time
}

func (a *admissionState) snapshot() admissionSnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	return admissionSnapshot{
		bound: a.bound, closed: a.closed, binding: a.binding,
		revision: a.revision, deadline: a.deadline,
		requestKey: a.requestKey, acceptedAt: a.acceptedAt,
	}
}

// install rechecks the deadline after the durable write so an admission that
// lost a race with expiry is never reported as active.
func (a *admissionState) install(rec admissionRecord) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || !time.Now().Before(a.deadline) {
		return false
	}
	a.bound = true
	a.binding = rec.Binding
	a.revision = rec.Revision
	a.deadline = rec.Deadline
	a.requestKey = rec.RequestKey
	a.acceptedAt = rec.AcceptedAt
	select {
	case a.wake <- struct{}{}:
	default:
	}
	return true
}

func (a *admissionState) close() {
	a.mu.Lock()
	a.closed = true
	a.mu.Unlock()
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

func (a *admissionState) activeDeadline() (time.Time, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.bound || a.closed || !time.Now().Before(a.deadline) {
		return time.Time{}, false
	}
	return a.deadline, true
}

// watchDeadline does not take the ledger or HTTP locks. A renewal only updates
// the in-memory deadline; the timer re-reads it after every wake-up.
func (b *boundary) watchDeadline() {
	a := b.admission
	for {
		a.mu.Lock()
		closed := a.closed
		deadline := a.deadline
		a.mu.Unlock()
		if closed {
			log.Printf("admission closed; exiting the entire instance")
			os.Exit(0)
		}
		wait := time.Until(deadline)
		if wait <= 0 {
			a.mu.Lock()
			if a.closed || !time.Now().Before(a.deadline) {
				a.closed = true
				a.mu.Unlock()
				log.Printf("admission deadline reached; closing the entire instance")
				os.Exit(0)
			}
			a.mu.Unlock()
			continue
		}
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
			a.mu.Lock()
			if a.closed || !time.Now().Before(a.deadline) {
				a.closed = true
				a.mu.Unlock()
				log.Printf("admission deadline reached; closing the entire instance")
				os.Exit(0)
			}
			a.mu.Unlock()
		case <-a.wake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}
	}
}

type operationRecord struct {
	OperationID       string     `json:"operationId"`
	Kind              string     `json:"kind"`
	PayloadHash       string     `json:"payloadHash"`
	State             string     `json:"state"`
	AcceptedAt        time.Time  `json:"acceptedAt"`
	DispatchStartedAt *time.Time `json:"dispatchStartedAt,omitempty"`
	FinishedAt        *time.Time `json:"finishedAt,omitempty"`
	HTTPStatus        int        `json:"httpStatus,omitempty"`
	ContentType       string     `json:"contentType,omitempty"`
	Output            string     `json:"output,omitempty"`
	Truncated         bool       `json:"truncated,omitempty"`
	Error             string     `json:"error,omitempty"`
}

type ledger struct {
	mu         sync.Mutex
	records    map[string]*operationRecord
	opLocks    sync.Map
	failWrites bool
}

func newLedger() *ledger {
	return &ledger{
		records:    make(map[string]*operationRecord),
		failWrites: os.Getenv("GUARD_PROBE_FAIL_RECEIPTS") == "1",
	}
}

func (l *ledger) lockFor(id string) *sync.Mutex {
	value, _ := l.opLocks.LoadOrStore(id, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func (l *ledger) get(id string) (operationRecord, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	rec, ok := l.records[id]
	if !ok {
		return operationRecord{}, false
	}
	return *rec, true
}

func (l *ledger) persist(rec *operationRecord) error {
	cached := *rec
	if err := l.write(&cached); err != nil {
		return err
	}
	l.mu.Lock()
	l.records[rec.OperationID] = &cached
	l.mu.Unlock()
	return nil
}

func (l *ledger) cache(rec *operationRecord) {
	cached := *rec
	l.mu.Lock()
	l.records[rec.OperationID] = &cached
	l.mu.Unlock()
}

func (l *ledger) write(rec *operationRecord) error {
	if l.failWrites {
		return errors.New("probe-injected receipt storage failure")
	}
	name := sha256.Sum256([]byte(rec.OperationID))
	return writeJSONAtomic(filepath.Join(operationsDir, hex.EncodeToString(name[:])+".json"), rec)
}

func loadLedger() (*ledger, error) {
	if err := os.MkdirAll(operationsDir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(operationsDir, 0o700); err != nil {
		return nil, err
	}
	l := newLedger()
	entries, err := os.ReadDir(operationsDir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(operationsDir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var rec operationRecord
		if err := json.Unmarshal(data, &rec); err != nil {
			return nil, fmt.Errorf("invalid receipt %s: %w", entry.Name(), err)
		}
		if rec.OperationID == "" {
			return nil, fmt.Errorf("receipt %s has no operation id", entry.Name())
		}
		if rec.State == statePending || rec.State == stateDispatchStarted {
			finished := time.Now()
			rec.State = stateUnknown
			rec.FinishedAt = &finished
			rec.Error = "guard restarted before terminal receipt"
			if err := l.persist(&rec); err != nil {
				l.cache(&rec)
			}
			continue
		}
		l.records[rec.OperationID] = &rec
	}
	return l, nil
}

func writeJSONAtomic(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	temp := path + ".tmp"
	file, err := os.OpenFile(temp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		os.Remove(temp)
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		os.Remove(temp)
		return err
	}
	if err := file.Close(); err != nil {
		os.Remove(temp)
		return err
	}
	if err := os.Rename(temp, path); err != nil {
		os.Remove(temp)
		return err
	}
	return syncDir(filepath.Dir(path))
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

type preparedDispatch struct {
	path        string
	contentType string
	body        []byte
}

type boundary struct {
	admission  *admissionState
	ledger     *ledger
	execdPID   int
	client     *http.Client
	admitMutex sync.Mutex
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (b *boundary) health(w http.ResponseWriter, _ *http.Request) {
	snapshot := b.admission.snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"closed":       snapshot.closed || !time.Now().Before(snapshot.deadline),
		"bound":        snapshot.bound,
		"deadline":     snapshot.deadline,
		"hardDeadline": b.admission.hardDeadline,
		"revision":     snapshot.revision,
		"execdPID":     b.execdPID,
		"observedAt":   time.Now(),
	})
}

func (b *boundary) admit(w http.ResponseWriter, r *http.Request) {
	var request admissionRequest
	if err := decode(w, r, &request); err != nil {
		http.Error(w, "invalid admission request", http.StatusBadRequest)
		return
	}
	next := request.binding()
	if !next.valid() || request.Revision < 1 || request.RequestKey == "" {
		http.Error(w, "admission requires full binding, revision and request key", http.StatusBadRequest)
		return
	}
	now := time.Now()
	if !request.Deadline.After(now) {
		http.Error(w, "deadline must be in the future", http.StatusBadRequest)
		return
	}
	if request.Deadline.After(b.admission.hardDeadline) {
		http.Error(w, "deadline exceeds instance hard lifetime", http.StatusConflict)
		return
	}

	b.admitMutex.Lock()
	defer b.admitMutex.Unlock()
	now = time.Now()
	current := b.admission.snapshot()
	if current.closed || !now.Before(current.deadline) {
		http.Error(w, "admission is closed", http.StatusConflict)
		return
	}
	replayed := false
	switch {
	case !current.bound:
	case next != current.binding:
		http.Error(w, "binding does not match the active admission", http.StatusConflict)
		return
	case request.Revision < current.revision:
		http.Error(w, "stale admission revision", http.StatusConflict)
		return
	case request.Revision == current.revision:
		if request.RequestKey != current.requestKey || !request.Deadline.Equal(current.deadline) {
			http.Error(w, "same revision with different parameters", http.StatusConflict)
			return
		}
		replayed = true
	default:
		if request.Deadline.Before(current.deadline) {
			http.Error(w, "renewal must not shorten the accepted deadline", http.StatusConflict)
			return
		}
	}

	record := admissionRecord{
		Binding: next, Revision: request.Revision, Deadline: request.Deadline,
		RequestKey: request.RequestKey, AcceptedAt: now,
	}
	if replayed {
		record = admissionRecord{
			Binding: current.binding, Revision: current.revision,
			Deadline: current.deadline, RequestKey: current.requestKey,
			AcceptedAt: current.acceptedAt,
		}
	}
	if !replayed {
		if err := writeJSONAtomic(admissionFile, record); err != nil {
			b.admission.close()
			time.AfterFunc(time.Second, func() { os.Exit(0) })
			http.Error(
				w,
				"admission receipt is not durable; closing the binding",
				http.StatusInternalServerError,
			)
			return
		}
		if !b.admission.install(record) {
			http.Error(w, "admission expired while the receipt was written", http.StatusConflict)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"accepted": true, "replayed": replayed,
		"revision": record.Revision, "deadline": record.Deadline,
		"bound": true, "closed": false, "observedAt": time.Now(),
	})
}

func (b *boundary) readAdmission(w http.ResponseWriter, r *http.Request) {
	var request struct {
		SandboxID string `json:"sandboxId"`
		RunID     string `json:"runId"`
		WorkerID  string `json:"workerId"`
		Epoch     int64  `json:"epoch"`
		LeaseID   string `json:"leaseId"`
	}
	if err := decode(w, r, &request); err != nil {
		http.Error(w, "invalid read request", http.StatusBadRequest)
		return
	}
	current := b.admission.snapshot()
	if request.SandboxID != "" || request.RunID != "" || request.WorkerID != "" || request.Epoch != 0 ||
		request.LeaseID != "" {
		if request.SandboxID != current.binding.SandboxID || request.RunID != current.binding.RunID ||
			request.WorkerID != current.binding.WorkerID || request.Epoch != current.binding.Epoch ||
			request.LeaseID != current.binding.LeaseID {
			http.Error(w, "binding does not match the active admission", http.StatusConflict)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"bound": current.bound, "closed": current.closed,
		"binding": current.binding, "revision": current.revision,
		"deadline": current.deadline, "requestKey": current.requestKey,
		"acceptedAt": current.acceptedAt, "observedAt": time.Now(),
	})
}

func payloadHash(kind string, fields ...string) string {
	hash := sha256.New()
	_, _ = io.WriteString(hash, kind)
	for _, field := range fields {
		_, _ = hash.Write([]byte{0})
		_, _ = io.WriteString(hash, field)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func prepareCommand(script string) (*preparedDispatch, error) {
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(map[string]any{
		"command": script,
		"cwd":     "/workspace",
		"uid":     65534,
		"gid":     65534,
		"timeout": 10000,
		"envs":    map[string]string{"HOME": "/workspace", "PATH": "/usr/local/bin:/usr/bin:/bin"},
	}); err != nil {
		return nil, err
	}
	return &preparedDispatch{path: "/command", contentType: "application/json", body: body.Bytes()}, nil
}

func prepareFile(content string) (*preparedDispatch, error) {
	var body bytes.Buffer
	m := multipart.NewWriter(&body)
	meta, err := m.CreateFormFile("metadata", "metadata.json")
	if err != nil {
		return nil, err
	}
	if _, err := io.WriteString(meta, `{"path":"/workspace/guard-file","permission":{"mode":420}}`); err != nil {
		return nil, err
	}
	file, err := m.CreateFormFile("file", "guard-file")
	if err != nil {
		return nil, err
	}
	if _, err := io.WriteString(file, content); err != nil {
		return nil, err
	}
	if err := m.Close(); err != nil {
		return nil, err
	}
	return &preparedDispatch{
		path:        "/v1/filesystem/65534/65534/files/upload",
		contentType: m.FormDataContentType(), body: body.Bytes(),
	}, nil
}

func (b *boundary) handleCommand(w http.ResponseWriter, r *http.Request) {
	var request struct {
		OperationID string `json:"operationId"`
		Script      string `json:"script"`
	}
	if err := decode(w, r, &request); err != nil {
		http.Error(w, "invalid command request", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(request.Script) == "" {
		http.Error(w, "expected non-empty script", http.StatusBadRequest)
		return
	}
	dispatch, err := prepareCommand(request.Script)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	b.execute(w, "command", request.OperationID, payloadHash("command", request.Script), dispatch)
}

func (b *boundary) handleFileWrite(w http.ResponseWriter, r *http.Request) {
	var request struct {
		OperationID string `json:"operationId"`
		Content     string `json:"content"`
	}
	if err := decode(w, r, &request); err != nil {
		http.Error(w, "invalid file write request", http.StatusBadRequest)
		return
	}
	dispatch, err := prepareFile(request.Content)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	b.execute(w, "file-write", request.OperationID, payloadHash("file-write", request.Content), dispatch)
}

func (b *boundary) execute(w http.ResponseWriter, kind, operationID, hash string, dispatch *preparedDispatch) {
	if !operationIDPattern.MatchString(operationID) {
		http.Error(w, "operationId must be 1-64 safe characters", http.StatusBadRequest)
		return
	}
	if _, ok := b.admission.activeDeadline(); !ok {
		http.Error(w, "admission is not active; execution cannot resume", http.StatusConflict)
		return
	}
	lock := b.ledger.lockFor(operationID)
	lock.Lock()
	defer lock.Unlock()

	record, exists := b.ledger.get(operationID)
	if exists {
		if record.Kind != kind || record.PayloadHash != hash {
			http.Error(w, "operationId reused with different parameters", http.StatusConflict)
			return
		}
		switch record.State {
		case stateSucceeded, stateFailed:
			b.replay(w, record)
			return
		case stateUnknown:
			http.Error(w, "operation outcome unknown; query the original operation", http.StatusBadGateway)
			return
		case stateDispatchStarted:
			record.State = stateUnknown
			record.Error = "dispatch started without a terminal receipt"
			finished := time.Now()
			record.FinishedAt = &finished
			_ = b.ledger.persist(&record)
			http.Error(w, "operation outcome unknown; query the original operation", http.StatusBadGateway)
			return
		}
	} else {
		record = operationRecord{
			OperationID: operationID,
			Kind:        kind,
			PayloadHash: hash,
			AcceptedAt:  time.Now(),
		}
	}

	record.Kind = kind
	record.PayloadHash = hash
	record.State = statePending
	if record.AcceptedAt.IsZero() {
		record.AcceptedAt = time.Now()
	}
	if err := b.ledger.persist(&record); err != nil {
		http.Error(w, "operation receipt is not durable; not dispatched", http.StatusInternalServerError)
		return
	}
	record.State = stateDispatchStarted
	started := time.Now()
	record.DispatchStartedAt = &started
	if err := b.ledger.persist(&record); err != nil {
		http.Error(w, "dispatch receipt is not durable; not dispatched", http.StatusInternalServerError)
		return
	}

	deadline, ok := b.admission.activeDeadline()
	if !ok {
		record.State = stateFailed
		record.Error = "expired before dispatch"
		finished := time.Now()
		record.FinishedAt = &finished
		_ = b.ledger.persist(&record)
		http.Error(w, "admission expired before dispatch", http.StatusConflict)
		return
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		"http://execd"+dispatch.path,
		bytes.NewReader(dispatch.body),
	)
	if err != nil {
		b.finishUnknown(&record, "build dispatch request: "+err.Error())
		http.Error(w, "operation outcome unknown; query the original operation", http.StatusBadGateway)
		return
	}
	request.Header.Set("Content-Type", dispatch.contentType)
	response, err := b.client.Do(request)
	if err != nil {
		b.finishUnknown(&record, "dispatch outcome unknown: "+err.Error())
		http.Error(w, "operation outcome unknown; query the original operation", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	data, truncated, err := readBounded(response.Body)
	if err != nil {
		b.finishUnknown(&record, "incomplete dispatch response: "+err.Error())
		http.Error(w, "operation outcome unknown; query the original operation", http.StatusBadGateway)
		return
	}
	record.HTTPStatus = response.StatusCode
	record.ContentType = response.Header.Get("Content-Type")
	record.Output = string(data)
	record.Truncated = truncated
	record.Error = ""
	finished := time.Now()
	record.FinishedAt = &finished
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		record.State = stateSucceeded
	} else {
		record.State = stateFailed
	}
	if err := b.ledger.persist(&record); err != nil {
		record.State = stateUnknown
		record.Error = "terminal receipt is not durable: " + err.Error()
		b.ledger.cache(&record)
		http.Error(w, "terminal receipt is not durable; outcome unknown", http.StatusInternalServerError)
		return
	}
	b.replay(w, record)
}

func (b *boundary) finishUnknown(record *operationRecord, message string) {
	record.State = stateUnknown
	record.Error = message
	finished := time.Now()
	record.FinishedAt = &finished
	if err := b.ledger.persist(record); err != nil {
		record.Error = message + "; receipt also failed: " + err.Error()
		b.ledger.cache(record)
	}
}

func (b *boundary) replay(w http.ResponseWriter, record operationRecord) {
	if record.ContentType != "" {
		w.Header().Set("Content-Type", record.ContentType)
	}
	if record.Truncated {
		w.Header().Set("X-Probe-Output-Truncated", "true")
	}
	w.WriteHeader(record.HTTPStatus)
	_, _ = io.WriteString(w, record.Output)
}

func (b *boundary) readOperation(w http.ResponseWriter, r *http.Request) {
	var request struct {
		OperationID string `json:"operationId"`
	}
	if err := decode(w, r, &request); err != nil || !operationIDPattern.MatchString(request.OperationID) {
		http.Error(w, "expected a valid operationId", http.StatusBadRequest)
		return
	}
	record, ok := b.ledger.get(request.OperationID)
	if !ok {
		http.Error(w, "operation not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, record)
}

func readBounded(reader io.Reader) ([]byte, bool, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxOutputSize+1))
	if err != nil {
		return nil, false, err
	}
	if len(data) > maxOutputSize {
		return data[:maxOutputSize], true, nil
	}
	return data, false, nil
}

func (b *boundary) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	switch r.URL.Path {
	case "/health":
		b.health(w, r)
	case "/admission":
		b.admit(w, r)
	case "/admission/read":
		b.readAdmission(w, r)
	case "/stop":
		var request struct {
			PodUID string `json:"podUid"`
		}
		if err := decode(w, r, &request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if request.PodUID == "" || request.PodUID != os.Getenv("SANDBOX_POD_UID") {
			http.Error(w, "pod uid mismatch", http.StatusConflict)
			return
		}
		b.admission.close()
		w.WriteHeader(http.StatusOK)
	case "/operation":
		b.readOperation(w, r)
	case "/command":
		b.handleCommand(w, r)
	case "/file-write":
		b.handleFileWrite(w, r)
	default:
		http.NotFound(w, r)
	}
}

func loadAdmission() (admissionRecord, bool, error) {
	data, err := os.ReadFile(admissionFile)
	if errors.Is(err, os.ErrNotExist) {
		return admissionRecord{}, false, nil
	}
	if err != nil {
		return admissionRecord{}, false, err
	}
	var record admissionRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return admissionRecord{}, false, fmt.Errorf("invalid admission receipt: %w", err)
	}
	return record, true, nil
}

func serve(seconds int) error {
	if os.Getpid() != 1 || os.Geteuid() != 0 {
		return errors.New("probe must be root PID 1 in a private PID namespace")
	}
	if seconds < 1 || seconds > 86400 {
		return errors.New("hard lifetime must be between 1 and 86400 seconds")
	}
	info, err := os.Lstat(stateDir)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm()&0o077 != 0 || stat.Uid != 0 {
		return errors.New("state must be a root-owned private directory")
	}
	lock, err := os.OpenFile(stateDir+"/guard.lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return err
	}
	hardDeadline := time.Now().Add(time.Duration(seconds) * time.Second)
	marker, err := os.OpenFile(stateDir+"/boot-started", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	firstBoot := err == nil
	if err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if firstBoot {
		if err := marker.Sync(); err != nil {
			marker.Close()
			return err
		}
		if err := marker.Close(); err != nil {
			return err
		}
		if err := syncDir(stateDir); err != nil {
			return err
		}
	}
	admission, admissionExists, err := loadAdmission()
	if err != nil {
		return err
	}
	ledger, err := loadLedger()
	if err != nil {
		return err
	}
	state := &admissionState{
		hardDeadline: hardDeadline,
		deadline:     hardDeadline,
		wake:         make(chan struct{}, 1),
	}
	if admissionExists {
		state.bound = true
		state.binding = admission.Binding
		state.revision = admission.Revision
		state.deadline = admission.Deadline
		state.requestKey = admission.RequestKey
		state.acceptedAt = admission.AcceptedAt
	}
	if !firstBoot || admissionExists {
		state.closed = true
	}
	b := &boundary{
		admission: state, ledger: ledger,
		client: socketClient(execdSocket, 15*time.Second),
	}
	defer b.client.CloseIdleConnections()
	if !state.closed {
		cmd := exec.Command("/execd", "--unix-socket", execdSocket)
		cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/tmp", "TMPDIR=/tmp"}
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			return err
		}
		b.execdPID = cmd.Process.Pid
		go func() {
			err := cmd.Wait()
			log.Printf("execd exited (%v); closing the entire instance", err)
			os.Exit(1)
		}()
		readyUntil := time.Now().Add(8 * time.Second)
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://execd/ready", nil)
			resp, err := b.client.Do(req)
			cancel()
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					break
				}
			}
			if !time.Now().Before(readyUntil) {
				return errors.New("execd readiness deadline exceeded")
			}
			time.Sleep(50 * time.Millisecond)
		}
		go b.watchDeadline()
	}
	if info, err := os.Lstat(guardSocket); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("refuse to replace non-socket control path")
		}
		if err := os.Remove(guardSocket); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := net.Listen("unix", guardSocket)
	if err != nil {
		return err
	}
	defer listener.Close()
	if err := os.Chmod(guardSocket, 0o600); err != nil {
		return err
	}
	return (&http.Server{Handler: http.HandlerFunc(b.serve), ReadHeaderTimeout: time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: 20 * time.Second}).Serve(
		listener,
	)
}

func main() {
	if len(os.Args) == 3 && os.Args[1] == "serve" {
		seconds, err := strconv.Atoi(os.Args[2])
		if err == nil {
			err = serve(seconds)
		}
		log.Fatal(err)
	}
	if len(os.Args) != 4 || os.Args[1] != "call" {
		log.Fatal("usage: guard serve SECONDS | guard call /path JSON")
	}
	client := socketClient(guardSocket, 18*time.Second)
	defer client.CloseIdleConnections()
	resp, err := client.Post("http://guard"+os.Args[2], "application/json", strings.NewReader(os.Args[3]))
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxOutputSize+1))
	if err != nil || len(data) > maxOutputSize {
		log.Fatal("incomplete control response")
	}
	fmt.Printf("%d %s", resp.StatusCode, data)
	if resp.StatusCode >= 400 {
		os.Exit(1)
	}
}

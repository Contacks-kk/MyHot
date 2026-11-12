package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type memoryRepository struct {
	nextID int64
	runs   map[string][]runRecord
	last   json.RawMessage
}

type runRecord struct {
	id      int64
	status  string
	started time.Time
	detail  json.RawMessage
	err     string
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{runs: map[string][]runRecord{}}
}

func (r *memoryRepository) HasRun(_ context.Context, start, end time.Time) (bool, error) {
	for _, run := range r.runs[runDay(start)] {
		if run.started.Before(end) && (run.status != "running" || time.Since(run.started) < stuckRunGrace) {
			return true, nil
		}
	}
	return false, nil
}

func (r *memoryRepository) BeginRun(_ context.Context, start, _ time.Time) (int64, bool, error) {
	for _, run := range r.runs[runDay(start)] {
		if run.status == "ok" || (run.status == "running" && time.Since(run.started) < stuckRunGrace) {
			return 0, false, nil
		}
	}
	r.nextID++
	r.runs[runDay(start)] = append(r.runs[runDay(start)], runRecord{id: r.nextID, status: "running", started: time.Now()})
	return r.nextID, true, nil
}

func (r *memoryRepository) CompleteRun(_ context.Context, id int64, detail json.RawMessage) error {
	return r.update(id, func(run *runRecord) { run.status, run.detail = "ok", detail })
}

func (r *memoryRepository) FailRun(_ context.Context, id int64, message string) error {
	return r.update(id, func(run *runRecord) { run.status, run.err = "failed", message })
}

func (r *memoryRepository) SaveLast(_ context.Context, value json.RawMessage) error {
	r.last = append([]byte(nil), value...)
	return nil
}

func (r *memoryRepository) update(id int64, update func(*runRecord)) error {
	for key, runs := range r.runs {
		for i := range runs {
			if runs[i].id == id {
				update(&runs[i])
				r.runs[key] = runs
				return nil
			}
		}
	}
	return errors.New("job run not found")
}

func runDay(value time.Time) string {
	return value.Format("2006-01-02")
}

type fakeCommands struct {
	tarFailures int
	calls       []string
}

type realTarCommands struct{ database *fakeCommands }

func (c realTarCommands) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name == "tar" {
		return (ExecRunner{}).Run(ctx, name, args...)
	}
	return c.database.Run(ctx, name, args...)
}

func (c *fakeCommands) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	c.calls = append(c.calls, name+" "+strings.Join(args, " "))
	switch name {
	case "pg_dump":
		index := indexOf(args, "--file")
		if index < 0 || index+1 >= len(args) {
			return nil, errors.New("missing pg_dump target")
		}
		return nil, os.WriteFile(args[index+1], []byte("database archive fixture"), 0o600)
	case "pg_restore":
		return nil, nil
	case "tar":
		if c.tarFailures > 0 {
			c.tarFailures--
			return nil, errors.New("fictional packing failure")
		}
		return nil, os.WriteFile(args[1], []byte("file archive fixture"), 0o600)
	default:
		return nil, fmt.Errorf("unexpected command %q", name)
	}
}

func indexOf(values []string, value string) int {
	for i, item := range values {
		if item == value {
			return i
		}
	}
	return -1
}

type capturedUpload struct {
	path string
	body []byte
}

type uploadTransport struct {
	items  []capturedUpload
	status int
}

func (t *uploadTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(body)
	if request.Header.Get("x-amz-content-sha256") != hex.EncodeToString(hash[:]) {
		return nil, errors.New("uploaded payload hash did not match")
	}
	if request.Header.Get("authorization") == "" || request.Header.Get("x-amz-date") == "" {
		return nil, errors.New("upload request was not signed")
	}
	status := t.status
	if status == 0 {
		status = http.StatusOK
	}
	t.items = append(t.items, capturedUpload{path: request.URL.Path, body: body})
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("fixture error")), Request: request}, nil
}

func newTestService(t *testing.T, repository *memoryRepository, commands *fakeCommands, transport *uploadTransport, forwarded bool) *Service {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "uploads", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "uploads", "nested", "fixture.bin"), []byte("uploaded fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "feedback-screenshots"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "feedback-screenshots", "fixture.png"), []byte("feedback fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: transport}
	return NewServiceWithDependencies(Config{
		DatabaseURL: "postgres://user:password@localhost:5432/fixture_db_test", DatabaseName: "fixture_db_test", DataDir: dir,
		Store:                Store{SecretID: "fixture-id", SecretKey: "fixture-secret", Bucket: "fixture-bucket", Region: "fixture-region", Domain: "backup.invalid"},
		ScreenshotsForwarded: forwarded,
	}, repository, commands, client)
}

func TestDueUsesBeijingTimeAndOneRunPerDate(t *testing.T) {
	repository := newMemoryRepository()
	service := NewServiceWithDependencies(Config{}, repository, nil, nil)
	before := time.Date(2026, 11, 1, 20, 9, 0, 0, time.UTC)
	if _, due, err := service.Due(context.Background(), before); err != nil || due {
		t.Fatalf("backup became due before 04:10 Beijing: due=%t err=%v", due, err)
	}
	date, due, err := service.Due(context.Background(), before.Add(time.Minute))
	if err != nil || !due || date != "2026-11-02" {
		t.Fatalf("backup due at the Beijing slot: date=%q due=%t err=%v", date, due, err)
	}
	repository.runs[date] = []runRecord{{status: "ok", started: time.Now()}}
	if _, due, err := service.Due(context.Background(), before.Add(time.Minute)); err != nil || due {
		t.Fatalf("completed daily backup was scheduled again: due=%t err=%v", due, err)
	}
}

func TestRunUploadsRestorableInputsToDailyWeeklyAndMonthlyPrefixes(t *testing.T) {
	repository := newMemoryRepository()
	commands := &fakeCommands{}
	transport := &uploadTransport{}
	service := newTestService(t, repository, commands, transport, false)
	now := time.Date(2026, 11, 1, 4, 0, 12, 345000000, time.UTC)
	if err := service.Run(context.Background(), "2026-11-01", now); err != nil {
		t.Fatal(err)
	}
	if len(transport.items) != 6 {
		t.Fatalf("monthly Sunday backup uploaded %d objects, want 6", len(transport.items))
	}
	wantPaths := []string{
		"/daily/fixture-db-test-202611010400.dump", "/weekly/fixture-db-test-202611010400.dump", "/monthly/fixture-db-test-202611010400.dump",
		"/daily/fixture-db-test-files-202611010400.tar.gz", "/weekly/fixture-db-test-files-202611010400.tar.gz", "/monthly/fixture-db-test-files-202611010400.tar.gz",
	}
	got := map[string]bool{}
	for _, item := range transport.items {
		got[item.path] = true
	}
	for _, path := range wantPaths {
		if !got[path] {
			t.Errorf("backup object %s was not uploaded; got %v", path, got)
		}
	}
	if len(repository.runs["2026-11-01"]) != 1 || repository.runs["2026-11-01"][0].status != "ok" {
		t.Fatalf("backup run was not audited as successful: %#v", repository.runs)
	}
	var summary Summary
	if err := json.Unmarshal(repository.last, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.At != "2026-11-01T04:00:12.345Z" || !summary.Uploaded || len(summary.Objects) != 6 {
		t.Fatalf("stored backup summary changed: %#v", summary)
	}
	archiveCall := "tar -czf"
	if len(commands.calls) != 3 || !strings.HasPrefix(commands.calls[2], archiveCall) {
		t.Fatalf("unexpected backup command sequence: %#v", commands.calls)
	}
}

func TestRunRetriesFilePackingAndStoresIncompleteBackupWithoutUploadingFiles(t *testing.T) {
	repository := newMemoryRepository()
	commands := &fakeCommands{tarFailures: 2}
	transport := &uploadTransport{}
	service := newTestService(t, repository, commands, transport, false)
	now := time.Date(2026, 11, 2, 4, 0, 0, 0, time.UTC)
	err := service.Run(context.Background(), "2026-11-02", now)
	if err == nil || !strings.Contains(err.Error(), "database backed up, but the file archive failed: fictional packing failure") {
		t.Fatalf("persistent file packing failure was lost: %v", err)
	}
	if len(commands.calls) != 4 || len(transport.items) != 1 || !strings.HasSuffix(transport.items[0].path, ".dump") {
		t.Fatalf("file failure must retry once then ship only database dump: calls=%#v uploads=%#v", commands.calls, transport.items)
	}
	var summary Summary
	if err := json.Unmarshal(repository.last, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Uploaded || !strings.Contains(summary.FilesError, "fictional packing failure") || len(summary.Objects) != 1 {
		t.Fatalf("incomplete backup state was not recorded: %#v", summary)
	}
	if repository.runs["2026-11-02"][0].status != "failed" {
		t.Fatalf("incomplete backup run was not marked failed: %#v", repository.runs)
	}
}

func TestForwardedScreenshotsAreExcludedButUploadsRemain(t *testing.T) {
	repository := newMemoryRepository()
	commands := &fakeCommands{}
	transport := &uploadTransport{}
	service := newTestService(t, repository, commands, transport, true)
	if err := service.Run(context.Background(), "2026-11-02", time.Date(2026, 11, 2, 4, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(commands.calls[2], "uploads") || strings.Contains(commands.calls[2], "feedback-screenshots") {
		t.Fatalf("archive source roots do not match Feishu forwarding state: %s", commands.calls[2])
	}
}

func TestUploadedFileArchiveRestoresAttachmentDirectoriesAndBytes(t *testing.T) {
	if _, err := exec.LookPath("tar"); err != nil {
		t.Skip("tar is not installed")
	}
	repository := newMemoryRepository()
	database := &fakeCommands{}
	transport := &uploadTransport{}
	service := newTestService(t, repository, database, transport, false)
	now := time.Date(2026, 11, 2, 4, 0, 0, 0, time.UTC)
	service.commands = realTarCommands{database: database}
	if err := service.Run(context.Background(), "2026-11-02", now); err != nil {
		t.Fatal(err)
	}
	var archive []byte
	for _, item := range transport.items {
		if item.path == "/daily/fixture-db-test-files-202611020400.tar.gz" {
			archive = item.body
			break
		}
	}
	if len(archive) == 0 {
		t.Fatal("file archive was not uploaded")
	}
	root := t.TempDir()
	archivePath := filepath.Join(root, "files.tar.gz")
	if err := os.WriteFile(archivePath, archive, 0o600); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(root, "restored")
	if err := os.Mkdir(restored, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := (ExecRunner{}).Run(context.Background(), "tar", "-xzf", archivePath, "-C", restored); err != nil {
		t.Fatalf("archive did not restore: %v", err)
	}
	for path, expected := range map[string]string{
		filepath.Join(restored, "uploads", "nested", "fixture.bin"):    "uploaded fixture",
		filepath.Join(restored, "feedback-screenshots", "fixture.png"): "feedback fixture",
	} {
		actual, err := os.ReadFile(path)
		if err != nil || string(actual) != expected {
			t.Errorf("restored file %s = %q, %v", path, actual, err)
		}
	}
}

func TestLocalRetentionKeepsThreeNewestDumpsAndFileArchives(t *testing.T) {
	repository := newMemoryRepository()
	commands := &fakeCommands{}
	transport := &uploadTransport{}
	service := newTestService(t, repository, commands, transport, false)
	for day := 1; day <= 4; day++ {
		date := fmt.Sprintf("2026-11-%02d", day)
		now := time.Date(2026, 11, day, 4, 0, 0, 0, time.UTC)
		if err := service.Run(context.Background(), date, now); err != nil {
			t.Fatal(err)
		}
	}
	backups, err := os.ReadDir(filepath.Join(service.cfg.DataDir, "backups"))
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 6 {
		var names []string
		for _, entry := range backups {
			names = append(names, entry.Name())
		}
		t.Fatalf("local backup retention kept %d files, want newest 3 pairs: %v", len(backups), names)
	}
}

func TestSignV4ReturnsDeterministicSignedHeaders(t *testing.T) {
	target, err := url.Parse("https://fixture-bucket.cos.fixture-region.myqcloud.com/daily/fixture.dump")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC)
	headers := map[string]string{"content-length": "4", "content-type": "application/octet-stream"}
	first := SignV4("PUT", target, "fixture-region", "s3", "access", "secret", "abcd", headers, now)
	second := SignV4("PUT", target, "fixture-region", "s3", "access", "secret", "abcd", headers, now)
	if first["authorization"] == "" || first["authorization"] != second["authorization"] || first["x-amz-date"] != "20261101T040000Z" {
		t.Fatalf("Signature V4 output was absent or non-deterministic: %#v", first)
	}
	if !strings.Contains(first["authorization"], "SignedHeaders=content-length;content-type;host;x-amz-content-sha256;x-amz-date") {
		t.Fatalf("signed header list changed: %s", first["authorization"])
	}
}

func TestDatabaseNameUsesEscapedURLPath(t *testing.T) {
	if got := DatabaseName("postgres://u:p@localhost:5432/news%2Ddb_test?sslmode=disable"); got != "news-db_test" {
		t.Fatalf("database name = %q", got)
	}
}

func TestUploadFailureDoesNotOverwriteLastBackupState(t *testing.T) {
	repository := newMemoryRepository()
	transport := &uploadTransport{status: http.StatusBadGateway}
	service := newTestService(t, repository, &fakeCommands{}, transport, false)
	err := service.Run(context.Background(), "2026-11-02", time.Date(2026, 11, 2, 4, 0, 0, 0, time.UTC))
	if err == nil || !strings.Contains(err.Error(), "HTTP 502 fixture error") {
		t.Fatalf("upload failure was not reported: %v", err)
	}
	if len(repository.last) != 0 || repository.runs["2026-11-02"][0].status != "failed" {
		t.Fatalf("failed upload overwrote success state or was not audited: last=%s runs=%#v", repository.last, repository.runs)
	}
}

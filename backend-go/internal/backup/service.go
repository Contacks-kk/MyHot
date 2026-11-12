package backup

import (
	"context"
	"crypto/hmac"
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
	"regexp"
	"sort"
	"strings"
	"time"
)

const JobName = "ops.backup"
const stuckRunGrace = 3 * time.Minute

type Store struct {
	SecretID  string
	SecretKey string
	Bucket    string
	Region    string
	Domain    string
}

func (s Store) Configured() bool {
	return strings.TrimSpace(s.SecretID) != "" && strings.TrimSpace(s.SecretKey) != "" && strings.TrimSpace(s.Bucket) != "" && strings.TrimSpace(s.Region) != ""
}

func (s Store) Endpoint() string {
	if domain := strings.TrimSpace(s.Domain); domain != "" {
		return "https://" + strings.TrimPrefix(strings.TrimPrefix(domain, "https://"), "http://")
	}
	return "https://" + s.Bucket + ".cos." + s.Region + ".myqcloud.com"
}

type Config struct {
	DatabaseURL          string
	DatabaseName         string
	DataDir              string
	Store                Store
	ScreenshotsForwarded bool
}

type Summary struct {
	At         string   `json:"at"`
	Uploaded   bool     `json:"uploaded"`
	Objects    []Object `json:"objects"`
	FilesError string   `json:"filesError,omitempty"`
}

type Object struct {
	Key    string `json:"key"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type Repository interface {
	HasRun(context.Context, time.Time, time.Time) (bool, error)
	BeginRun(context.Context, time.Time, time.Time) (int64, bool, error)
	CompleteRun(context.Context, int64, json.RawMessage) error
	FailRun(context.Context, int64, string) error
	SaveLast(context.Context, json.RawMessage) error
}

type CommandRunner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	var stderr strings.Builder
	command.Stderr = &stderr
	stdout, err := command.Output()
	if err != nil {
		return nil, commandFailure{stderr: stderr.String(), err: err}
	}
	return stdout, nil
}

type Service struct {
	cfg      Config
	repo     Repository
	commands CommandRunner
	client   *http.Client
}

func NewService(cfg Config, repository Repository) *Service {
	return NewServiceWithDependencies(cfg, repository, ExecRunner{}, &http.Client{Timeout: time.Hour})
}

func NewServiceWithDependencies(cfg Config, repository Repository, commands CommandRunner, client *http.Client) *Service {
	if commands == nil {
		commands = ExecRunner{}
	}
	if client == nil {
		client = &http.Client{Timeout: time.Hour}
	}
	return &Service{cfg: cfg, repo: repository, commands: commands, client: client}
}

func (s *Service) Configured() bool { return s.cfg.Store.Configured() }

// Due reports whether today's Beijing backup has not started. The River scanner
// calls this on startup and every minute, which runs one missed slot after restart.
func (s *Service) Due(ctx context.Context, now time.Time) (string, bool, error) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return "", false, err
	}
	local := now.In(location)
	if local.Hour() < 4 || (local.Hour() == 4 && local.Minute() < 10) {
		return "", false, nil
	}
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
	done, err := s.repo.HasRun(ctx, start, start.AddDate(0, 0, 1))
	return start.Format("2006-01-02"), !done, err
}

// Run writes the same per-day audit record consumed by the operations page and alerts.
// River owns one retry after a failed run, matching pg-boss retryLimit=1.
func (s *Service) Run(ctx context.Context, date string, now time.Time) error {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return err
	}
	day, err := time.ParseInLocation("2006-01-02", date, location)
	if err != nil || day.Format("2006-01-02") != date {
		return errors.New("invalid backup date")
	}
	runID, started, err := s.repo.BeginRun(ctx, day, day.AddDate(0, 0, 1))
	if err != nil || !started {
		return err
	}
	summary, backupErr := s.create(ctx, now)
	if backupErr != nil {
		if recordErr := s.repo.FailRun(ctx, runID, backupErr.Error()); recordErr != nil {
			return fmt.Errorf("backup failed: %v; record failure: %w", backupErr, recordErr)
		}
		return backupErr
	}
	detail, err := json.Marshal(summary)
	if err != nil {
		return err
	}
	if err := s.repo.CompleteRun(ctx, runID, detail); err != nil {
		return err
	}
	return nil
}

func (s *Service) create(ctx context.Context, now time.Time) (Summary, error) {
	if strings.TrimSpace(s.cfg.DatabaseName) == "" {
		return Summary{}, errors.New("database name is missing")
	}
	if err := os.MkdirAll(filepath.Join(s.cfg.DataDir, "backups"), 0o755); err != nil {
		return Summary{}, err
	}
	stamp := now.UTC().Format("200601021504")
	name := safeDatabaseName(s.cfg.DatabaseName)
	dump := filepath.Join(s.cfg.DataDir, "backups", name+"-"+stamp+".dump")
	if _, err := s.commands.Run(ctx, "pg_dump", "--format=custom", "--compress=6", "--no-owner", "--file", dump, s.cfg.DatabaseURL); err != nil {
		return Summary{}, commandError("pg_dump", err)
	}
	if _, err := s.commands.Run(ctx, "pg_restore", "--list", dump); err != nil {
		return Summary{}, commandError("pg_restore", err)
	}

	files := filepath.Join(s.cfg.DataDir, "backups", name+"-files-"+stamp+".tar.gz")
	var keep []string
	for _, dir := range s.includedDirectories() {
		info, err := os.Stat(filepath.Join(s.cfg.DataDir, dir))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return Summary{}, err
		}
		if !info.IsDir() {
			return Summary{}, fmt.Errorf("backup %s path is not a directory", dir)
		}
		keep = append(keep, dir)
	}
	filesError := ""
	if len(keep) == 0 {
		nullInput := "/dev/null"
		if os.PathSeparator == '\\' {
			nullInput = "NUL"
		}
		if _, err := s.commands.Run(ctx, "tar", "-czf", files, "-T", nullInput); err != nil {
			return Summary{}, commandError("tar", err)
		}
	} else {
		args := append([]string{"-czf", files, "-C", s.cfg.DataDir}, keep...)
		_, packErr := s.commands.Run(ctx, "tar", args...)
		if packErr != nil {
			_, packErr = s.commands.Run(ctx, "tar", args...)
		}
		if packErr != nil {
			filesError = firstLine(commandMessage(packErr), 300)
		}
	}

	prefixes := backupPrefixes(now)
	result := Summary{At: now.UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z"), Uploaded: s.cfg.Store.Configured() && filesError == ""}
	filesToUpload := []string{dump, files}
	if filesError != "" {
		filesToUpload = []string{dump}
		result.FilesError = filesError
	}
	for _, file := range filesToUpload {
		info, err := os.Stat(file)
		if err != nil {
			return Summary{}, err
		}
		sha, err := fileSHA256(file)
		if err != nil {
			return Summary{}, err
		}
		for _, prefix := range prefixes {
			key := prefix + "/" + filepath.Base(file)
			if s.cfg.Store.Configured() {
				if err := s.upload(ctx, key, file, sha, info.Size(), now); err != nil {
					return Summary{}, err
				}
			} else {
				key = "local-only:" + filepath.Base(file)
			}
			result.Objects = append(result.Objects, Object{Key: key, Bytes: info.Size(), SHA256: sha})
		}
	}
	if err := s.keepLocalCopies(filepath.Join(s.cfg.DataDir, "backups"), name); err != nil {
		return Summary{}, err
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return Summary{}, err
	}
	if err := s.repo.SaveLast(ctx, raw); err != nil {
		return Summary{}, err
	}
	if !s.cfg.Store.Configured() {
		return result, errors.New("backup made locally but not uploaded: object store credentials are not configured")
	}
	if filesError != "" {
		return result, fmt.Errorf("database backed up, but the file archive failed: %s", filesError)
	}
	return result, nil
}

func (s *Service) includedDirectories() []string {
	if s.cfg.ScreenshotsForwarded {
		return []string{"uploads"}
	}
	return []string{"uploads", "feedback-screenshots"}
}

func (s *Service) upload(ctx context.Context, key, file, payloadHash string, size int64, now time.Time) error {
	urlValue, err := url.Parse(strings.TrimRight(s.cfg.Store.Endpoint(), "/") + "/" + key)
	if err != nil {
		return err
	}
	body, err := os.Open(file)
	if err != nil {
		return err
	}
	defer body.Close()
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, urlValue.String(), body)
	if err != nil {
		return err
	}
	request.ContentLength = size
	request.Header.Set("content-length", fmt.Sprint(size))
	request.Header.Set("content-type", "application/octet-stream")
	for name, value := range SignV4("PUT", urlValue, s.cfg.Store.Region, "s3", s.cfg.Store.SecretID, s.cfg.Store.SecretKey, payloadHash, map[string]string{
		"content-length": fmt.Sprint(size), "content-type": "application/octet-stream",
	}, now) {
		request.Header.Set(name, value)
	}
	response, err := s.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 300))
		return fmt.Errorf("backup upload %s: HTTP %d %s", key, response.StatusCode, strings.TrimSpace(string(message)))
	}
	return nil
}

func (s *Service) keepLocalCopies(dir, name string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, prefix := range []string{name + "-2", name + "-files-"} {
		var names []string
		for _, entry := range entries {
			if entry.Type().IsRegular() && strings.HasPrefix(entry.Name(), prefix) {
				names = append(names, entry.Name())
			}
		}
		sort.Sort(sort.Reverse(sort.StringSlice(names)))
		if len(names) <= 3 {
			continue
		}
		for _, old := range names[3:] {
			if err := os.Remove(filepath.Join(dir, old)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

func backupPrefixes(now time.Time) []string {
	bj := now.Add(8 * time.Hour)
	prefixes := []string{"daily"}
	if bj.Weekday() == time.Sunday {
		prefixes = append(prefixes, "weekly")
	}
	if bj.Day() == 1 {
		prefixes = append(prefixes, "monthly")
	}
	return prefixes
}

var databaseNamePattern = regexp.MustCompile(`[^a-z0-9]+`)

func safeDatabaseName(value string) string {
	return strings.Trim(databaseNamePattern.ReplaceAllString(strings.ToLower(value), "-"), "-")
}

func DatabaseName(databaseURL string) string {
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		return ""
	}
	name, err := url.PathUnescape(strings.TrimPrefix(parsed.EscapedPath(), "/"))
	if err != nil {
		return ""
	}
	return name
}

func fileSHA256(name string) (string, error) {
	file, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func commandError(name string, err error) error {
	if output := strings.TrimSpace(string(execErrorOutput(err))); output != "" {
		return fmt.Errorf("%s: %s", name, output)
	}
	return fmt.Errorf("%s: %w", name, err)
}

func commandMessage(err error) string {
	if output := strings.TrimSpace(string(execErrorOutput(err))); output != "" {
		return output
	}
	return err.Error()
}

// ExecRunner wraps stderr in a typed error so callers can preserve the command's useful message.
type commandFailure struct {
	stderr string
	err    error
}

func (e commandFailure) Error() string { return e.err.Error() }
func (e commandFailure) Unwrap() error { return e.err }

func execErrorOutput(err error) []byte {
	var failure commandFailure
	if errors.As(err, &failure) {
		return []byte(failure.stderr)
	}
	return nil
}

func firstLine(value string, max int) string {
	value = strings.TrimSpace(value)
	if lines := strings.SplitN(value, "\n", 2); len(lines) > 0 {
		value = strings.TrimSpace(lines[0])
	}
	if len(value) > max {
		return value[:max]
	}
	return value
}

// SignV4 returns the AWS Signature Version 4 headers used by Tencent COS.
func SignV4(method string, target *url.URL, region, service, accessKey, secretKey, payloadHash string, headers map[string]string, now time.Time) map[string]string {
	amzDate := now.UTC().Format("20060102T150405Z")
	day := amzDate[:8]
	all := make(map[string]string, len(headers)+3)
	for key, value := range headers {
		all[strings.ToLower(key)] = strings.TrimSpace(value)
	}
	all["host"] = target.Host
	all["x-amz-content-sha256"] = payloadHash
	all["x-amz-date"] = amzDate
	names := make([]string, 0, len(all))
	for name := range all {
		names = append(names, name)
	}
	sort.Strings(names)
	canonicalHeaders := make([]string, 0, len(names))
	for _, name := range names {
		canonicalHeaders = append(canonicalHeaders, name+":"+all[name]+"\n")
	}
	canonicalQuery := target.Query().Encode()
	canonicalPath := target.EscapedPath()
	if canonicalPath == "" {
		canonicalPath = "/"
	}
	canonical := strings.Join([]string{method, canonicalPath, canonicalQuery, strings.Join(canonicalHeaders, ""), strings.Join(names, ";"), payloadHash}, "\n")
	scope := day + "/" + region + "/" + service + "/aws4_request"
	toSign := strings.Join([]string{"AWS4-HMAC-SHA256", amzDate, scope, hashHex([]byte(canonical))}, "\n")
	kDate := hmacBytes([]byte("AWS4"+secretKey), day)
	kRegion := hmacBytes(kDate, region)
	kService := hmacBytes(kRegion, service)
	kSigning := hmacBytes(kService, "aws4_request")
	signature := hex.EncodeToString(hmacBytes(kSigning, toSign))
	all["authorization"] = "AWS4-HMAC-SHA256 Credential=" + accessKey + "/" + scope + ", SignedHeaders=" + strings.Join(names, ";") + ", Signature=" + signature
	return all
}

func hashHex(value []byte) string {
	result := sha256.Sum256(value)
	return hex.EncodeToString(result[:])
}

func hmacBytes(key []byte, value string) []byte {
	hash := hmac.New(sha256.New, key)
	_, _ = hash.Write([]byte(value))
	return hash.Sum(nil)
}

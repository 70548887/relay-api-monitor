// Package fingerprint identifies which model wrote a set of replies by scoring them against the lm-detector
// reference bank.
package fingerprint

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	lmfpd "github.com/Ikaleio/lm-detector/sdk/go"
	"relaylab/internal/domain"
)

// ErrBankUnavailable wraps every failure to read or download the reference bank.
var ErrBankUnavailable = errors.New("fingerprint: reference bank unavailable")

const (
	cacheFile     = "data.bin"
	localBankFile = "unified_bank.json"
	detectorFile  = "shared_detector.bin"

	// The SDK refreshes a cache older than its TTL in the background. Here the bank changes only through Refresh.
	noBackgroundRefresh = 100 * 365 * 24 * time.Hour
	loadTimeout         = 5 * time.Minute
	// retryDelay keeps repeated requests from repeating a failed download or read.
	retryDelay     = 30 * time.Second
	maxHeaderBytes = 16 << 20
)

// Model is one model of the reference bank.
type Model = domain.FingerprintModel

// Reference is one loaded build of the reference bank. It is immutable and safe for concurrent use.
type Reference struct {
	// BuiltAt identifies the build.
	BuiltAt string
	// UpdatedAt is when the files of this build were written.
	UpdatedAt time.Time

	detector *lmfpd.Detector
	models   []Model
	index    map[string]int
}

// Models lists the models of the bank in bank order.
func (r *Reference) Models() []Model { return append([]Model(nil), r.models...) }

// Model looks one model up by its bank ID.
func (r *Reference) Model(id string) (Model, bool) {
	i, ok := r.index[id]
	if !ok {
		return Model{}, false
	}
	return r.models[i], true
}

// Status describes the reference bank for the administration page.
type Status = domain.FingerprintBank

// Bank loads the reference bank and keeps the current build in memory. The online bank is cached in one file of the
// bank directory, which the server and worker processes share: a build written by one process is picked up by the
// other on its next use.
type Bank struct {
	dir string
	// sources are the download base URLs; nil selects the SDK defaults.
	sources []string

	refreshMu sync.Mutex
	mu        sync.Mutex
	loaded    *loadedReference
	call      *loadCall
}

type loadedReference struct {
	source string
	stamp  fileStamp
	ref    *Reference
}

type loadCall struct {
	source   string
	done     chan struct{}
	ref      *Reference
	err      error
	finished time.Time
}

// reusable reports whether a caller should wait for this call instead of starting another: it is still running, or
// it failed a moment ago.
func (c *loadCall) reusable(now time.Time) bool {
	select {
	case <-c.done:
		return c.err != nil && now.Sub(c.finished) < retryDelay
	default:
		return true
	}
}

// fileStamp identifies the content of the bank files by size and modification time.
type fileStamp struct {
	sizes [2]int64
	times [2]int64
}

// NewBank returns a bank that caches the online reference data in dir.
func NewBank(dir string) *Bank { return &Bank{dir: dir} }

func (b *Bank) files(source string) []string {
	if source == "" {
		return []string{filepath.Join(b.dir, cacheFile)}
	}
	return []string{filepath.Join(source, localBankFile), filepath.Join(source, detectorFile)}
}

func statFiles(paths []string) (fileStamp, time.Time, bool) {
	var stamp fileStamp
	var newest time.Time
	for i, path := range paths {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			return fileStamp{}, time.Time{}, false
		}
		stamp.sizes[i], stamp.times[i] = info.Size(), info.ModTime().UnixNano()
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	return stamp, newest, true
}

func unavailable(err error) error { return fmt.Errorf("%w: %v", ErrBankUnavailable, err) }

// Reference returns the current build of the bank. An empty source selects the online bank, which is downloaded on
// first use and then read from its cache file; any other source is an absolute local directory holding
// unified_bank.json and shared_detector.bin. Loading continues in the background when ctx ends first.
func (b *Bank) Reference(ctx context.Context, source string) (*Reference, error) {
	if source != "" && !filepath.IsAbs(source) {
		return nil, unavailable(errors.New("the local reference directory must be an absolute path"))
	}
	b.mu.Lock()
	if stamp, _, ok := statFiles(b.files(source)); ok && b.loaded != nil && b.loaded.source == source && b.loaded.stamp == stamp {
		ref := b.loaded.ref
		b.mu.Unlock()
		return ref, nil
	}
	call := b.call
	if call == nil || call.source != source || !call.reusable(time.Now()) {
		call = &loadCall{source: source, done: make(chan struct{})}
		b.call = call
		go b.run(call)
	}
	b.mu.Unlock()
	select {
	case <-call.done:
		return call.ref, call.err
	case <-ctx.Done():
		return nil, unavailable(ctx.Err())
	}
}

func (b *Bank) run(call *loadCall) {
	ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
	defer cancel()
	ref, stamp, err := b.read(ctx, call.source)
	b.mu.Lock()
	if err == nil {
		b.loaded = &loadedReference{source: call.source, stamp: stamp, ref: ref}
	}
	call.ref, call.err, call.finished = ref, err, time.Now()
	b.mu.Unlock()
	close(call.done)
}

func (b *Bank) read(ctx context.Context, source string) (*Reference, fileStamp, error) {
	paths := b.files(source)
	options := lmfpd.LoadOptions{Sources: []string{source}}
	header := readBankFile
	if source == "" {
		if err := os.MkdirAll(b.dir, 0o700); err != nil {
			return nil, fileStamp{}, unavailable(err)
		}
		options = lmfpd.LoadOptions{Sources: b.sources, Cache: paths[0], TTL: noBackgroundRefresh}
		header = readCacheHeader
	}
	// The other process may replace the files between the two reads below; one more attempt then reads one build.
	for attempt := 0; ; attempt++ {
		before, _, existed := statFiles(paths)
		detector, err := lmfpd.Load(ctx, options)
		if err != nil {
			return nil, fileStamp{}, unavailable(err)
		}
		head, err := header(paths[0])
		if err != nil {
			return nil, fileStamp{}, unavailable(err)
		}
		after, updatedAt, ok := statFiles(paths)
		if ok && (!existed || before == after) && head.BuiltAt == detector.BuiltAt() {
			return newReference(detector, head, updatedAt), after, nil
		}
		if attempt == 1 {
			return nil, fileStamp{}, unavailable(errors.New("the reference bank files changed while they were read"))
		}
	}
}

// Refresh downloads the online bank again and replaces the cached build. changed reports whether the build differs
// from the one cached before.
func (b *Bank) Refresh(ctx context.Context) (ref *Reference, changed bool, err error) {
	b.refreshMu.Lock()
	defer b.refreshMu.Unlock()
	if err = os.MkdirAll(b.dir, 0o700); err != nil {
		return nil, false, unavailable(err)
	}
	target := filepath.Join(b.dir, cacheFile)
	suffix := make([]byte, 8)
	if _, err = rand.Read(suffix); err != nil {
		return nil, false, unavailable(err)
	}
	// The SDK downloads whenever its cache file is missing, so a fresh staging path forces a full download.
	staging := target + ".download-" + hex.EncodeToString(suffix)
	defer os.Remove(staging)
	detector, err := lmfpd.Load(ctx, lmfpd.LoadOptions{Sources: b.sources, Cache: staging, TTL: noBackgroundRefresh})
	if err != nil {
		return nil, false, unavailable(err)
	}
	head, err := readCacheHeader(staging)
	if err != nil {
		return nil, false, unavailable(fmt.Errorf("the downloaded reference bank could not be stored: %v", err))
	}
	if head.BuiltAt != detector.BuiltAt() {
		return nil, false, unavailable(errors.New("the downloaded reference bank is inconsistent"))
	}
	previous, previousErr := readCacheHeader(target)
	changed = previousErr != nil || previous.BuiltAt != head.BuiltAt || previous.version() != head.version()
	if err = replaceFile(staging, target); err != nil {
		return nil, false, unavailable(err)
	}
	stamp, updatedAt, ok := statFiles([]string{target})
	if !ok {
		return nil, false, unavailable(errors.New("the reference bank cache is missing after the update"))
	}
	ref = newReference(detector, head, updatedAt)
	b.mu.Lock()
	b.loaded = &loadedReference{source: "", stamp: stamp, ref: ref}
	if b.call != nil && b.call.source == "" {
		b.call = nil
	}
	b.mu.Unlock()
	return ref, changed, nil
}

// replaceFile renames over the target. On Windows the rename fails while another process has the target open, which
// lasts only for the moment that process reads the file.
func replaceFile(from, to string) error {
	var err error
	for attempt := 0; attempt < 10; attempt++ {
		if err = os.Rename(from, to); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return err
}

// Status reports the bank without downloading it: an online bank that was never downloaded is simply not ready.
func (b *Bank) Status(ctx context.Context, source string) Status {
	if _, _, ok := statFiles(b.files(source)); !ok && source == "" {
		return Status{Source: "online"}
	}
	status, _ := b.Catalog(ctx, source)
	return status
}

// Catalog reports the bank together with its models. Unlike Status it downloads the online bank on first use; the
// models are nil while the bank is not ready.
func (b *Bank) Catalog(ctx context.Context, source string) (Status, []Model) {
	status := Status{Source: "online"}
	if source != "" {
		status.Source = "local"
	}
	ref, err := b.Reference(ctx, source)
	if err != nil {
		status.Error = err.Error()
		return status, nil
	}
	updatedAt := ref.UpdatedAt.UTC()
	status.Ready, status.BuiltAt, status.Models, status.UpdatedAt = true, ref.BuiltAt, len(ref.models), &updatedAt
	return status, ref.Models()
}

// Expected resolves the reference model of a channel model in the current build of the bank. See
// Reference.Expected.
func (b *Bank) Expected(ctx context.Context, source string, m domain.Model) (string, error) {
	ref, err := b.Reference(ctx, source)
	if err != nil {
		return "", err
	}
	return ref.Expected(m), nil
}

// Judge scores one round of answers against the current build of the bank. See Reference.Judge.
func (b *Bank) Judge(ctx context.Context, source string, answers []Answer, expected string) (domain.FingerprintAnalysis, error) {
	ref, err := b.Reference(ctx, source)
	if err != nil {
		return domain.FingerprintAnalysis{}, err
	}
	return ref.Judge(answers, expected)
}

// bankHeader holds the fields read from bundle.json or unified_bank.json.
type bankHeader struct {
	BuiltAt string  `json:"built_at"`
	Models  []Model `json:"models"`
	Files   map[string]struct {
		SHA256 string `json:"sha256"`
	} `json:"files"`
}

// version is the SHA-256 of the detector file, which names the build of the online bank.
func (h *bankHeader) version() string { return h.Files[detectorFile].SHA256 }

func parseHeader(data []byte) (*bankHeader, error) {
	var head bankHeader
	if err := json.Unmarshal(data, &head); err != nil {
		return nil, errors.New("the reference bank index is not valid JSON")
	}
	if len(head.Models) == 0 {
		return nil, errors.New("the reference bank lists no models")
	}
	for _, model := range head.Models {
		if model.ID == "" {
			return nil, errors.New("the reference bank lists a model without an ID")
		}
	}
	return &head, nil
}

// readCacheHeader reads bundle.json from the SDK cache file, which stores its little-endian u32 length, bundle.json
// itself, then the detector.
func readCacheHeader(path string) (*bankHeader, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var prefix [4]byte
	if _, err = io.ReadFull(file, prefix[:]); err != nil {
		return nil, errors.New("the reference bank cache is truncated")
	}
	length := binary.LittleEndian.Uint32(prefix[:])
	if length == 0 || length > maxHeaderBytes {
		return nil, errors.New("the reference bank cache has an invalid index length")
	}
	data := make([]byte, length)
	if _, err = io.ReadFull(file, data); err != nil {
		return nil, errors.New("the reference bank cache is truncated")
	}
	return parseHeader(data)
}

func readBankFile(path string) (*bankHeader, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseHeader(data)
}

func newReference(detector *lmfpd.Detector, head *bankHeader, updatedAt time.Time) *Reference {
	ref := &Reference{BuiltAt: head.BuiltAt, UpdatedAt: updatedAt, detector: detector, models: head.Models,
		index: make(map[string]int, len(head.Models))}
	for i, model := range head.Models {
		ref.index[model.ID] = i
	}
	return ref
}

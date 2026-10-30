// Package backup implements the archive format shared by the CLI, admin
// backup manager and launcher. Callers coordinate operation locks themselves.
package backup

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"
	"relaylab/internal/operationlock"
	"relaylab/internal/security"
	"relaylab/internal/store"
)

const MaxDatabaseSize int64 = 2 << 30
const MaxArchiveSize int64 = 3 << 30
const maxManifestSize int64 = 64 << 10

type Options struct {
	BackupID   string
	Recipient  string
	Version    string
	Commit     string
	Schema     int
	Legacy     bool
	StagingDir string
	OnPhase    func(string) error
	// OnReady must durably store the encrypted file's digest before publication.
	// A failed callback prevents the final filename from appearing.
	OnReady func(Member) error
}

type Member struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	FormatVersion  int               `json:"format_version,omitempty"`
	DatabaseSchema int               `json:"database_schema,omitempty"`
	AppVersion     string            `json:"app_version,omitempty"`
	Commit         string            `json:"commit,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
	BackupID       string            `json:"backup_id,omitempty"`
	KeyID          string            `json:"key_id,omitempty"`
	Files          map[string]Member `json:"files,omitempty"`
	// Schema and Version are retained for the existing unencrypted ZIP format.
	Schema  int    `json:"schema,omitempty"`
	Version string `json:"version,omitempty"`
}

type Validated struct {
	Dir      string
	DBPath   string
	KeyPath  string
	Manifest Manifest
}

func (v *Validated) Close() error {
	if v == nil || v.Dir == "" {
		return nil
	}
	return os.RemoveAll(v.Dir)
}

func StagingRoot(dataDir string) string {
	return filepath.Join(operationlock.BackupRoot(dataDir), ".staging")
}

// ProtectDirectory and ProtectFile restrict backup-owned plaintext staging
// and recovery journals; unlike the legacy general data helper, they fail
// closed when Windows ACLs cannot be applied.
func ProtectDirectory(path string) error { return protectDirectory(path) }
func ProtectFile(path string) error      { return protectFile(path) }

// PublishVerifiedFile publishes an already validated encrypted temporary file,
// without replacing an existing destination. Both paths must be on one volume.
func PublishVerifiedFile(source, target string) error { return publishArchive(source, target) }

func GenerateIdentity() (string, string, error) {
	i, err := age.GenerateX25519Identity()
	if err != nil {
		return "", "", err
	}
	return i.String(), i.Recipient().String(), nil
}

func VerifyIdentity(recipient, identity string) error {
	r, err := age.ParseX25519Recipient(strings.TrimSpace(recipient))
	if err != nil {
		return errors.New("恢复公钥格式无效")
	}
	i, err := age.ParseX25519Identity(strings.TrimSpace(identity))
	if err != nil || i.Recipient().String() != r.String() {
		return errors.New("恢复私钥与公钥不匹配")
	}
	challenge := make([]byte, 32)
	if _, err = rand.Read(challenge); err != nil {
		return err
	}
	var ciphertext bytes.Buffer
	w, err := age.Encrypt(&ciphertext, r)
	if err != nil {
		return err
	}
	if _, err = w.Write(challenge); err != nil {
		return err
	}
	if err = w.Close(); err != nil {
		return err
	}
	reader, err := age.Decrypt(&ciphertext, i)
	if err != nil {
		return errors.New("恢复密钥加解密验证失败")
	}
	decoded, err := io.ReadAll(io.LimitReader(reader, 33))
	if err != nil || !bytes.Equal(decoded, challenge) {
		return errors.New("恢复密钥加解密验证失败")
	}
	return nil
}

func RecipientID(recipient string) string {
	h := sha256.Sum256([]byte(strings.TrimSpace(recipient)))
	return hex.EncodeToString(h[:])[:16]
}

// Create does not take an operation lock: the caller may already own it while
// coordinating an upgrade. The public CLI wraps it with operationlock.Acquire.
func Create(ctx context.Context, dataDir, out string, opts Options) error {
	s, err := store.OpenExisting(filepath.Join(dataDir, "relaylab.db"))
	if err != nil {
		return err
	}
	defer s.Close()
	return CreateFromStore(ctx, s, dataDir, out, opts)
}

func CreateFromStore(ctx context.Context, st *store.Store, dataDir, out string, opts Options) error {
	if out == "" {
		return errors.New("请指定备份输出路径")
	}
	var recipient *age.X25519Recipient
	var err error
	if !opts.Legacy {
		recipient, err = age.ParseX25519Recipient(strings.TrimSpace(opts.Recipient))
		if err != nil {
			return errors.New("请配置有效的恢复公钥")
		}
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	root := opts.StagingDir
	if root == "" {
		root = StagingRoot(dataDir)
	}
	sourceInfo, err := os.Stat(filepath.Join(dataDir, "relaylab.db"))
	if err != nil {
		return err
	}
	if sourceInfo.Size() > MaxDatabaseSize {
		return errors.New("数据库超过备份上限 2 GiB")
	}
	if err = EnsureSpace(root, uint64(sourceInfo.Size())*2+(64<<20)); err != nil {
		return err
	}
	temp, err := newStaging(root, "archive-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	dbPath := filepath.Join(temp, "relaylab.db")
	if err = st.Backup(ctx, dbPath); err != nil {
		return err
	}
	dbInfo, err := os.Stat(dbPath)
	if err != nil {
		return err
	}
	if dbInfo.Size() > MaxDatabaseSize {
		return errors.New("数据库超过备份上限 2 GiB")
	}
	if opts.OnPhase != nil {
		if err = opts.OnPhase("validating"); err != nil {
			return err
		}
	}
	keyPath := filepath.Join(dataDir, "master.key")
	if err = copyKey(keyPath, filepath.Join(temp, "master.key")); err != nil {
		return err
	}
	schema, err := validateDatabase(ctx, temp, false)
	if err != nil {
		return err
	}
	if opts.Schema > 0 && opts.Schema != schema {
		return errors.New("数据库版本与备份构建版本不一致")
	}
	backupID := opts.BackupID
	if backupID == "" {
		backupID = security.Token()
	}
	manifest := Manifest{FormatVersion: 2, DatabaseSchema: schema, AppVersion: opts.Version, Commit: opts.Commit, CreatedAt: time.Now().UTC(), BackupID: backupID, Files: map[string]Member{}}
	if recipient != nil {
		manifest.KeyID = RecipientID(recipient.String())
	}
	for _, name := range []string{"relaylab.db", "master.key"} {
		member, err := hashFile(ctx, filepath.Join(temp, name))
		if err != nil {
			return err
		}
		manifest.Files[name] = member
	}
	if opts.Legacy {
		manifest = Manifest{Schema: 1, Version: opts.Version, CreatedAt: manifest.CreatedAt}
	}
	meta, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(temp, "manifest.json"), meta, 0600); err != nil {
		return err
	}
	if opts.OnPhase != nil {
		if err = opts.OnPhase("encrypting"); err != nil {
			return err
		}
	}
	return writeArchive(ctx, temp, out, recipient, opts.OnReady)
}

func newStaging(root, pattern string) (string, error) {
	if root == "" {
		return "", errors.New("必须指定持久暂存目录")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(root, pattern)
	if err != nil {
		return "", err
	}
	if err = protectDirectory(dir); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

func copyKey(src, dest string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != 32 {
		return errors.New("备份主密钥必须为32字节普通文件")
	}
	key, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if len(key) != 32 {
		return errors.New("备份主密钥必须为32字节")
	}
	defer clear(key)
	return os.WriteFile(dest, key, 0600)
}

func writeArchive(ctx context.Context, dir, out string, recipient *age.X25519Recipient, onReady func(Member) error) (err error) {
	out, err = filepath.Abs(out)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(out), 0700); err != nil {
		return err
	}
	if _, err = os.Lstat(out); err == nil {
		return os.ErrExist
	} else if !os.IsNotExist(err) {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(out), ".backup-*.partial")
	if err != nil {
		return err
	}
	defer func() { _ = f.Close(); _ = os.Remove(f.Name()) }()
	if err = protectFile(f.Name()); err != nil {
		return err
	}
	archiveHash := sha256.New()
	var writer io.Writer = io.MultiWriter(f, archiveHash)
	var encrypted io.WriteCloser
	if recipient != nil {
		encrypted, err = age.Encrypt(writer, recipient)
		if err != nil {
			return err
		}
		writer = encrypted
	}
	zw := zip.NewWriter(writer)
	for _, name := range []string{"master.key", "relaylab.db", "manifest.json"} {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		if err != nil {
			_ = zw.Close()
			return err
		}
		src, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			_ = zw.Close()
			return err
		}
		_, err = io.Copy(w, contextReader{ctx, src})
		_ = src.Close()
		if err != nil {
			_ = zw.Close()
			return err
		}
	}
	if err = zw.Close(); err != nil {
		return err
	}
	if encrypted != nil {
		if err = encrypted.Close(); err != nil {
			return err
		}
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if onReady != nil {
		info, statErr := os.Stat(f.Name())
		if statErr != nil {
			return statErr
		}
		if err = onReady(Member{Size: info.Size(), SHA256: hex.EncodeToString(archiveHash.Sum(nil))}); err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
	}
	// Publish inside the same output directory, without ever overwriting an
	// existing backup, after its trusted digest has been committed.
	return publishArchive(f.Name(), out)
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func hashFile(ctx context.Context, name string) (Member, error) {
	f, err := os.Open(name)
	if err != nil {
		return Member{}, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, contextReader{ctx, f})
	return Member{Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}, err
}

// Validate extracts only into a freshly created restricted directory, checks
// declared sizes and checksums, verifies all credentials and migrates only the
// isolated copy. The input archive is never modified.
func Validate(ctx context.Context, input, identity, staging string) (_ *Validated, err error) {
	info, err := os.Lstat(input)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > MaxArchiveSize {
		return nil, errors.New("备份文件无效或超过大小限制")
	}
	temp, err := newStaging(staging, "validate-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(temp)
		}
	}()
	f, err := os.Open(input)
	if err != nil {
		return nil, err
	}
	br := bufio.NewReader(f)
	header, _ := br.Peek(22)
	zipPath := input
	if strings.HasPrefix(string(header), "age-encryption.org/") {
		if err = EnsureSpace(staging, uint64(info.Size())+(64<<20)); err != nil {
			_ = f.Close()
			return nil, err
		}
		i, parseErr := age.ParseX25519Identity(strings.TrimSpace(identity))
		if parseErr != nil {
			_ = f.Close()
			return nil, errors.New("请提供有效的恢复私钥")
		}
		plain, decryptErr := age.Decrypt(br, i)
		if decryptErr != nil {
			_ = f.Close()
			return nil, errors.New("恢复私钥不匹配或加密备份损坏")
		}
		zipPath = filepath.Join(temp, ".decrypted.zip")
		zf, openErr := os.OpenFile(zipPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if openErr != nil {
			_ = f.Close()
			return nil, openErr
		}
		n, copyErr := io.Copy(zf, io.LimitReader(contextReader{ctx, plain}, MaxArchiveSize+1))
		if syncErr := zf.Sync(); copyErr == nil {
			copyErr = syncErr
		}
		if closeErr := zf.Close(); copyErr == nil {
			copyErr = closeErr
		}
		_ = f.Close()
		if copyErr != nil {
			return nil, fmt.Errorf("加密备份校验失败: %w", copyErr)
		}
		if n > MaxArchiveSize {
			return nil, errors.New("解密备份超过大小限制")
		}
	} else {
		_ = f.Close()
	}
	if err = extractZIP(ctx, zipPath, temp); err != nil {
		return nil, err
	}
	if zipPath != input {
		if err = os.Remove(zipPath); err != nil {
			return nil, err
		}
	}
	manifest, err := verifyManifest(ctx, temp)
	if err != nil {
		return nil, err
	}
	if manifest.FormatVersion == 2 {
		originalSchema, schemaErr := readSchema(ctx, filepath.Join(temp, "relaylab.db"))
		if schemaErr != nil {
			return nil, schemaErr
		}
		if originalSchema != manifest.DatabaseSchema {
			return nil, errors.New("备份 manifest 与数据库版本不一致")
		}
	}
	schema, err := validateDatabase(ctx, temp, true)
	if err != nil {
		return nil, err
	}
	if manifest.FormatVersion == 0 {
		manifest.FormatVersion = 1
	}
	// Return the schema of the validated and migrated target, with the original
	// manifest left intact on disk for inspection and provenance.
	manifest.DatabaseSchema = schema
	return &Validated{Dir: temp, DBPath: filepath.Join(temp, "relaylab.db"), KeyPath: filepath.Join(temp, "master.key"), Manifest: manifest}, nil
}

func extractZIP(ctx context.Context, input, dir string) error {
	zr, err := zip.OpenReader(input)
	if err != nil {
		return err
	}
	defer zr.Close()
	var required uint64
	for _, item := range zr.File {
		if item.UncompressedSize64 > uint64(MaxDatabaseSize)+uint64(maxManifestSize)+32 {
			return errors.New("备份成员超过大小限制")
		}
		required += item.UncompressedSize64
	}
	if required > uint64(MaxDatabaseSize)+uint64(maxManifestSize)+32 {
		return errors.New("备份展开总量超过大小限制")
	}
	if err = EnsureSpace(dir, required+(64<<20)); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, item := range zr.File {
		if item.Name != "relaylab.db" && item.Name != "master.key" && item.Name != "manifest.json" {
			return errors.New("备份包含未知文件")
		}
		if seen[item.Name] {
			return errors.New("备份包含重复文件")
		}
		seen[item.Name] = true
		if item.FileInfo().IsDir() || (item.Mode()&(os.ModeSymlink|os.ModeDevice|os.ModeNamedPipe|os.ModeSocket)) != 0 {
			return errors.New("备份成员必须为普通文件")
		}
		limit := MaxDatabaseSize
		switch item.Name {
		case "master.key":
			limit = 32
			if item.UncompressedSize64 != 32 {
				return errors.New("备份主密钥必须为32字节")
			}
		case "manifest.json":
			limit = maxManifestSize
		}
		if item.UncompressedSize64 > uint64(limit) {
			return errors.New("备份成员超过大小限制")
		}
		src, err := item.Open()
		if err != nil {
			return err
		}
		dest, err := os.OpenFile(filepath.Join(dir, item.Name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			_ = src.Close()
			return err
		}
		n, err := io.Copy(dest, io.LimitReader(contextReader{ctx, src}, limit+1))
		if err == nil && (n > limit || uint64(n) != item.UncompressedSize64) {
			err = errors.New("备份成员实际长度与声明不符")
		}
		if syncErr := dest.Sync(); err == nil {
			err = syncErr
		}
		if closeErr := src.Close(); err == nil {
			err = closeErr
		}
		if closeErr := dest.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
	}
	if !seen["relaylab.db"] || !seen["master.key"] {
		return errors.New("备份缺少数据库或主密钥")
	}
	return nil
}

func verifyManifest(ctx context.Context, dir string) (Manifest, error) {
	var m Manifest
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if os.IsNotExist(err) {
		return m, nil
	} // Existing pre-manifest ZIPs remain restorable.
	if err != nil {
		return m, err
	}
	if err = json.Unmarshal(b, &m); err != nil {
		return m, errors.New("备份 manifest 格式无效")
	}
	if m.FormatVersion == 0 && m.Schema == 1 {
		return m, nil
	}
	if m.FormatVersion != 2 || m.DatabaseSchema <= 0 || len(m.Files) != 2 {
		return m, errors.New("备份格式版本不受支持")
	}
	for _, name := range []string{"relaylab.db", "master.key"} {
		want, ok := m.Files[name]
		if !ok || len(want.SHA256) != 64 {
			return m, errors.New("备份成员校验信息缺失")
		}
		got, err := hashFile(ctx, filepath.Join(dir, name))
		if err != nil {
			return m, err
		}
		if got.Size != want.Size || got.SHA256 != strings.ToLower(want.SHA256) {
			return m, errors.New("备份成员 SHA-256 校验失败")
		}
	}
	return m, nil
}

func validateDatabase(ctx context.Context, dir string, migrate bool) (schema int, err error) {
	vault, err := security.LoadVault(dir, true)
	if err != nil {
		return 0, err
	}
	var st *store.Store
	if migrate {
		st, err = store.OpenForRestore(filepath.Join(dir, "relaylab.db"))
	} else {
		st, err = store.OpenExisting(filepath.Join(dir, "relaylab.db"))
	}
	if err != nil {
		return 0, err
	}
	defer func() {
		if closeErr := st.Close(); err == nil {
			err = closeErr
		}
	}()
	channels, err := st.ListChannels(ctx)
	if err != nil {
		return 0, err
	}
	for _, c := range channels {
		if c.EncryptedKey != "" {
			if _, err = vault.Decrypt(c.EncryptedKey); err != nil {
				return 0, err
			}
		}
	}
	connections, err := st.ListUserConnections(ctx, "")
	if err != nil {
		return 0, err
	}
	for _, c := range connections {
		if _, err = vault.DecryptUserConnection(c.UserID, c.ID, c.EncryptedKey); err != nil {
			return 0, err
		}
	}
	if err = st.ValidateBackupStorageKeys(ctx, vault); err != nil {
		return 0, err
	}
	var message string
	if err = st.DB.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&message); err != nil {
		return 0, err
	}
	if message != "ok" {
		return 0, errors.New("数据库完整性校验失败")
	}
	// integrity_check does not validate foreign-key relationships.
	foreign, e := st.DB.QueryContext(ctx, "PRAGMA foreign_key_check")
	if e != nil {
		return 0, e
	}
	broken := foreign.Next()
	readErr := foreign.Err()
	closeErr := foreign.Close()
	if readErr != nil {
		return 0, readErr
	}
	if closeErr != nil {
		return 0, closeErr
	}
	if broken {
		return 0, errors.New("数据库关联完整性校验失败")
	}
	if err = st.DB.QueryRowContext(ctx, "PRAGMA user_version").Scan(&schema); err != nil {
		return 0, err
	}
	if err = checkpoint(ctx, st); err != nil {
		return 0, err
	}
	return schema, nil
}

func checkpoint(ctx context.Context, st *store.Store) error {
	var busy, log, done int
	if err := st.DB.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &log, &done); err != nil {
		return err
	}
	if busy != 0 {
		return errors.New("备份暂存数据库 WAL 检查点繁忙")
	}
	return nil
}

func readSchema(ctx context.Context, path string) (int, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return 0, err
	}
	p := filepath.ToSlash(abs)
	if filepath.VolumeName(abs) != "" && !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	u := url.URL{Scheme: "file", Path: p, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var schema int
	err = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&schema)
	return schema, err
}

// Restore restores to a new empty target only; online replacement belongs to
// the launcher's recoverable transaction and uses Validate instead.
func Restore(ctx context.Context, dataDir, input, identity string) error {
	if input == "" {
		return errors.New("请使用 --input 指定备份 ZIP 或加密包")
	}
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return err
	}
	if filepath.Dir(abs) == abs {
		return errors.New("恢复目标必须为专用子目录")
	}
	if err = emptyTarget(abs); err != nil {
		return err
	}
	release, err := operationlock.Acquire(abs)
	if err != nil {
		return err
	}
	defer release()
	v, err := Validate(ctx, input, identity, StagingRoot(abs))
	if err != nil {
		return err
	}
	defer v.Close()
	if err = FinalizeRestored(ctx, v.Dir); err != nil {
		return err
	}
	// Validate again immediately before publishing: removing an unexpectedly
	// nonempty directory must fail, never erase a concurrent initializer's data.
	if err = emptyTarget(abs); err != nil {
		return err
	}
	if _, err = os.Stat(abs); err == nil {
		if err = os.Remove(abs); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.Rename(v.Dir, abs)
}

// FinalizeRestored invalidates credentials and old execution queues on an
// isolated validated copy. It must run only for an actual restore, never for
// a read-only preflight or backup validation.
func FinalizeRestored(ctx context.Context, dir string) (err error) {
	st, err := store.OpenExisting(filepath.Join(dir, "relaylab.db"))
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := st.Close(); err == nil {
			err = closeErr
		}
	}()
	if err = st.PrepareRestoredData(ctx, time.Now().UTC()); err != nil {
		return err
	}
	return checkpoint(ctx, st)
}

func emptyTarget(dir string) error {
	if info, err := os.Lstat(dir); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("恢复目标必须是空的普通目录")
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		if len(entries) != 0 {
			return errors.New("恢复目标目录必须为空；请选择新的数据目录")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}

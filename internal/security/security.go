package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

type Vault struct{ aead cipher.AEAD }

func LoadVault(dataDir string, existing bool) (*Vault, error) {
	resolved, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, err
	}
	if filepath.Dir(resolved) == resolved {
		return nil, errors.New("数据目录必须是专用子目录，不能是磁盘根目录")
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(dataDir, "master.key")
	key, err := readSecretFile(path)
	if os.IsNotExist(err) {
		if existing {
			return nil, errors.New("数据库已存在但 master.key 缺失；请恢复原始主密钥")
		}
		key = make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return nil, err
		}
		f, e := os.CreateTemp(dataDir, ".master-key-*")
		if e != nil {
			return nil, e
		}
		tempPath := f.Name()
		defer os.Remove(tempPath)
		_, err = f.Write(key)
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if err = publishKeyFile(tempPath, path); err != nil {
			// A concurrent initializer won. Its complete key must be reused.
			var readErr error
			key, readErr = readSecretFile(path)
			if readErr != nil {
				return nil, fmt.Errorf("发布主密钥失败: %w", err)
			}
		}
	} else if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, errors.New("master.key 必须是32字节的原始密钥")
	}
	if err := protectSecretFile(path); err != nil { return nil, fmt.Errorf("限制主密钥权限失败: %w",err) }
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	a, err := cipher.NewGCM(b)
	if err != nil {
		return nil, err
	}
	return &Vault{a}, nil
}

// ProtectFile restricts only a managed secret file, not its containing folder.
func ProtectFile(path string) error { return protectSecretFile(path) }

// ProtectDirectory is used only for newly created application-owned temporary
// directories that can contain backup keys while being assembled or restored.
func ProtectDirectory(path string) error { return protectDataDir(path) }

// ValidateNewDataDirectory is an application initialization guard. The generic
// vault is also used with independently named test/library databases.
func ValidateNewDataDirectory(dataDir string) error {
	if _, err := os.Stat(filepath.Join(dataDir, "master.key")); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	entries, err := os.ReadDir(dataDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	allowed := map[string]bool{"relaylab.db": true, "relaylab.db-wal": true, "relaylab.db-shm": true, "serve.out.log": true, "serve.err.log": true, "worker.out.log": true, "worker.err.log": true, "serve.log": true, "worker.log": true, "processes.json": true, "processes.json.tmp": true, "stop.request": true, "launcher.token": true, "launcher.json": true, "launcher.json.tmp": true, "launcher.process.json": true, "launcher.process.json.tmp": true, "launcher.out.log": true, "launcher.err.log": true, "update-state.json": true}
	for _, entry := range entries {
		if entry.IsDir() || (!allowed[entry.Name()] && !strings.HasPrefix(entry.Name(), ".master-key-")) {
			return errors.New("新数据目录包含其他文件，请选择空的专用目录")
		}
	}
	return nil
}

func (v *Vault) Encrypt(plain string) (string, error) {
	n := make([]byte, v.aead.NonceSize())
	if _, err := rand.Read(n); err != nil {
		return "", err
	}
	return base64.RawStdEncoding.EncodeToString(v.aead.Seal(n, n, []byte(plain), []byte("relaylab/channel-key/v1"))), nil
}
func (v *Vault) Decrypt(encoded string) (string, error) {
	b, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		return "", errors.New("密钥密文损坏")
	}
	n := v.aead.NonceSize()
	if len(b) < n {
		return "", errors.New("密钥密文损坏")
	}
	p, err := v.aead.Open(nil, b[:n], b[n:], []byte("relaylab/channel-key/v1"))
	if err != nil {
		return "", errors.New("平台密钥解密失败，请核对 master.key")
	}
	return string(p), nil
}

func PasswordHash(password string) (string, error) {
	if len(password) < 12 || len(password) > 256 {
		return "", errors.New("密码需为12至256字节")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	h := argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, 32)
	return fmt.Sprintf("$argon2id$v=19$m=65536,t=3,p=2$%s$%s", base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(h)), nil
}
func VerifyPassword(encoded, password string) bool {
	p := strings.Split(encoded, "$")
	if len(p) != 6 || p[1] != "argon2id" || p[2] != "v=19" {
		return false
	}
	var m, t uint32
	var parallel uint8
	if _, err := fmt.Sscanf(p[3], "m=%d,t=%d,p=%d", &m, &t, &parallel); err != nil || m != 65536 || t != 3 || parallel != 2 {
		return false
	}
	salt, e := base64.RawStdEncoding.DecodeString(p[4])
	if e != nil || len(salt) != 16 {
		return false
	}
	want, e := base64.RawStdEncoding.DecodeString(p[5])
	if e != nil || len(want) != 32 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, t, m, parallel, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}
func Token() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func HashToken(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func Mask(s string) string {
	if len(s) <= 12 {
		return "••••••••"
	}
	return "••••••••" + s[len(s)-4:]
}
func ConstantEqual(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
func ValidateName(s string) error {
	if len(strings.TrimSpace(s)) == 0 || len(s) > 200 {
		return errors.New("名称不能为空且不能超过200字节")
	}
	return nil
}
func ValidateInt(s string, min, max int) (int, error) {
	n, e := strconv.Atoi(s)
	if e != nil || n < min || n > max {
		return 0, errors.New("数值超出允许范围")
	}
	return n, nil
}

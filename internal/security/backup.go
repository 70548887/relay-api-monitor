package security

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
)

func (v *Vault) EncryptBackupCredentials(storageID, plain string) (string, error) {
	if v == nil || storageID == "" {
		return "", errors.New("备份加密上下文缺失")
	}
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawStdEncoding.EncodeToString(v.aead.Seal(nonce, nonce, []byte(plain), []byte("relaylab/backup-storage/v1\x00"+storageID))), nil
}
func (v *Vault) DecryptBackupCredentials(storageID, encoded string) (string, error) {
	if v == nil {
		return "", errors.New("备份主密钥未加载")
	}
	b, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil || len(b) < v.aead.NonceSize() {
		return "", errors.New("备份存储密文损坏")
	}
	n := v.aead.NonceSize()
	plain, err := v.aead.Open(nil, b[:n], b[n:], []byte("relaylab/backup-storage/v1\x00"+storageID))
	if err != nil {
		return "", errors.New("备份存储解密失败")
	}
	return string(plain), nil
}

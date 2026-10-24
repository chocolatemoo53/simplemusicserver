package tube

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"

	"golang.org/x/crypto/argon2"
)

const keySize = 32

var ErrDecrypt = errors.New("decryption failed")

var argonSlots = make(chan struct{}, max(1, runtime.NumCPU()/2))

const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 1
	argonSalt    = 16
)

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Errorf("crypto/rand failed: %w", err))
	}
	return b
}

func newAEAD(key []byte) cipher.AEAD {
	if len(key) != keySize {
		panic(fmt.Errorf("key must be %d bytes, got %d", keySize, len(key)))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		panic(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic(err)
	}
	return aead
}

func sealBytes(key, plaintext, ad []byte) []byte {
	aead := newAEAD(key)
	nonce := randomBytes(aead.NonceSize())
	return aead.Seal(nonce, nonce, plaintext, ad)
}

func openBytes(key, sealed, ad []byte) ([]byte, error) {
	aead := newAEAD(key)
	n := aead.NonceSize()
	if len(sealed) < n+aead.Overhead() {
		return nil, ErrDecrypt
	}
	plain, err := aead.Open(nil, sealed[:n], sealed[n:], ad)
	if err != nil {
		return nil, ErrDecrypt
	}
	return plain, nil
}

func deriveKey(secret []byte, info string) []byte {
	key, err := hkdf.Key(sha256.New, secret, nil, info, keySize)
	if err != nil {
		panic(err)
	}
	return key
}

func passwordKEK(password string, salt []byte) []byte {
	argonSlots <- struct{}{}
	defer func() { <-argonSlots }()
	return argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, keySize)
}

func secretKEK(secret, purpose string) []byte {
	return deriveKey([]byte(secret), "simplemusicserver kek "+purpose)
}

type Keyring struct {
	master  []byte
	meta    []byte
	ids     []byte
	fileKEK []byte
}

func newKeyring(master []byte) *Keyring {
	return &Keyring{
		master:  master,
		meta:    deriveKey(master, "simplemusicserver metadata"),
		ids:     deriveKey(master, "simplemusicserver ids"),
		fileKEK: deriveKey(master, "simplemusicserver file keys"),
	}
}

func newMasterKeyring() *Keyring {
	return newKeyring(randomBytes(keySize))
}

func (k *Keyring) ID(namespace, value string) string {
	mac := hmac.New(sha256.New, k.ids)
	mac.Write([]byte(namespace))
	mac.Write([]byte{0})
	mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
}

func (k *Keyring) SealJSON(v any, ad string) ([]byte, error) {
	plain, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return sealBytes(k.meta, plain, []byte(ad)), nil
}

func (k *Keyring) OpenJSON(sealed []byte, ad string, v any) error {
	plain, err := openBytes(k.meta, sealed, []byte(ad))
	if err != nil {
		return err
	}
	return json.Unmarshal(plain, v)
}

func (k *Keyring) NewFileKey(ad string) (fileKey, wrapped []byte) {
	fileKey = randomBytes(keySize)
	return fileKey, sealBytes(k.fileKEK, fileKey, []byte(ad))
}

func (k *Keyring) FileKey(wrapped []byte, ad string) ([]byte, error) {
	return openBytes(k.fileKEK, wrapped, []byte(ad))
}

func wrapMaster(kek []byte, k *Keyring, ad string) []byte {
	return sealBytes(kek, k.master, []byte(ad))
}

func unwrapMaster(kek, wrapped []byte, ad string) (*Keyring, error) {
	master, err := openBytes(kek, wrapped, []byte(ad))
	if err != nil {
		return nil, err
	}
	return newKeyring(master), nil
}

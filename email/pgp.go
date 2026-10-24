package email

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
)

const maxKeySize = 64 << 10

var (
	ErrPrivateKey    = errors.New("that's a private key: paste only your public key")
	ErrNoEncryptKey  = errors.New("that key can't be used for encryption (it may be expired, revoked or sign-only)")
	ErrOneKeyOnly    = errors.New("paste exactly one public key")
	ErrKeyTooLarge   = errors.New("that key is too large")
	ErrNotArmoredKey = errors.New("paste the key in text form, starting with -----BEGIN PGP PUBLIC KEY BLOCK-----")
)

func ParsePublicKey(armored string) (fingerprint string, err error) {
	armored = strings.TrimSpace(armored)
	if len(armored) > maxKeySize {
		return "", ErrKeyTooLarge
	}
	if strings.Contains(armored, "PRIVATE KEY BLOCK") {
		return "", ErrPrivateKey
	}
	if !strings.HasPrefix(armored, "-----BEGIN PGP PUBLIC KEY BLOCK-----") {
		return "", ErrNotArmoredKey
	}
	ring, err := openpgp.ReadArmoredKeyRing(strings.NewReader(armored))
	if err != nil {
		return "", fmt.Errorf("couldn't read that key: %w", err)
	}
	if len(ring) != 1 {
		return "", ErrOneKeyOnly
	}
	entity := ring[0]
	if entity.PrivateKey != nil {
		return "", ErrPrivateKey
	}
	if _, ok := entity.EncryptionKey(time.Now()); !ok {
		return "", ErrNoEncryptKey
	}
	return FormatFingerprint(entity.PrimaryKey.Fingerprint), nil
}

func FormatFingerprint(fp []byte) string {
	hex := strings.ToUpper(fmt.Sprintf("%x", fp))
	var groups []string
	for len(hex) > 0 {
		n := min(4, len(hex))
		groups = append(groups, hex[:n])
		hex = hex[n:]
	}
	return strings.Join(groups, " ")
}

func Encrypt(plain []byte, armoredKey string) ([]byte, error) {
	ring, err := openpgp.ReadArmoredKeyRing(strings.NewReader(armoredKey))
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	aw, err := armor.Encode(&out, "PGP MESSAGE", nil)
	if err != nil {
		return nil, err
	}
	w, err := openpgp.Encrypt(aw, ring, nil, nil, nil)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(plain); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	if err := aw.Close(); err != nil {
		return nil, err
	}
	out.WriteString("\n")
	return out.Bytes(), nil
}

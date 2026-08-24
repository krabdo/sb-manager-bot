package manager

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
)

type CookieCipher struct{ aead cipher.AEAD }

func NewCookieCipher(key []byte) (*CookieCipher, error) {
	if len(key) != 32 {
		return nil, errors.New("encryption key must be 32 bytes")
	}
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	a, err := cipher.NewGCM(b)
	if err != nil {
		return nil, err
	}
	return &CookieCipher{aead: a}, nil
}

func cookieAAD(telegramID int64, forumID string) []byte {
	return []byte(fmt.Sprintf("%d\x00%s", telegramID, forumID))
}

func (c *CookieCipher) Encrypt(cookie string, telegramID int64, forumID string) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	out := append([]byte{1}, nonce...)
	return c.aead.Seal(out, nonce, []byte(cookie), cookieAAD(telegramID, forumID)), nil
}

func (c *CookieCipher) Decrypt(blob []byte, telegramID int64, forumID string) (string, error) {
	if len(blob) < 1+c.aead.NonceSize()+c.aead.Overhead() || blob[0] != 1 {
		return "", errors.New("invalid encrypted cookie")
	}
	nonce := blob[1 : 1+c.aead.NonceSize()]
	plain, err := c.aead.Open(nil, nonce, blob[1+c.aead.NonceSize():], cookieAAD(telegramID, forumID))
	if err != nil {
		return "", errors.New("cookie decryption failed")
	}
	return string(plain), nil
}

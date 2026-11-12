//go:build !windows

package panel

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
)

const protectedHashPrefix = "nodo-key-v1:"

func secretCipher() (cipher.AEAD, error) {
	key, err := hex.DecodeString(os.Getenv("NODO_MASTER_KEY"))
	if err != nil || len(key) != 32 {
		return nil, errors.New("configura NODO_MASTER_KEY con 64 caracteres hexadecimales y consérvala fuera de la base de datos")
	}
	defer clear(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
func protectSecret(secret []byte) ([]byte, error) {
	if len(secret) != 32 {
		return nil, errors.New("clave de protección inválida")
	}
	gcm, err := secretCipher()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, secret, []byte("nodo-secret-v1")), nil
}
func unprotectSecret(blob []byte) ([]byte, error) {
	gcm, err := secretCipher()
	if err != nil {
		return nil, err
	}
	if len(blob) != gcm.NonceSize()+32+gcm.Overhead() {
		return nil, errors.New("clave de protección inválida")
	}
	return gcm.Open(nil, blob[:gcm.NonceSize()], blob[gcm.NonceSize():], []byte("nodo-secret-v1"))
}

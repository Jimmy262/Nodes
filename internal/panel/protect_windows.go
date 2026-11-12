//go:build windows

package panel

import (
	"errors"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

const protectedHashPrefix = "nodo-dpapi-v1:"

// DPAPI's default scope is the current Windows user, never LOCAL_MACHINE.
func protectSecret(secret []byte) ([]byte, error) {
	if len(secret) == 0 {
		return nil, errors.New("clave de protección vacía")
	}
	input := windows.DataBlob{Size: uint32(len(secret)), Data: &secret[0]}
	var output windows.DataBlob
	err := windows.CryptProtectData(&input, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output)
	runtime.KeepAlive(secret)
	if err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(output.Data)))
	return append([]byte(nil), unsafe.Slice(output.Data, int(output.Size))...), nil
}
func unprotectSecret(encrypted []byte) ([]byte, error) {
	if len(encrypted) == 0 || len(encrypted) > 4096 {
		return nil, errors.New("clave de protección inválida")
	}
	input := windows.DataBlob{Size: uint32(len(encrypted)), Data: &encrypted[0]}
	var output windows.DataBlob
	err := windows.CryptUnprotectData(&input, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output)
	runtime.KeepAlive(encrypted)
	if err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(output.Data)))
	plain := unsafe.Slice(output.Data, int(output.Size))
	defer clear(plain)
	if len(plain) != 32 {
		return nil, errors.New("clave de protección inválida")
	}
	return append([]byte(nil), plain...), nil
}

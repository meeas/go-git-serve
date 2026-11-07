// Package bundle embeds a configuration into a copy of the executable, so a
// class can be handed a single file.
//
// Every build reserves a fixed-size slot inside its normal data. Bundling
// fills that slot in place; the file's size and structure do not change,
// there is no data appended after the executable, and on Apple Silicon the
// ad-hoc code signature is recomputed (see macho.go). At run time the slot
// is read straight from memory, wherever the file lives.
//
// The payload is obfuscated, not encrypted: it stops the token showing up
// in a text editor or `strings`, but anyone determined can recover it,
// because the program itself must be able to.
package bundle

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// SlotSize is the space reserved in every build.
const SlotSize = 64 << 10

// Slot layout.
const (
	magicLen      = 16
	offVersion    = 16
	offState      = 17
	offLength     = 20 // uint32 little-endian
	offNonce      = 24 // 16 bytes
	offChecksum   = 40 // sha256 of the plaintext payload
	offPayload    = 72
	formatVersion = 1
	stateEmpty    = 0
	stateFilled   = 1
)

// Capacity is the largest payload a slot holds.
const Capacity = SlotSize - offPayload

// slot is the reserved space. It starts with the marker so it is stored in
// the file's initialised data (not zero-filled at load time), and so the
// bundler can find it. Nothing else in the binary contains the marker: the
// bundler builds it at run time from a reversed copy.
var slot = [SlotSize]byte{
	'g', 'g', 's', '-', 'b', 'u', 'n', 'd', 'l', 'e', '-', 's', 'l', 'o', 't', 0,
	offVersion: formatVersion,
}

const magicReversed = "\x00tols-eldnub-sgg"

func magic() []byte {
	m := []byte(magicReversed)
	slices.Reverse(m)
	return m
}

// Bundle is a bundled configuration.
type Bundle struct {
	Name   string `json:"name"`   // app name (config/cache/service names)
	Config string `json:"config"` // TOML text
}

// Current returns the configuration bundled into the running executable,
// or nil when there is none.
func Current() (*Bundle, error) {
	s := slot // copy: read the live data once
	return decode(s[:])
}

// Read returns the bundle in an executable file's bytes, or nil if its slot
// is empty.
func Read(bin []byte) (*Bundle, error) {
	off, err := find(bin)
	if err != nil {
		return nil, err
	}
	return decode(bin[off : off+SlotSize])
}

// Write returns a copy of bin with b in its slot (replacing any previous
// bundle), re-signed when it is a signed Apple Silicon executable.
func Write(bin []byte, b Bundle) ([]byte, error) {
	off, err := find(bin)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	if len(payload) > Capacity {
		return nil, fmt.Errorf("configuration too large to bundle (%d bytes, maximum %d)", len(payload), Capacity)
	}
	out := bytes.Clone(bin)
	s := out[off : off+SlotSize]
	clear(s[magicLen:])
	s[offVersion] = formatVersion
	s[offState] = stateFilled
	binary.LittleEndian.PutUint32(s[offLength:], uint32(len(payload)))
	nonce := s[offNonce : offNonce+16]
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(payload)
	copy(s[offChecksum:], sum[:])
	copy(s[offPayload:], payload)
	xorStream(s[offPayload:offPayload+len(payload)], nonce)

	if err := resignMachO(out); err != nil {
		return nil, err
	}
	return out, nil
}

func decode(s []byte) (*Bundle, error) {
	if !bytes.Equal(s[:magicLen], magic()) {
		return nil, errors.New("bundle slot damaged")
	}
	if s[offVersion] != formatVersion {
		return nil, fmt.Errorf("bundle format %d is not supported by this version", s[offVersion])
	}
	if s[offState] == stateEmpty {
		return nil, nil
	}
	n := int(binary.LittleEndian.Uint32(s[offLength:]))
	if n > Capacity {
		return nil, errors.New("bundle damaged (bad length)")
	}
	payload := bytes.Clone(s[offPayload : offPayload+n])
	xorStream(payload, s[offNonce:offNonce+16])
	if sum := sha256.Sum256(payload); !bytes.Equal(sum[:], s[offChecksum:offChecksum+32]) {
		return nil, errors.New("bundle damaged (checksum mismatch)")
	}
	var b Bundle
	if err := json.Unmarshal(payload, &b); err != nil {
		return nil, fmt.Errorf("bundle damaged: %w", err)
	}
	return &b, nil
}

// find locates the single slot in an executable's bytes.
func find(bin []byte) (int, error) {
	m := magic()
	off := bytes.Index(bin, m)
	if off < 0 {
		return 0, errors.New("this executable has no bundle slot (is it a go-git-serve build from a version with bundling?)")
	}
	if bytes.Index(bin[off+1:], m) >= 0 {
		return 0, errors.New("this executable has more than one bundle slot")
	}
	if off+SlotSize > len(bin) {
		return 0, errors.New("bundle slot truncated")
	}
	return off, nil
}

// obfuscationKey is mixed into the keystream. It is not a secret.
const obfuscationKey = "go-git-serve bundle obfuscation v1"

// xorStream XORs data with a SHA-256 based keystream derived from nonce.
func xorStream(data, nonce []byte) {
	var counter [8]byte
	for i := 0; i < len(data); i += sha256.Size {
		binary.LittleEndian.PutUint64(counter[:], uint64(i/sha256.Size))
		h := sha256.New()
		h.Write([]byte(obfuscationKey))
		h.Write(nonce)
		h.Write(counter[:])
		block := h.Sum(nil)
		for j := 0; j < len(block) && i+j < len(data); j++ {
			data[i+j] ^= block[j]
		}
	}
}

// Platform identifies an executable's OS from its header.
func Platform(bin []byte) string {
	switch {
	case len(bin) >= 2 && bin[0] == 'M' && bin[1] == 'Z':
		return "windows"
	case len(bin) >= 4 && string(bin[:4]) == "\x7fELF":
		return "linux"
	case len(bin) >= 4 && (binary.LittleEndian.Uint32(bin) == 0xfeedfacf || binary.BigEndian.Uint32(bin) == 0xcafebabe):
		return "darwin"
	}
	return "unknown"
}

package bundle

import (
	"bytes"
	"crypto/sha256"
	"debug/macho"
	"encoding/binary"
	"errors"
	"fmt"
)

// Apple Silicon requires every executable to carry a valid code signature.
// Go's linker gives darwin/arm64 binaries an ad-hoc signature: one
// CodeDirectory holding the SHA-256 of each 4 KiB page of the file up to
// the signature (Go's cmd/internal/codesign). Filling the bundle slot
// changes those pages, so their hashes are recomputed and written back in
// place. Nothing else in the signature depends on the file contents.
//
// Anything other than such a linker ad-hoc signature (e.g. a real
// Developer ID signature) is refused rather than silently invalidated.

const (
	lcCodeSignature       = 0x1d
	csMagicEmbeddedSig    = 0xfade0cc0
	csMagicCodeDirectory  = 0xfade0c02
	csSlotCodeDirectory   = 0
	csAdhoc               = 0x2
	csHashTypeSHA256      = 2
	csPageSizeBits        = 12
	machoMagic64LE        = 0xfeedfacf
	machoFatMagicBE       = 0xcafebabe
	codeDirectoryMinBytes = 40
)

// resignMachO recomputes the ad-hoc signature of a 64-bit Mach-O file in
// place. Other formats and unsigned Mach-O files (darwin/amd64) are left
// untouched.
func resignMachO(bin []byte) error {
	if len(bin) < 4 {
		return nil
	}
	if binary.BigEndian.Uint32(bin) == machoFatMagicBE {
		return errors.New("universal (fat) macOS binaries are not supported; use the arm64 or amd64 build")
	}
	if binary.LittleEndian.Uint32(bin) != machoMagic64LE {
		return nil
	}
	f, err := macho.NewFile(bytes.NewReader(bin))
	if err != nil {
		return fmt.Errorf("reading macOS executable: %w", err)
	}
	var dataOff, dataSize uint32
	found := false
	for _, l := range f.Loads {
		raw := l.Raw()
		if len(raw) >= 16 && f.ByteOrder.Uint32(raw) == lcCodeSignature {
			dataOff, dataSize = f.ByteOrder.Uint32(raw[8:]), f.ByteOrder.Uint32(raw[12:])
			found = true
		}
	}
	if !found {
		return nil // unsigned (Intel builds are not signed by Go)
	}
	if uint64(dataOff)+uint64(dataSize) > uint64(len(bin)) {
		return errors.New("macOS code signature lies outside the file")
	}
	sig := bin[dataOff : dataOff+dataSize]
	be := binary.BigEndian
	if len(sig) < 20 || be.Uint32(sig) != csMagicEmbeddedSig {
		return errors.New("unrecognised macOS code signature")
	}
	if count := be.Uint32(sig[8:]); count != 1 || be.Uint32(sig[12:]) != csSlotCodeDirectory {
		return errors.New("the macOS executable has a full (non ad-hoc) code signature; bundling would break it")
	}
	cdOff := be.Uint32(sig[16:])
	if uint64(cdOff)+codeDirectoryMinBytes > uint64(len(sig)) {
		return errors.New("macOS code directory truncated")
	}
	cd := sig[cdOff:]
	var (
		cdMagic    = be.Uint32(cd[0:])
		flags      = be.Uint32(cd[12:])
		hashOffset = be.Uint32(cd[16:])
		nSpecial   = be.Uint32(cd[24:])
		nCode      = be.Uint32(cd[28:])
		codeLimit  = be.Uint32(cd[32:])
		hashSize   = cd[36]
		hashType   = cd[37]
		pageBits   = cd[39]
	)
	switch {
	case cdMagic != csMagicCodeDirectory:
		return errors.New("unrecognised macOS code directory")
	case flags&csAdhoc == 0:
		return errors.New("the macOS executable is not ad-hoc signed; bundling would break its signature")
	case hashType != csHashTypeSHA256 || hashSize != sha256.Size || pageBits != csPageSizeBits || nSpecial != 0:
		return errors.New("unsupported macOS code signature format")
	case codeLimit != dataOff:
		return errors.New("unexpected macOS code signature layout")
	}
	const pageSize = 1 << csPageSizeBits
	if want := (uint64(codeLimit) + pageSize - 1) / pageSize; uint64(nCode) != want {
		return errors.New("macOS code signature page count mismatch")
	}
	if uint64(hashOffset)+uint64(nCode)*sha256.Size > uint64(len(cd)) {
		return errors.New("macOS code signature hashes truncated")
	}
	for i := uint32(0); i < nCode; i++ {
		start := uint64(i) * pageSize
		end := min(start+pageSize, uint64(codeLimit))
		sum := sha256.Sum256(bin[start:end])
		copy(cd[uint64(hashOffset)+uint64(i)*sha256.Size:], sum[:])
	}
	return nil
}

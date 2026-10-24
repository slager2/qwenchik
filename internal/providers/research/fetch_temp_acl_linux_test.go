//go:build linux

package research

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestFetchTempRootRejectsExtendedPOSIXACLs(t *testing.T) {
	for _, attribute := range []string{"system.posix_acl_access", "system.posix_acl_default"} {
		t.Run(attribute, func(t *testing.T) {
			rootPath := filepath.Join(t.TempDir(), "fetch-temp")
			if err := os.Mkdir(rootPath, 0700); err != nil {
				t.Fatal(err)
			}
			root, err := os.Open(rootPath)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			if err := unix.Fsetxattr(int(root.Fd()), attribute, extendedPOSIXACL(), 0); err != nil {
				if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP) {
					t.Skipf("filesystem does not support POSIX ACLs: %v", err)
				}
				t.Fatal(err)
			}
			before, err := root.Stat()
			if err != nil || before.Mode().Perm() != 0700 {
				t.Fatalf("fixture ACL changed root mode: mode=%#o err=%v", before.Mode().Perm(), err)
			}
			if opened, err := openFetchRoot(rootPath); err == nil {
				opened.Close()
				t.Fatal("fetch root with an extended POSIX ACL was accepted")
			}
			after, err := root.Stat()
			if err != nil || after.Mode().Perm() != before.Mode().Perm() {
				t.Fatalf("rejected ACL root changed mode: before=%#o after=%#o err=%v", before.Mode().Perm(), after.Mode().Perm(), err)
			}
		})
	}
}

func extendedPOSIXACL() []byte {
	entries := []struct {
		tag  uint16
		perm uint16
		id   uint32
	}{
		{tag: 0x01, perm: 0x07, id: ^uint32(0)}, // ACL_USER_OBJ
		{tag: 0x02, perm: 0x04, id: 65534},      // ACL_USER
		{tag: 0x04, perm: 0x00, id: ^uint32(0)}, // ACL_GROUP_OBJ
		{tag: 0x10, perm: 0x00, id: ^uint32(0)}, // ACL_MASK
		{tag: 0x20, perm: 0x00, id: ^uint32(0)}, // ACL_OTHER
	}
	encoded := make([]byte, 4+8*len(entries))
	binary.LittleEndian.PutUint32(encoded, 0x0002)
	for index, entry := range entries {
		offset := 4 + 8*index
		binary.LittleEndian.PutUint16(encoded[offset:], entry.tag)
		binary.LittleEndian.PutUint16(encoded[offset+2:], entry.perm)
		binary.LittleEndian.PutUint32(encoded[offset+4:], entry.id)
	}
	return encoded
}

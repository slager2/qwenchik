//go:build linux

package research

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func validateFetchTempACL(root *os.File) error {
	// Reject even masked ACL entries so inherited permissions cannot become
	// effective if the root's mode is later changed by an administrator.
	for _, attribute := range []string{"system.posix_acl_access", "system.posix_acl_default"} {
		_, err := unix.Fgetxattr(int(root.Fd()), attribute, nil)
		switch {
		case err == nil:
			return errors.New("fetch temp root must not have extended POSIX ACLs")
		case errors.Is(err, unix.ENODATA), errors.Is(err, unix.ENOTSUP):
			continue
		default:
			return errors.New("fetch temp ACLs could not be verified")
		}
	}
	return nil
}

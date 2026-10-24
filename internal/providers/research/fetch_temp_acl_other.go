//go:build !linux && !windows

package research

import (
	"errors"
	"os"
)

func validateFetchTempACL(*os.File) error {
	return errors.New("fetch temp ACL validation is unsupported on this Unix platform")
}

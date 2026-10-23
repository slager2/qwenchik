//go:build windows

package workerlock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var ErrAlreadyRunning = errors.New("another qagent worker is already using this database")

type Lock struct {
	file       *os.File
	overlapped windows.Overlapped
}

func Acquire(path string) (*Lock, error) {
	if path == "" {
		return nil, errors.New("worker lock path is required")
	}
	if err := rejectParentTraversal(path); err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve worker lock path: %w", err)
	}
	volume := filepath.VolumeName(absolute)
	if !isDriveLetterVolume(volume) || !filepath.IsAbs(absolute) || len(absolute) < 3 || !isPathSeparator(absolute[2]) {
		return nil, errors.New("worker lock requires an absolute local drive path")
	}
	components := splitPathComponents(absolute[3:])
	if len(components) == 0 {
		return nil, errors.New("worker lock path must name a file below the drive root")
	}
	for _, component := range components {
		if err := validatePathComponent(component); err != nil {
			return nil, err
		}
	}

	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil || !user.User.Sid.IsValid() {
		if err == nil {
			err = errors.New("current Windows user SID is unavailable")
		}
		return nil, err
	}
	userSID := user.User.Sid.String()
	if userSID == "" {
		return nil, errors.New("current Windows user SID could not be formatted")
	}
	security, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;" + userSID + ")")
	if err != nil {
		return nil, fmt.Errorf("create private worker lock ACL: %w", err)
	}

	parent, err := openVerifiedDriveRoot(volume)
	if err != nil {
		return nil, fmt.Errorf("open worker lock drive root: %w", err)
	}
	for _, component := range components[:len(components)-1] {
		next, err := openDirectoryRelative(parent, component)
		if err != nil {
			_ = windows.CloseHandle(parent)
			return nil, fmt.Errorf("open worker lock ancestor %q: %w", component, err)
		}
		if err := verifyWorkerLockDirectory(next); err != nil {
			_ = windows.CloseHandle(next)
			_ = windows.CloseHandle(parent)
			return nil, fmt.Errorf("verify worker lock ancestor %q: %w", component, err)
		}
		if err := windows.CloseHandle(parent); err != nil {
			_ = windows.CloseHandle(next)
			return nil, fmt.Errorf("close worker lock ancestor handle: %w", err)
		}
		parent = next
	}

	handle, err := openWorkerLockFileRelative(parent, components[len(components)-1], security)
	parentCloseErr := windows.CloseHandle(parent)
	if err != nil {
		return nil, fmt.Errorf("open worker lock: %w", err)
	}
	if parentCloseErr != nil {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("close worker lock parent handle: %w", parentCloseErr)
	}
	if err := verifyWorkerLockFile(handle, user.User.Sid); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	file := os.NewFile(uintptr(handle), absolute)

	lock := &Lock{file: file}
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK | windows.LOCKFILE_FAIL_IMMEDIATELY)
	if err := windows.LockFileEx(windows.Handle(file.Fd()), flags, 0, 1, 0, &lock.overlapped); err != nil {
		_ = file.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, ErrAlreadyRunning
		}
		return nil, fmt.Errorf("acquire worker lock: %w", err)
	}
	return lock, nil
}

func isDriveLetterVolume(volume string) bool {
	return len(volume) == 2 && volume[1] == ':' &&
		(volume[0] >= 'A' && volume[0] <= 'Z' || volume[0] >= 'a' && volume[0] <= 'z')
}

func isPathSeparator(char byte) bool {
	return char == '\\' || char == '/'
}

func splitPathComponents(path string) []string {
	return strings.FieldsFunc(path, func(r rune) bool { return r == '\\' || r == '/' })
}

func rejectParentTraversal(path string) error {
	if isPathSeparator(path[len(path)-1]) {
		return errors.New("worker lock path must name a file, not a directory")
	}
	for _, component := range splitPathComponents(path) {
		switch component {
		case "..":
			return errors.New("worker lock path must not contain parent traversal")
		case ".":
			// filepath.Abs will normalize harmless current-directory components.
		}
	}
	return nil
}

func validatePathComponent(component string) error {
	if component == "" || component == "." || component == ".." || strings.ContainsAny(component, `<>:"|?*`) {
		return fmt.Errorf("invalid worker lock path component %q", component)
	}
	if strings.HasSuffix(component, ".") || strings.HasSuffix(component, " ") {
		return fmt.Errorf("invalid worker lock path component %q", component)
	}
	for _, char := range component {
		if char < 0x20 {
			return fmt.Errorf("invalid worker lock path component %q", component)
		}
	}
	return nil
}

func openVerifiedDriveRoot(volume string) (windows.Handle, error) {
	root, err := openDirectoryRelative(0, `\??\`+volume+`\`)
	if err != nil {
		return 0, err
	}
	if err := verifyWorkerLockDirectory(root); err != nil {
		_ = windows.CloseHandle(root)
		return 0, err
	}
	return root, nil
}

func openDirectoryRelative(parent windows.Handle, name string) (windows.Handle, error) {
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return 0, err
	}
	attributes := windows.OBJECT_ATTRIBUTES{
		RootDirectory: parent,
		ObjectName:    objectName,
		Attributes:    windows.OBJ_CASE_INSENSITIVE,
	}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	var status windows.IO_STATUS_BLOCK
	var handle windows.Handle
	access := uint32(windows.FILE_TRAVERSE | windows.FILE_READ_ATTRIBUTES | windows.SYNCHRONIZE)
	options := uint32(windows.FILE_DIRECTORY_FILE | windows.FILE_OPEN_REPARSE_POINT | windows.FILE_SYNCHRONOUS_IO_NONALERT)
	err = windows.NtCreateFile(&handle, access, &attributes, &status, nil, windows.FILE_ATTRIBUTE_NORMAL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_OPEN, options, 0, 0)
	if err != nil {
		return 0, err
	}
	return handle, nil
}

func openWorkerLockFileRelative(parent windows.Handle, name string, security *windows.SECURITY_DESCRIPTOR) (windows.Handle, error) {
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return 0, err
	}
	attributes := windows.OBJECT_ATTRIBUTES{
		RootDirectory:      parent,
		ObjectName:         objectName,
		Attributes:         windows.OBJ_CASE_INSENSITIVE,
		SecurityDescriptor: security,
	}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	var status windows.IO_STATUS_BLOCK
	var handle windows.Handle
	access := uint32(windows.GENERIC_READ | windows.GENERIC_WRITE | windows.READ_CONTROL)
	options := uint32(windows.FILE_NON_DIRECTORY_FILE | windows.FILE_OPEN_REPARSE_POINT | windows.FILE_SYNCHRONOUS_IO_NONALERT)
	err = windows.NtCreateFile(&handle, access, &attributes, &status, nil, windows.FILE_ATTRIBUTE_NORMAL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, windows.FILE_OPEN_IF, options, 0, 0)
	if err != nil {
		return 0, err
	}
	return handle, nil
}

func verifyWorkerLockDirectory(handle windows.Handle) error {
	fileType, err := windows.GetFileType(handle)
	if err != nil {
		return fmt.Errorf("inspect worker lock directory type: %w", err)
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return fmt.Errorf("inspect worker lock directory attributes: %w", err)
	}
	if fileType != windows.FILE_TYPE_DISK || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 ||
		info.FileAttributes&(windows.FILE_ATTRIBUTE_DEVICE|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
		return errors.New("worker lock ancestor must be a regular non-reparse directory")
	}
	return nil
}

func verifyWorkerLockFile(handle windows.Handle, userSID *windows.SID) error {
	fileType, err := windows.GetFileType(handle)
	if err != nil {
		return fmt.Errorf("inspect worker lock type: %w", err)
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return fmt.Errorf("inspect worker lock attributes: %w", err)
	}
	if fileType != windows.FILE_TYPE_DISK || info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_DEVICE|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 || info.NumberOfLinks != 1 {
		return errors.New("worker lock must be a single-link regular non-reparse file")
	}
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("inspect worker lock ACL: %w", err)
	}
	if sd == nil {
		return errors.New("worker lock has no security descriptor")
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(sd)))
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !owner.Equals(userSID) {
		return errors.New("worker lock is not owned by the current user")
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("worker lock ACL is not protected")
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil || dacl.AceCount != 1 {
		return errors.New("worker lock ACL is not current-user-only")
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil || ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags != 0 || ace.Mask != windows.GENERIC_ALL {
		return errors.New("worker lock ACL is not current-user-only")
	}
	aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if !aceSID.IsValid() || !aceSID.Equals(userSID) {
		return errors.New("worker lock ACL belongs to a different user")
	}
	return nil
}

func (l *Lock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}

	err := windows.UnlockFileEx(windows.Handle(l.file.Fd()), 0, 1, 0, &l.overlapped)
	closeErr := l.file.Close()
	l.file = nil
	if err != nil {
		return fmt.Errorf("release worker lock: %w", err)
	}
	return closeErr
}

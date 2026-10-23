//go:build windows

package artifacts

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
	"unsafe"

	"github.com/slager2/qwenchik/internal/domain"
	"golang.org/x/sys/windows"
)

var safeSegment = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

const (
	winFileAddFile         = 0x00000002
	winFileAddSubdirectory = 0x00000004
	winFileDeleteChild     = 0x00000040
	winFileAllAccess       = 0x001F01FF
)

type Store struct {
	root     string
	maxBytes int64
}

type winSecurityDescriptor struct {
	revision byte
	reserved byte
	control  uint16
	owner    *windows.SID
	group    *windows.SID
	sacl     *windows.ACL
	dacl     *windows.ACL
}

type privateSecurity struct {
	descriptor winSecurityDescriptor
	acl        []byte
	sid        []byte
}

func currentUserSID() ([]byte, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil || !user.User.Sid.IsValid() {
		if err == nil {
			err = errors.New("current Windows user SID is unavailable")
		}
		return nil, err
	}
	length := user.User.Sid.Len()
	if length < 8 {
		return nil, errors.New("invalid current Windows user SID")
	}
	sid := make([]byte, length)
	copy(sid, unsafe.Slice((*byte)(unsafe.Pointer(user.User.Sid)), length))
	return sid, nil
}

func newPrivateSecurity() (*privateSecurity, error) {
	sid, err := currentUserSID()
	if err != nil {
		return nil, err
	}
	// ACL: one non-inheritable FILE_ALL_ACCESS allow ACE for the current user.
	aceSize := 8 + len(sid)
	acl := make([]byte, 8+aceSize)
	acl[0] = 2 // ACL_REVISION
	binary.LittleEndian.PutUint16(acl[2:4], uint16(len(acl)))
	binary.LittleEndian.PutUint16(acl[4:6], 1)
	acl[8] = 0 // ACCESS_ALLOWED_ACE_TYPE
	binary.LittleEndian.PutUint16(acl[10:12], uint16(aceSize))
	binary.LittleEndian.PutUint32(acl[12:16], winFileAllAccess)
	copy(acl[16:], sid)

	sec := &privateSecurity{acl: acl, sid: sid}
	sec.descriptor = winSecurityDescriptor{
		revision: 1,
		control:  uint16(windows.SE_DACL_PRESENT | windows.SE_DACL_PROTECTED),
		owner:    (*windows.SID)(unsafe.Pointer(&sec.sid[0])),
		dacl:     (*windows.ACL)(unsafe.Pointer(&sec.acl[0])),
	}
	return sec, nil
}

func (s *privateSecurity) native() *windows.SECURITY_DESCRIPTOR {
	return (*windows.SECURITY_DESCRIPTOR)(unsafe.Pointer(&s.descriptor))
}

type winACLHeader struct {
	revision  byte
	reserved  byte
	size      uint16
	aces      uint16
	reserved2 uint16
}

type winACEHeader struct {
	typ   byte
	flags byte
	size  uint16
	mask  uint32
}

func verifyPrivateACL(handle windows.Handle) error {
	userSID, err := currentUserSID()
	if err != nil {
		return err
	}
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read Windows ACL: %w", err)
	}
	if sd == nil {
		return errors.New("Windows storage object has no security descriptor")
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(sd)))
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !bytes.Equal(unsafe.Slice((*byte)(unsafe.Pointer(owner)), owner.Len()), userSID) {
		return errors.New("Windows storage object is not owned by the current user")
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("Windows storage ACL is not protected")
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return errors.New("Windows storage object has no private ACL")
	}
	header := (*winACLHeader)(unsafe.Pointer(dacl))
	if header.revision != 2 || header.aces != 1 || int(header.size) != 8+8+len(userSID) {
		return errors.New("Windows storage ACL grants access beyond the current user")
	}
	ace := (*winACEHeader)(unsafe.Add(unsafe.Pointer(dacl), unsafe.Sizeof(*header)))
	if ace.typ != 0 || ace.flags != 0 || int(ace.size) != 8+len(userSID) || ace.mask != winFileAllAccess {
		return errors.New("Windows storage ACL is not current-user-only")
	}
	aceSID := unsafe.Slice((*byte)(unsafe.Add(unsafe.Pointer(ace), unsafe.Sizeof(*ace))), len(userSID))
	if !bytes.Equal(aceSID, userSID) {
		return errors.New("Windows storage ACL belongs to a different user")
	}
	return nil
}

func setPrivateACL(handle windows.Handle) error {
	if err := verifyPrivateOwner(handle); err != nil {
		return err
	}
	sec, err := newPrivateSecurity()
	if err != nil {
		return err
	}
	acl := (*windows.ACL)(unsafe.Pointer(&sec.acl[0]))
	err = windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil)
	runtime.KeepAlive(sec)
	if err != nil {
		return fmt.Errorf("set current-user-only Windows ACL: %w", err)
	}
	return verifyPrivateACL(handle)
}

func verifyPrivateOwner(handle windows.Handle) error {
	userSID, err := currentUserSID()
	if err != nil {
		return err
	}
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read Windows storage owner: %w", err)
	}
	if sd == nil {
		return errors.New("Windows storage object has no security descriptor")
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(sd)))
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !bytes.Equal(unsafe.Slice((*byte)(unsafe.Pointer(owner)), owner.Len()), userSID) {
		return errors.New("Windows storage object is not owned by the current user")
	}
	return nil
}

func winNTName(name string) (*windows.NTUnicodeString, error) {
	return windows.NewNTUnicodeString(name)
}

func ntCreateAt(parent windows.Handle, name string, access, share, disposition, options, attributes uint32, security *privateSecurity) (windows.Handle, windows.IO_STATUS_BLOCK, error) {
	ntName, err := winNTName(name)
	if err != nil {
		return 0, windows.IO_STATUS_BLOCK{}, err
	}
	oa := &windows.OBJECT_ATTRIBUTES{
		RootDirectory: parent,
		ObjectName:    ntName,
		Attributes:    windows.OBJ_CASE_INSENSITIVE,
	}
	oa.Length = uint32(unsafe.Sizeof(*oa))
	if security != nil {
		oa.SecurityDescriptor = security.native()
	}
	var handle windows.Handle
	var iosb windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&handle, access, oa, &iosb, nil, attributes, share, disposition, options, 0, 0)
	runtime.KeepAlive(ntName)
	runtime.KeepAlive(security)
	if err != nil && handle != 0 {
		_ = windows.CloseHandle(handle)
		handle = 0
	}
	return handle, iosb, err
}

func ntOpenVolumeRoot(volume string, access uint32) (windows.Handle, error) {
	name, err := winNTName(`\??\` + volume + `\`)
	if err != nil {
		return 0, err
	}
	oa := &windows.OBJECT_ATTRIBUTES{ObjectName: name, Attributes: windows.OBJ_CASE_INSENSITIVE}
	oa.Length = uint32(unsafe.Sizeof(*oa))
	var handle windows.Handle
	var iosb windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&handle, access, oa, &iosb, nil, windows.FILE_ATTRIBUTE_DIRECTORY,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN,
		windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT,
		0, 0)
	runtime.KeepAlive(name)
	if err != nil {
		if handle != 0 {
			_ = windows.CloseHandle(handle)
		}
		return 0, err
	}
	if err := verifyDirectoryHandle(handle); err != nil {
		_ = windows.CloseHandle(handle)
		return 0, err
	}
	return handle, nil
}

const (
	winDirectoryReadAccess = windows.FILE_LIST_DIRECTORY | windows.FILE_TRAVERSE | windows.FILE_READ_ATTRIBUTES | windows.READ_CONTROL | windows.SYNCHRONIZE
	winDirectoryFullAccess = winDirectoryReadAccess | winFileAddFile | winFileAddSubdirectory | winFileDeleteChild | windows.DELETE | windows.WRITE_DAC | windows.FILE_WRITE_ATTRIBUTES
)

func verifyDirectoryHandle(handle windows.Handle) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("storage path contains a Windows reparse point")
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_DEVICE != 0 {
		return errors.New("storage path component is not a regular directory")
	}
	return nil
}

func openDirectoryChild(parent windows.Handle, name string, access uint32) (windows.Handle, error) {
	handle, _, err := ntCreateAt(parent, name, access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN,
		windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT,
		windows.FILE_ATTRIBUTE_DIRECTORY, nil)
	if err != nil {
		return 0, err
	}
	if err := verifyDirectoryHandle(handle); err != nil {
		_ = windows.CloseHandle(handle)
		return 0, err
	}
	return handle, nil
}

func createDirectoryChild(parent windows.Handle, name string) (windows.Handle, error) {
	sec, err := newPrivateSecurity()
	if err != nil {
		return 0, err
	}
	handle, _, err := ntCreateAt(parent, name, winDirectoryFullAccess,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_CREATE,
		windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT,
		windows.FILE_ATTRIBUTE_DIRECTORY, sec)
	runtime.KeepAlive(sec)
	if err != nil {
		return 0, err
	}
	if err := verifyDirectoryHandle(handle); err != nil {
		_ = windows.CloseHandle(handle)
		return 0, err
	}
	if err := verifyPrivateACL(handle); err != nil {
		_ = windows.CloseHandle(handle)
		return 0, err
	}
	return handle, nil
}

func isNotFound(err error) bool {
	return err == windows.STATUS_OBJECT_NAME_NOT_FOUND || err == windows.STATUS_OBJECT_PATH_NOT_FOUND
}

func isNameCollision(err error) bool { return err == windows.STATUS_OBJECT_NAME_COLLISION }

func absoluteWindowsComponents(path string) (string, []string, string, error) {
	if path == "" {
		return "", nil, "", errors.New("storage directory is required")
	}
	if volume := filepath.VolumeName(path); volume != "" && !filepath.IsAbs(path) {
		return "", nil, "", errors.New("drive-relative storage paths are not supported")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", nil, "", err
	}
	absolute = filepath.Clean(absolute)
	volume := filepath.VolumeName(absolute)
	if len(volume) != 2 || volume[1] != ':' || !((volume[0] >= 'A' && volume[0] <= 'Z') || (volume[0] >= 'a' && volume[0] <= 'z')) {
		return "", nil, "", errors.New("storage requires an absolute local drive path")
	}
	relative := strings.TrimLeft(strings.TrimPrefix(absolute, volume), `\`)
	if relative == "" {
		return absolute, nil, volume, nil
	}
	parts := strings.Split(relative, `\`)
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "/:\x00") {
			return "", nil, "", errors.New("invalid Windows storage path component")
		}
	}
	return absolute, parts, volume, nil
}

func openDirectory(path string, create bool) (*os.File, error) {
	absolute, parts, volume, err := absoluteWindowsComponents(path)
	if err != nil {
		return nil, err
	}
	if len(parts) == 0 {
		return nil, errors.New("a drive root cannot be used as private storage")
	}
	root, err := ntOpenVolumeRoot(volume, winDirectoryReadAccess)
	if err != nil {
		return nil, err
	}
	handles := []windows.Handle{root}
	closeHandles := func() {
		for i := len(handles) - 1; i >= 0; i-- {
			_ = windows.CloseHandle(handles[i])
		}
	}
	for index, part := range parts {
		parent := handles[len(handles)-1]
		final := index == len(parts)-1
		access := uint32(winDirectoryFullAccess)
		next, openErr := openDirectoryChild(parent, part, access)
		if openErr == windows.STATUS_ACCESS_DENIED && !final {
			next, openErr = openDirectoryChild(parent, part, winDirectoryReadAccess)
		}
		if isNotFound(openErr) && create {
			next, openErr = createDirectoryChild(parent, part)
			if openErr == windows.STATUS_ACCESS_DENIED {
				// Reopen the parent with creation rights using its already-verified ancestor handle.
				var elevated windows.Handle
				if index == 0 {
					elevated, openErr = ntOpenVolumeRoot(volume, winDirectoryFullAccess)
				} else {
					elevated, openErr = openDirectoryChild(handles[index-1], parts[index-1], winDirectoryFullAccess)
				}
				if openErr == nil {
					next, openErr = createDirectoryChild(elevated, part)
					_ = windows.CloseHandle(elevated)
				}
			}
			if isNameCollision(openErr) {
				next, openErr = openDirectoryChild(parent, part, access)
				if openErr == windows.STATUS_ACCESS_DENIED && !final {
					next, openErr = openDirectoryChild(parent, part, winDirectoryReadAccess)
				}
			}
		}
		if openErr != nil {
			closeHandles()
			return nil, openErr
		}
		handles = append(handles, next)
	}
	finalHandle := handles[len(handles)-1]
	if err := verifyPrivateACL(finalHandle); err != nil {
		closeHandles()
		return nil, err
	}
	file := os.NewFile(uintptr(finalHandle), absolute)
	handles = handles[:len(handles)-1]
	closeHandles()
	return file, nil
}

func New(root string, maxBytes int64) (*Store, error) {
	if root == "" {
		return nil, errors.New("artifact root is required")
	}
	if maxBytes < 1 {
		maxBytes = 10_000_000
	}
	if maxBytes == math.MaxInt64 {
		return nil, errors.New("invalid artifact limit")
	}
	directory, err := openDirectory(root, true)
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	return &Store{root: directory.Name(), maxBytes: maxBytes}, nil
}

func (s *Store) taskDirectory(task string, create bool) (*os.File, error) {
	if !safeSegment.MatchString(task) {
		return nil, errors.New("invalid task identifier")
	}
	return openDirectory(filepath.Join(s.root, task), create)
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func openArtifactFile(dir *os.File, name string, access uint32) (windows.Handle, error) {
	handle, _, err := ntCreateAt(windows.Handle(dir.Fd()), name, access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN,
		windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT,
		windows.FILE_ATTRIBUTE_NORMAL, nil)
	if err != nil {
		return 0, err
	}
	if err := verifyRegularFile(handle); err != nil {
		_ = windows.CloseHandle(handle)
		return 0, err
	}
	if err := verifyPrivateACL(handle); err != nil {
		_ = windows.CloseHandle(handle)
		return 0, err
	}
	return handle, nil
}

func createArtifactFile(dir *os.File, name string) (windows.Handle, error) {
	sec, err := newPrivateSecurity()
	if err != nil {
		return 0, err
	}
	handle, _, err := ntCreateAt(windows.Handle(dir.Fd()), name,
		windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE|windows.DELETE|windows.READ_CONTROL|windows.WRITE_DAC,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_CREATE,
		windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT,
		windows.FILE_ATTRIBUTE_TEMPORARY, sec)
	runtime.KeepAlive(sec)
	if err != nil {
		return 0, err
	}
	if err := verifyRegularFile(handle); err != nil {
		_ = windows.CloseHandle(handle)
		return 0, err
	}
	if err := verifyPrivateACL(handle); err != nil {
		_ = windows.CloseHandle(handle)
		return 0, err
	}
	return handle, nil
}

func openArtifactLock(dir *os.File) (*os.File, error) {
	const name = ".artifact.lock"
	h, err := openArtifactFileNoACLMutation(dir, name)
	if isNotFound(err) {
		sec, secErr := newPrivateSecurity()
		if secErr != nil {
			return nil, secErr
		}
		h, _, err = ntCreateAt(windows.Handle(dir.Fd()), name,
			windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE|windows.READ_CONTROL|windows.WRITE_DAC,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
			windows.FILE_CREATE,
			windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT,
			windows.FILE_ATTRIBUTE_NORMAL, sec)
		runtime.KeepAlive(sec)
		if isNameCollision(err) {
			h, err = openArtifactFileNoACLMutation(dir, name)
		}
	}
	if err != nil {
		return nil, err
	}
	if err := verifyRegularFile(h); err != nil {
		_ = windows.CloseHandle(h)
		return nil, err
	}
	if err := verifyPrivateACL(h); err != nil {
		_ = windows.CloseHandle(h)
		return nil, err
	}
	return os.NewFile(uintptr(h), name), nil
}

func openArtifactFileNoACLMutation(dir *os.File, name string) (windows.Handle, error) {
	h, _, err := ntCreateAt(windows.Handle(dir.Fd()), name,
		windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE|windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		windows.FILE_OPEN,
		windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT,
		windows.FILE_ATTRIBUTE_NORMAL, nil)
	if err != nil {
		return 0, err
	}
	if err := verifyRegularFile(h); err != nil {
		_ = windows.CloseHandle(h)
		return 0, err
	}
	return h, nil
}

func verifyRegularFile(handle windows.Handle) error {
	typ, err := windows.GetFileType(handle)
	if err != nil {
		return err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return err
	}
	if typ != windows.FILE_TYPE_DISK || info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_DEVICE|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
		return errors.New("artifact is not a regular non-reparse file")
	}
	return nil
}

func lockArtifact(ctx context.Context, handle windows.Handle) (*windows.Overlapped, error) {
	overlapped := &windows.Overlapped{}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		err := windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, overlapped)
		if err == nil {
			return overlapped, nil
		}
		if err != windows.ERROR_LOCK_VIOLATION && err != windows.ERROR_IO_PENDING {
			return nil, err
		}
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func diskFreeBytes(directory *os.File) (uint64, error) {
	buffer := make([]uint16, windows.MAX_LONG_PATH)
	length, err := windows.GetFinalPathNameByHandle(windows.Handle(directory.Fd()), &buffer[0], uint32(len(buffer)), 0)
	if err != nil {
		return 0, err
	}
	if length == 0 || int(length) >= len(buffer) {
		return 0, errors.New("cannot resolve verified storage directory")
	}
	var available uint64
	if err := windows.GetDiskFreeSpaceEx(&buffer[0], &available, nil, nil); err != nil {
		return 0, err
	}
	return available, nil
}

type freeSpaceWriter struct {
	file      *os.File
	directory *os.File
}

func (w freeSpaceWriter) Write(p []byte) (int, error) {
	available, err := diskFreeBytes(w.directory)
	if err != nil {
		return 0, err
	}
	if uint64(len(p)) > available {
		if available == 0 {
			return 0, errors.New("insufficient artifact disk space")
		}
		if available < uint64(len(p)) {
			p = p[:int(available)]
		}
		n, writeErr := w.file.Write(p)
		if writeErr != nil {
			return n, writeErr
		}
		return n, errors.New("insufficient artifact disk space")
	}
	return w.file.Write(p)
}

func renameNoReplace(file *os.File, directory *os.File, name string) error {
	nameUTF16, err := windows.UTF16FromString(name)
	if err != nil {
		return err
	}
	nameUTF16 = nameUTF16[:len(nameUTF16)-1]
	var layout struct {
		ReplaceIfExists uint8
		RootDirectory   windows.Handle
		FileNameLength  uint32
		FileName        [1]uint16
	}
	nameOffset := int(unsafe.Offsetof(layout.FileName))
	buffer := make([]byte, nameOffset+len(nameUTF16)*2)
	info := (*struct {
		ReplaceIfExists uint8
		RootDirectory   windows.Handle
		FileNameLength  uint32
		FileName        [1]uint16
	})(unsafe.Pointer(&buffer[0]))
	info.ReplaceIfExists = 0
	info.RootDirectory = windows.Handle(directory.Fd())
	info.FileNameLength = uint32(len(nameUTF16) * 2)
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(&info.FileName[0])), len(nameUTF16)), nameUTF16)
	var iosb windows.IO_STATUS_BLOCK
	err = windows.NtSetInformationFile(windows.Handle(file.Fd()), &iosb, &buffer[0], uint32(len(buffer)), windows.FileRenameInformation)
	runtime.KeepAlive(nameUTF16)
	runtime.KeepAlive(buffer)
	return err
}

func markFileForDeletion(handle windows.Handle) error {
	deleteFile := uint8(1)
	var iosb windows.IO_STATUS_BLOCK
	return windows.NtSetInformationFile(handle, &iosb, (*byte)(unsafe.Pointer(&deleteFile)), uint32(unsafe.Sizeof(deleteFile)), windows.FileDispositionInformation)
}

func (s *Store) Put(ctx context.Context, taskID, name, mimeType string, content []byte) (domain.Artifact, string, error) {
	return s.PutReader(ctx, taskID, name, mimeType, bytes.NewReader(content))
}

func (s *Store) PutReader(ctx context.Context, taskID, name, mimeType string, content io.Reader) (record domain.Artifact, reference string, retErr error) {
	if name == "" || name == "." || name == ".." || len(name) > 200 || filepath.Base(name) != name {
		return domain.Artifact{}, "", errors.New("invalid artifact name")
	}
	if mimeType == "" || len(mimeType) > 200 || content == nil {
		return domain.Artifact{}, "", errors.New("invalid artifact MIME type or reader")
	}
	if err := ctx.Err(); err != nil {
		return domain.Artifact{}, "", err
	}
	dir, err := s.taskDirectory(taskID, true)
	if err != nil {
		return domain.Artifact{}, "", err
	}
	defer dir.Close()
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return domain.Artifact{}, "", err
	}
	temporaryName := ".artifact-" + hex.EncodeToString(random[:])
	handle, err := createArtifactFile(dir, temporaryName)
	if err != nil {
		return domain.Artifact{}, "", err
	}
	temporary := os.NewFile(uintptr(handle), temporaryName)
	committed := false
	defer func() {
		if !committed {
			if cleanupErr := markFileForDeletion(handle); cleanupErr != nil {
				if retErr == nil {
					retErr = fmt.Errorf("remove temporary artifact: %w", cleanupErr)
				} else {
					retErr = fmt.Errorf("%w (temporary artifact cleanup failed: %v)", retErr, cleanupErr)
				}
			}
		}
		if closeErr := temporary.Close(); closeErr != nil && retErr == nil {
			retErr = closeErr
		}
	}()
	hash := sha256.New()
	writer := io.MultiWriter(freeSpaceWriter{file: temporary, directory: dir}, hash)
	n, err := io.Copy(writer, io.LimitReader(contextReader{ctx, content}, s.maxBytes+1))
	if err != nil {
		return domain.Artifact{}, "", err
	}
	if n > s.maxBytes {
		return domain.Artifact{}, "", errors.New("artifact exceeds configured size limit")
	}
	if err := temporary.Sync(); err != nil {
		return domain.Artifact{}, "", err
	}
	hashText := hex.EncodeToString(hash.Sum(nil))
	identity := sha256.Sum256([]byte(taskID + "\x00" + mimeType + "\x00" + hashText))
	id := hex.EncodeToString(identity[:])
	if err := ctx.Err(); err != nil {
		return domain.Artifact{}, "", err
	}
	lock, err := openArtifactLock(dir)
	if err != nil {
		return domain.Artifact{}, "", err
	}
	defer lock.Close()
	lockRange, err := lockArtifact(ctx, windows.Handle(lock.Fd()))
	if err != nil {
		return domain.Artifact{}, "", err
	}
	defer windows.UnlockFileEx(windows.Handle(lock.Fd()), 0, 1, 0, lockRange)
	existingHandle, err := openArtifactFile(dir, id, windows.FILE_GENERIC_READ|windows.READ_CONTROL)
	if err == nil {
		existing := os.NewFile(uintptr(existingHandle), id)
		defer existing.Close()
		h := sha256.New()
		count, e := io.Copy(h, io.LimitReader(contextReader{ctx, existing}, s.maxBytes+1))
		if e != nil {
			return domain.Artifact{}, "", e
		}
		if count != n || hex.EncodeToString(h.Sum(nil)) != hashText {
			return domain.Artifact{}, "", errors.New("existing artifact content does not match its deterministic identifier")
		}
	} else if !isNotFound(err) {
		return domain.Artifact{}, "", err
	} else {
		if err := ctx.Err(); err != nil {
			return domain.Artifact{}, "", err
		}
		if err := renameNoReplace(temporary, dir, id); err != nil {
			return domain.Artifact{}, "", fmt.Errorf("commit artifact without overwrite: %w", err)
		}
		committed = true
	}
	record = domain.Artifact{ID: id, TaskID: taskID, Name: name, MIMEType: mimeType, SHA256: hashText, SizeBytes: n, CreatedAt: time.Now().UTC()}
	reference = filepath.Join(taskID, id)
	return record, reference, nil
}

func (s *Store) Read(ctx context.Context, taskID, artifactID string) ([]byte, error) {
	if !safeSegment.MatchString(artifactID) {
		return nil, errors.New("invalid artifact identifier")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, err := s.taskDirectory(taskID, false)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	handle, err := openArtifactFile(dir, artifactID, windows.FILE_GENERIC_READ|windows.READ_CONTROL)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), artifactID)
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(contextReader{ctx, file}, s.maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > s.maxBytes {
		return nil, errors.New("artifact exceeds configured size limit")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return content, nil
}

func (s *Store) ReadRange(ctx context.Context, taskID, artifactID string, offset int64, limit int) ([]byte, error) {
	if !safeSegment.MatchString(artifactID) || offset < 0 || limit < 1 || limit > 64<<10 {
		return nil, errors.New("invalid artifact range")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, err := s.taskDirectory(taskID, false)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	handle, err := openArtifactFile(dir, artifactID, windows.FILE_GENERIC_READ|windows.READ_CONTROL)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), artifactID)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if offset > info.Size() {
		return nil, errors.New("artifact offset exceeds size")
	}
	content, err := io.ReadAll(contextReader{ctx, io.NewSectionReader(file, offset, int64(limit))})
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return content, nil
}

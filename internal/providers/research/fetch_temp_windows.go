//go:build windows

package research

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
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var ownedFetchName = regexp.MustCompile(`^[a-f0-9]{16}-[a-f0-9]{32}$`)

const maxFetchTempEntries = 16384

const (
	fetchMarkerPrefix   = ".qwenchik-fetch-lease-"
	fetchMarkerMaxBytes = 256
)

const (
	winFetchFileAddFile         = 0x00000002
	winFetchFileAddSubdirectory = 0x00000004
	winFetchFileDeleteChild     = 0x00000040
	winFetchFileAllAccess       = 0x001F01FF
)

type fetchTemp struct {
	file       *os.File
	root       *os.File
	name       string
	markerName string
	once       sync.Once
}

func (t *fetchTemp) cleanup() {
	t.once.Do(func() {
		var dataInfo windows.ByHandleFileInformation
		dataHandle := windows.Handle(0)
		dataKnown := false
		if t.file != nil {
			fd := t.file.Fd()
			if fd != ^uintptr(0) {
				dataHandle = windows.Handle(fd)
				if info, err := fetchVerifyRegular(dataHandle); err == nil {
					dataInfo, dataKnown = info, true
				}
			}
		}

		var markerFile *os.File
		markerValid := false
		if dataKnown && t.root != nil && t.markerName == fetchMarkerName(t.name) {
			markerFile, markerValid, _ = openValidFetchMarker(t.root, t.name, dataInfo)
		}
		if dataKnown {
			if markerValid {
				if err := markFetchFileForDeletion(windows.Handle(markerFile.Fd())); err == nil {
					_ = markFetchFileForDeletion(dataHandle)
				}
			} else {
				_ = markFetchFileForDeletion(dataHandle)
			}
		} else if t.root != nil && t.name != "" {
			// A closed lease handle loses in-memory provenance. Only a matching,
			// validated marker authorizes removal by name.
			var dataFile *os.File
			handle, info, err := openFetchFileAt(t.root, t.name,
				windows.FILE_GENERIC_READ|windows.DELETE|windows.READ_CONTROL,
				windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE)
			if err == nil {
				dataFile = os.NewFile(uintptr(handle), t.name)
				if t.markerName == fetchMarkerName(t.name) {
					markerFile, markerValid, _ = openValidFetchMarker(t.root, t.name, info)
				}
				if markerValid {
					if err := markFetchFileForDeletion(windows.Handle(markerFile.Fd())); err == nil {
						_ = markFetchFileForDeletion(handle)
					}
				}
			}
			if dataFile != nil {
				_ = dataFile.Close()
			}
		}
		if markerFile != nil {
			_ = markerFile.Close()
		}
		if t.file != nil {
			_ = t.file.Close()
		}
		if t.root != nil {
			_ = t.root.Close()
		}
	})
}

type fetchWinSecurityDescriptor struct {
	revision byte
	reserved byte
	control  uint16
	owner    *windows.SID
	group    *windows.SID
	sacl     *windows.ACL
	dacl     *windows.ACL
}

type fetchPrivateSecurity struct {
	descriptor fetchWinSecurityDescriptor
	acl        []byte
	sid        []byte
}

func fetchCurrentSID() ([]byte, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil || !user.User.Sid.IsValid() {
		if err == nil {
			err = errors.New("current Windows user SID is unavailable")
		}
		return nil, err
	}
	size := user.User.Sid.Len()
	if size < 8 {
		return nil, errors.New("invalid current Windows user SID")
	}
	sid := make([]byte, size)
	copy(sid, unsafe.Slice((*byte)(unsafe.Pointer(user.User.Sid)), size))
	return sid, nil
}

func newFetchPrivateSecurity() (*fetchPrivateSecurity, error) {
	sid, err := fetchCurrentSID()
	if err != nil {
		return nil, err
	}
	aceSize := 8 + len(sid)
	acl := make([]byte, 8+aceSize)
	acl[0] = 2
	binary.LittleEndian.PutUint16(acl[2:4], uint16(len(acl)))
	binary.LittleEndian.PutUint16(acl[4:6], 1)
	acl[8] = 0
	binary.LittleEndian.PutUint16(acl[10:12], uint16(aceSize))
	binary.LittleEndian.PutUint32(acl[12:16], winFetchFileAllAccess)
	copy(acl[16:], sid)
	security := &fetchPrivateSecurity{acl: acl, sid: sid}
	security.descriptor = fetchWinSecurityDescriptor{
		revision: 1,
		control:  uint16(windows.SE_DACL_PRESENT | windows.SE_DACL_PROTECTED),
		owner:    (*windows.SID)(unsafe.Pointer(&security.sid[0])),
		dacl:     (*windows.ACL)(unsafe.Pointer(&security.acl[0])),
	}
	return security, nil
}

func (s *fetchPrivateSecurity) native() *windows.SECURITY_DESCRIPTOR {
	return (*windows.SECURITY_DESCRIPTOR)(unsafe.Pointer(&s.descriptor))
}

type fetchACLHeader struct {
	revision  byte
	reserved  byte
	size      uint16
	aces      uint16
	reserved2 uint16
}

type fetchACEHeader struct {
	typ   byte
	flags byte
	size  uint16
	mask  uint32
}

func verifyFetchPrivateACL(handle windows.Handle) error {
	sid, err := fetchCurrentSID()
	if err != nil {
		return err
	}
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read fetch temporary ACL: %w", err)
	}
	if sd == nil {
		return errors.New("fetch temporary object has no security descriptor")
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(sd)))
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !bytesEqualSID(owner, sid) {
		return errors.New("fetch temporary object is not owned by the current user")
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("fetch temporary ACL is not protected")
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return errors.New("fetch temporary object has no private ACL")
	}
	header := (*fetchACLHeader)(unsafe.Pointer(dacl))
	if header.revision != 2 || header.aces != 1 || int(header.size) != 16+len(sid) {
		return errors.New("fetch temporary ACL grants access beyond the current user")
	}
	ace := (*fetchACEHeader)(unsafe.Add(unsafe.Pointer(dacl), unsafe.Sizeof(*header)))
	if ace.typ != 0 || ace.flags != 0 || int(ace.size) != 8+len(sid) || ace.mask != winFetchFileAllAccess {
		return errors.New("fetch temporary ACL is not current-user-only")
	}
	aceSID := unsafe.Slice((*byte)(unsafe.Add(unsafe.Pointer(ace), unsafe.Sizeof(*ace))), len(sid))
	if !bytes.Equal(aceSID, sid) {
		return errors.New("fetch temporary ACL belongs to a different user")
	}
	return nil
}

func bytesEqualSID(owner *windows.SID, sid []byte) bool {
	return owner != nil && owner.Len() == len(sid) && string(unsafe.Slice((*byte)(unsafe.Pointer(owner)), owner.Len())) == string(sid)
}

func setFetchPrivateACL(handle windows.Handle) error {
	if err := verifyFetchPrivateOwner(handle); err != nil {
		return err
	}
	security, err := newFetchPrivateSecurity()
	if err != nil {
		return err
	}
	acl := (*windows.ACL)(unsafe.Pointer(&security.acl[0]))
	err = windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil)
	runtime.KeepAlive(security)
	if err != nil {
		return fmt.Errorf("set private fetch temporary ACL: %w", err)
	}
	return verifyFetchPrivateACL(handle)
}

func verifyFetchPrivateOwner(handle windows.Handle) error {
	sid, err := fetchCurrentSID()
	if err != nil {
		return err
	}
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read fetch temporary owner: %w", err)
	}
	if sd == nil {
		return errors.New("fetch temporary object has no security descriptor")
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(sd)))
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !bytesEqualSID(owner, sid) {
		return errors.New("fetch temporary object is not owned by the current user")
	}
	return nil
}

func fetchNTName(name string) (*windows.NTUnicodeString, error) {
	return windows.NewNTUnicodeString(name)
}

func fetchNtCreateAt(parent windows.Handle, name string, access, share, disposition, options, attributes uint32, security *fetchPrivateSecurity) (windows.Handle, error) {
	ntName, err := fetchNTName(name)
	if err != nil {
		return 0, err
	}
	oa := &windows.OBJECT_ATTRIBUTES{RootDirectory: parent, ObjectName: ntName, Attributes: windows.OBJ_CASE_INSENSITIVE}
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
	return handle, err
}

const (
	fetchDirectoryReadAccess = windows.FILE_LIST_DIRECTORY | windows.FILE_TRAVERSE | windows.FILE_READ_ATTRIBUTES | windows.READ_CONTROL | windows.SYNCHRONIZE
	fetchDirectoryFullAccess = fetchDirectoryReadAccess | winFetchFileAddFile | winFetchFileAddSubdirectory | winFetchFileDeleteChild | windows.DELETE | windows.WRITE_DAC | windows.FILE_WRITE_ATTRIBUTES
)

func verifyFetchDirectory(handle windows.Handle) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("fetch temporary path contains a Windows reparse point")
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_DEVICE != 0 {
		return errors.New("fetch temporary path component is not a directory")
	}
	return nil
}

func fetchOpenVolumeRoot(volume string, access uint32) (windows.Handle, error) {
	name, err := fetchNTName(`\??\` + volume + `\`)
	if err != nil {
		return 0, err
	}
	oa := &windows.OBJECT_ATTRIBUTES{ObjectName: name, Attributes: windows.OBJ_CASE_INSENSITIVE}
	oa.Length = uint32(unsafe.Sizeof(*oa))
	var handle windows.Handle
	var iosb windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&handle, access, oa, &iosb, nil, windows.FILE_ATTRIBUTE_DIRECTORY,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_OPEN,
		windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT, 0, 0)
	runtime.KeepAlive(name)
	if err != nil {
		if handle != 0 {
			_ = windows.CloseHandle(handle)
		}
		return 0, err
	}
	if err := verifyFetchDirectory(handle); err != nil {
		_ = windows.CloseHandle(handle)
		return 0, err
	}
	return handle, nil
}

func fetchOpenDirectoryChild(parent windows.Handle, name string, access uint32) (windows.Handle, error) {
	handle, err := fetchNtCreateAt(parent, name, access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN,
		windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT,
		windows.FILE_ATTRIBUTE_DIRECTORY, nil)
	if err != nil {
		return 0, err
	}
	if err := verifyFetchDirectory(handle); err != nil {
		_ = windows.CloseHandle(handle)
		return 0, err
	}
	return handle, nil
}

func fetchCreateDirectoryChild(parent windows.Handle, name string) (windows.Handle, error) {
	security, err := newFetchPrivateSecurity()
	if err != nil {
		return 0, err
	}
	handle, err := fetchNtCreateAt(parent, name, fetchDirectoryFullAccess,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_CREATE,
		windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT,
		windows.FILE_ATTRIBUTE_DIRECTORY, security)
	runtime.KeepAlive(security)
	if err != nil {
		return 0, err
	}
	if err := verifyFetchDirectory(handle); err != nil {
		_ = windows.CloseHandle(handle)
		return 0, err
	}
	if err := verifyFetchPrivateACL(handle); err != nil {
		_ = windows.CloseHandle(handle)
		return 0, err
	}
	return handle, nil
}

func fetchNotFound(err error) bool {
	return err == windows.STATUS_OBJECT_NAME_NOT_FOUND || err == windows.STATUS_OBJECT_PATH_NOT_FOUND
}

func fetchNameCollision(err error) bool { return err == windows.STATUS_OBJECT_NAME_COLLISION }

func fetchAbsoluteComponents(path string) (string, []string, string, error) {
	if path == "" {
		return "", nil, "", errors.New("fetch temporary root is required")
	}
	if volume := filepath.VolumeName(path); volume != "" && !filepath.IsAbs(path) {
		return "", nil, "", errors.New("drive-relative fetch temporary paths are unsupported")
	}
	absolute, err := resolveFetchTempPath(path)
	if err != nil {
		return "", nil, "", err
	}
	volume := filepath.VolumeName(absolute)
	if len(volume) != 2 || volume[1] != ':' || !((volume[0] >= 'A' && volume[0] <= 'Z') || (volume[0] >= 'a' && volume[0] <= 'z')) {
		return "", nil, "", errors.New("fetch temporary storage requires a local drive path")
	}
	if err := validateFetchTempRootPath(absolute); err != nil {
		return "", nil, "", err
	}
	relative := strings.TrimLeft(strings.TrimPrefix(absolute, volume), `\`)
	if relative == "" {
		return "", nil, "", errors.New("a drive root cannot be used as fetch temporary storage")
	}
	parts := strings.Split(relative, `\`)
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "/:\x00") {
			return "", nil, "", errors.New("invalid fetch temporary path component")
		}
	}
	return absolute, parts, volume, nil
}

func validateFetchTempRootPath(absolute string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if fetchTempRootContains(absolute, cwd) {
		return errors.New("fetch temp root must be a dedicated directory, not the working directory or an ancestor")
	}
	if home, err := os.UserHomeDir(); err == nil && fetchTempRootContains(absolute, home) {
		return errors.New("fetch temp root must not contain the user home directory")
	}
	return nil
}

func openFetchRoot(path string) (*os.File, error) {
	absolute, parts, volume, err := fetchAbsoluteComponents(path)
	if err != nil {
		return nil, err
	}
	root, err := fetchOpenVolumeRoot(volume, fetchDirectoryReadAccess)
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
		handle, openErr := fetchOpenDirectoryChild(parent, part, fetchDirectoryFullAccess)
		if openErr == windows.STATUS_ACCESS_DENIED && !final {
			handle, openErr = fetchOpenDirectoryChild(parent, part, fetchDirectoryReadAccess)
		}
		if fetchNotFound(openErr) {
			handle, openErr = fetchCreateDirectoryChild(parent, part)
			if openErr == windows.STATUS_ACCESS_DENIED {
				var elevated windows.Handle
				if index == 0 {
					elevated, openErr = fetchOpenVolumeRoot(volume, fetchDirectoryFullAccess)
				} else {
					elevated, openErr = fetchOpenDirectoryChild(handles[index-1], parts[index-1], fetchDirectoryFullAccess)
				}
				if openErr == nil {
					handle, openErr = fetchCreateDirectoryChild(elevated, part)
					_ = windows.CloseHandle(elevated)
				}
			}
			if fetchNameCollision(openErr) {
				handle, openErr = fetchOpenDirectoryChild(parent, part, fetchDirectoryFullAccess)
				if openErr == windows.STATUS_ACCESS_DENIED && !final {
					handle, openErr = fetchOpenDirectoryChild(parent, part, fetchDirectoryReadAccess)
				}
			}
		}
		if openErr != nil {
			closeHandles()
			return nil, openErr
		}
		handles = append(handles, handle)
	}
	finalHandle := handles[len(handles)-1]
	if err := verifyFetchPrivateACL(finalHandle); err != nil {
		closeHandles()
		return nil, err
	}
	rootFile := os.NewFile(uintptr(finalHandle), absolute)
	handles = handles[:len(handles)-1]
	closeHandles()
	return rootFile, nil
}

func fetchVerifyRegular(handle windows.Handle) (windows.ByHandleFileInformation, error) {
	typ, err := windows.GetFileType(handle)
	if err != nil {
		return windows.ByHandleFileInformation{}, err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return windows.ByHandleFileInformation{}, err
	}
	if typ != windows.FILE_TYPE_DISK || info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_DEVICE|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
		return windows.ByHandleFileInformation{}, errors.New("fetch temporary entry is not a regular non-reparse file")
	}
	return info, nil
}

func openFetchFileAt(root *os.File, name string, access uint32, share uint32) (windows.Handle, windows.ByHandleFileInformation, error) {
	handle, err := fetchNtCreateAt(windows.Handle(root.Fd()), name, access, share,
		windows.FILE_OPEN,
		windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT,
		windows.FILE_ATTRIBUTE_NORMAL, nil)
	if err != nil {
		return 0, windows.ByHandleFileInformation{}, err
	}
	info, err := fetchVerifyRegular(handle)
	if err == nil {
		err = verifyFetchPrivateACL(handle)
	}
	if err != nil {
		_ = windows.CloseHandle(handle)
		return 0, windows.ByHandleFileInformation{}, err
	}
	return handle, info, nil
}

func createFetchFileAt(root *os.File, name string) (windows.Handle, error) {
	return createFetchFileAtAttributes(root, name, windows.FILE_ATTRIBUTE_TEMPORARY)
}

func createFetchFileAtAttributes(root *os.File, name string, attributes uint32) (windows.Handle, error) {
	security, err := newFetchPrivateSecurity()
	if err != nil {
		return 0, err
	}
	handle, err := fetchNtCreateAt(windows.Handle(root.Fd()), name,
		windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE|windows.DELETE|windows.READ_CONTROL|windows.WRITE_DAC,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_CREATE,
		windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT,
		attributes, security)
	runtime.KeepAlive(security)
	if err != nil {
		return 0, err
	}
	if _, err := fetchVerifyRegular(handle); err == nil {
		err = verifyFetchPrivateACL(handle)
	}
	if err != nil {
		_ = windows.CloseHandle(handle)
		return 0, err
	}
	return handle, nil
}

func fetchMarkerName(name string) string { return fetchMarkerPrefix + name }

func fetchMarkerContents(name string, info windows.ByHandleFileInformation) ([]byte, error) {
	if name == "" || len(name) > 128 || strings.ContainsAny(name, "\r\n\x00") || info.NumberOfLinks != 1 {
		return nil, errors.New("invalid fetch lease provenance input")
	}
	sid, err := fetchCurrentSID()
	if err != nil {
		return nil, err
	}
	sidHash := sha256.Sum256(sid)
	content := []byte(fmt.Sprintf("QWFETCH-LEASE-V1\n%s\n%08x%08x%08x\n%s\n",
		name, info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow, hex.EncodeToString(sidHash[:])))
	if len(content) > fetchMarkerMaxBytes {
		return nil, errors.New("fetch lease provenance marker exceeds fixed size")
	}
	return content, nil
}

func openValidFetchMarker(root *os.File, name string, dataInfo windows.ByHandleFileInformation) (*os.File, bool, error) {
	if dataInfo.NumberOfLinks != 1 {
		return nil, false, nil
	}
	handle, markerInfo, err := openFetchFileAt(root, fetchMarkerName(name),
		windows.FILE_GENERIC_READ|windows.DELETE|windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE)
	if fetchNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	marker := os.NewFile(uintptr(handle), fetchMarkerName(name))
	markerSize := uint64(markerInfo.FileSizeHigh)<<32 | uint64(markerInfo.FileSizeLow)
	if markerInfo.NumberOfLinks != 1 || markerSize > fetchMarkerMaxBytes {
		_ = marker.Close()
		return nil, false, nil
	}
	contents, readErr := io.ReadAll(io.LimitReader(marker, fetchMarkerMaxBytes+1))
	if readErr != nil {
		_ = marker.Close()
		return nil, false, readErr
	}
	expected, expectedErr := fetchMarkerContents(name, dataInfo)
	if expectedErr != nil {
		_ = marker.Close()
		return nil, false, expectedErr
	}
	if !bytes.Equal(contents, expected) {
		_ = marker.Close()
		return nil, false, nil
	}
	return marker, true, nil
}

func renameFetchFileNoReplace(file *os.File, root *os.File, name string) error {
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
	info.RootDirectory = windows.Handle(root.Fd())
	info.FileNameLength = uint32(len(nameUTF16) * 2)
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(&info.FileName[0])), len(nameUTF16)), nameUTF16)
	var iosb windows.IO_STATUS_BLOCK
	err = windows.NtSetInformationFile(windows.Handle(file.Fd()), &iosb, &buffer[0], uint32(len(buffer)), windows.FileRenameInformation)
	runtime.KeepAlive(nameUTF16)
	runtime.KeepAlive(buffer)
	return err
}

func createFetchMarker(root *os.File, name string, dataHandle windows.Handle) (markerName string, retErr error) {
	dataInfo, err := fetchVerifyRegular(dataHandle)
	if err != nil {
		return "", err
	}
	if err := verifyFetchPrivateACL(dataHandle); err != nil {
		return "", err
	}
	contents, err := fetchMarkerContents(name, dataInfo)
	if err != nil {
		return "", err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	temporaryName := ".qwenchik-marker-tmp-" + hex.EncodeToString(random[:])
	handle, err := createFetchFileAtAttributes(root, temporaryName, windows.FILE_ATTRIBUTE_TEMPORARY|windows.FILE_ATTRIBUTE_HIDDEN)
	if err != nil {
		return "", err
	}
	file := os.NewFile(uintptr(handle), temporaryName)
	renamed := false
	defer func() {
		if !renamed {
			if cleanupErr := markFetchFileForDeletion(handle); cleanupErr != nil && retErr == nil {
				retErr = fmt.Errorf("remove incomplete fetch provenance marker: %w", cleanupErr)
			}
		}
		if closeErr := file.Close(); closeErr != nil && retErr == nil {
			retErr = closeErr
		}
	}()
	n, err := file.Write(contents)
	if err != nil {
		return "", err
	}
	if n != len(contents) {
		return "", io.ErrShortWrite
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	markerName = fetchMarkerName(name)
	if err := renameFetchFileNoReplace(file, root, markerName); err != nil {
		markerName = ""
		return "", fmt.Errorf("commit fetch provenance marker: %w", err)
	}
	renamed = true
	return markerName, nil
}

func openFetchQuotaLock(root *os.File) (*os.File, error) {
	const name = ".fetch-quota.lock"
	access := uint32(windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE | windows.READ_CONTROL | windows.WRITE_DAC)
	share := uint32(windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE) // Hold the name stable while a process owns the lock.
	handle, err := fetchNtCreateAt(windows.Handle(root.Fd()), name, access, share,
		windows.FILE_OPEN,
		windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT,
		windows.FILE_ATTRIBUTE_NORMAL, nil)
	if fetchNotFound(err) {
		security, secErr := newFetchPrivateSecurity()
		if secErr != nil {
			return nil, secErr
		}
		handle, err = fetchNtCreateAt(windows.Handle(root.Fd()), name, access, share,
			windows.FILE_CREATE,
			windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT,
			windows.FILE_ATTRIBUTE_NORMAL, security)
		runtime.KeepAlive(security)
		if fetchNameCollision(err) {
			handle, err = fetchNtCreateAt(windows.Handle(root.Fd()), name, access, share,
				windows.FILE_OPEN,
				windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT,
				windows.FILE_ATTRIBUTE_NORMAL, nil)
		}
	}
	if err != nil {
		return nil, err
	}
	if _, err := fetchVerifyRegular(handle); err == nil {
		err = verifyFetchPrivateACL(handle)
	}
	if err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	return os.NewFile(uintptr(handle), name), nil
}

func lockFetchQuota(ctx context.Context, handle windows.Handle) (*windows.Overlapped, error) {
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

func tryFetchLeaseLock(handle windows.Handle) (*windows.Overlapped, bool, error) {
	overlapped := &windows.Overlapped{}
	err := windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, overlapped)
	if err == nil {
		return overlapped, true, nil
	}
	if err == windows.ERROR_LOCK_VIOLATION || err == windows.ERROR_IO_PENDING {
		return nil, false, nil
	}
	return nil, false, err
}

func markFetchFileForDeletion(handle windows.Handle) error {
	deleteFile := uint8(1)
	var iosb windows.IO_STATUS_BLOCK
	return windows.NtSetInformationFile(handle, &iosb, (*byte)(unsafe.Pointer(&deleteFile)), uint32(unsafe.Sizeof(deleteFile)), windows.FileDispositionInformation)
}

func fetchDiskFreeBytes(root *os.File) (uint64, error) {
	buffer := make([]uint16, windows.MAX_LONG_PATH)
	length, err := windows.GetFinalPathNameByHandle(windows.Handle(root.Fd()), &buffer[0], uint32(len(buffer)), 0)
	if err != nil {
		return 0, err
	}
	if length == 0 || int(length) >= len(buffer) {
		return 0, errors.New("cannot resolve verified fetch temporary root")
	}
	var available uint64
	if err := windows.GetDiskFreeSpaceEx(&buffer[0], &available, nil, nil); err != nil {
		return 0, err
	}
	return available, nil
}

func fetchOwnerPrefix() (string, error) {
	sid, err := fetchCurrentSID()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(sid)
	return "fetch-" + hex.EncodeToString(digest[:8]) + "-", nil
}

func addFetchQuota(total *int64, size int64, limit int64) {
	if size <= 0 || limit <= *total {
		return
	}
	if size > limit-*total {
		*total = limit
		return
	}
	*total += size
}

func createFetchTemp(ctx context.Context, p FetchPolicy, task string, reserve int64) (*fetchTemp, error) {
	if reserve < 0 || p.TempGlobalBytes < 0 || p.TempTaskBytes < 0 {
		return nil, errors.New("invalid fetch temporary quota")
	}
	root, err := openFetchRoot(p.TempRoot)
	if err != nil {
		return nil, err
	}
	keepRoot := false
	defer func() {
		if !keepRoot {
			_ = root.Close()
		}
	}()
	lock, err := openFetchQuotaLock(root)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	lockRange, err := lockFetchQuota(ctx, windows.Handle(lock.Fd()))
	if err != nil {
		return nil, err
	}
	defer windows.UnlockFileEx(windows.Handle(lock.Fd()), 0, 1, 0, lockRange)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	prefix, err := fetchOwnerPrefix()
	if err != nil {
		return nil, err
	}
	taskHash := sha256.Sum256([]byte(task))
	taskPrefix := prefix + hex.EncodeToString(taskHash[:8]) + "-"
	names, err := root.Readdirnames(maxFetchTempEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(names) > maxFetchTempEntries {
		return nil, errors.New("fetch temporary directory exceeds entry limit")
	}
	var globalBytes, localBytes int64
	accountCandidate := func(name string, info windows.ByHandleFileInformation) error {
		size := uint64(info.FileSizeHigh)<<32 | uint64(info.FileSizeLow)
		if size > uint64(^uint64(0)>>1) {
			return errors.New("fetch temporary file size exceeds supported quota range")
		}
		addFetchQuota(&globalBytes, int64(size), p.TempGlobalBytes)
		if strings.HasPrefix(name, taskPrefix) {
			addFetchQuota(&localBytes, int64(size), p.TempTaskBytes)
		}
		return nil
	}
	for _, name := range names {
		if !strings.HasPrefix(name, prefix) || !ownedFetchName.MatchString(strings.TrimPrefix(name, prefix)) {
			continue
		}
		handle, info, openErr := openFetchFileAt(root, name,
			windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE|windows.DELETE|windows.READ_CONTROL,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE)
		if fetchNotFound(openErr) {
			continue
		}
		if openErr != nil {
			return nil, fmt.Errorf("inspect owned fetch temporary %q: %w", name, openErr)
		}
		file := os.NewFile(uintptr(handle), name)
		marker, marked, markerErr := openValidFetchMarker(root, name, info)
		if markerErr != nil || !marked {
			_ = file.Close()
			if err := accountCandidate(name, info); err != nil {
				return nil, err
			}
			continue
		}
		leaseLock, acquired, lockErr := tryFetchLeaseLock(handle)
		if lockErr != nil {
			_ = marker.Close()
			_ = file.Close()
			return nil, lockErr
		}
		if acquired {
			// Remove the provenance record first. If payload deletion then fails,
			// recovery will conservatively preserve and account the now-unmarked file.
			deleteErr := markFetchFileForDeletion(windows.Handle(marker.Fd()))
			if deleteErr == nil {
				deleteErr = markFetchFileForDeletion(handle)
			}
			_ = windows.UnlockFileEx(handle, 0, 1, 0, leaseLock)
			_ = marker.Close()
			_ = file.Close()
			if deleteErr != nil {
				return nil, fmt.Errorf("remove stale fetch temporary %q: %w", name, deleteErr)
			}
			continue
		}
		_ = marker.Close()
		_ = file.Close()
		if err := accountCandidate(name, info); err != nil {
			return nil, err
		}
	}
	if reserve == 0 {
		return nil, nil
	}
	if reserve > p.TempGlobalBytes-globalBytes || reserve > p.TempTaskBytes-localBytes {
		return nil, errors.New("fetch temporary disk quota exceeded")
	}
	available, err := fetchDiskFreeBytes(root)
	if err != nil {
		return nil, err
	}
	needed := uint64(reserve) + uint64(globalBytes)
	if needed > available {
		return nil, errors.New("insufficient fetch temporary disk space")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, err
	}
	name := taskPrefix + hex.EncodeToString(random[:])
	handle, err := createFetchFileAt(root, name)
	if err != nil {
		return nil, err
	}
	lease := &fetchTemp{file: os.NewFile(uintptr(handle), filepath.Join(root.Name(), name)), root: root, name: name}
	if err := windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{}); err != nil {
		lease.cleanup()
		return nil, err
	}
	if err := lease.file.Truncate(reserve); err != nil {
		lease.cleanup()
		return nil, err
	}
	if err := lease.file.Sync(); err != nil {
		lease.cleanup()
		return nil, err
	}
	markerName, markerErr := createFetchMarker(root, name, handle)
	lease.markerName = markerName
	if markerErr != nil {
		lease.cleanup()
		return nil, markerErr
	}
	if err := ctx.Err(); err != nil {
		lease.cleanup()
		return nil, err
	}
	keepRoot = true
	return lease, nil
}

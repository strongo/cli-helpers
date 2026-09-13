//go:build windows

package daemonlifecycle

import (
	"fmt"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// FILE_ALL_ACCESS is STANDARD_RIGHTS_REQUIRED | SYNCHRONIZE plus the nine
// file-specific rights. x/sys intentionally does not export this composite.
const userFileAccess windows.ACCESS_MASK = windows.STANDARD_RIGHTS_REQUIRED |
	windows.SYNCHRONIZE | 0x1ff

func currentUserSID() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return user.User.Sid, nil
}

func protectOwnerOnly(path string) error {
	sid, err := currentUserSID()
	if err != nil {
		return err
	}
	inheritance := uint32(windows.NO_INHERITANCE)
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		inheritance = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	}
	acl, err := ownerOnlyACL(sid, inheritance)
	if err != nil {
		return err
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil); err != nil {
		return err
	}
	// Applying the restrictive DACL first grants the current user WRITE_OWNER
	// even when a runner-created parent assigned an administrative group as the
	// inherited owner. A combined owner+DACL update opens the object before that
	// right exists and fails for an ordinary process token.
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION, sid, nil, nil, nil)
}

func protectOwnerOnlyFile(file *os.File) error {
	// os.OpenFile does not request WRITE_DAC, so SetSecurityInfo on its handle
	// fails with ERROR_ACCESS_DENIED on Windows. Apply the policy through the
	// named object instead. Callers pair this with ValidateOwnerOnlyFile, whose
	// handle-based check detects a path replacement before the file is trusted.
	return protectOwnerOnly(file.Name())
}

func ownerOnlyACL(sid *windows.SID, inheritance uint32) (*windows.ACL, error) {
	var pinner runtime.Pinner
	pinner.Pin(sid)
	defer pinner.Unpin()
	return windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: userFileAccess,
		AccessMode:        windows.SET_ACCESS,
		Inheritance:       inheritance,
		Trustee: windows.TRUSTEE{
			TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}}, nil)
}

func validateOwnerOnly(path string, info os.FileInfo) error {
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	return validateSecurityDescriptor(descriptor, info.IsDir())
}

func validateOwnerOnlyFile(file *os.File, info os.FileInfo) error {
	pathInfo, err := os.Stat(file.Name())
	if err != nil {
		return err
	}
	if !os.SameFile(info, pathInfo) {
		return fmt.Errorf("open handle no longer names the file at %s", file.Name())
	}
	descriptor, err := windows.GetSecurityInfo(windows.Handle(file.Fd()), windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	return validateSecurityDescriptor(descriptor, false)
}

func validateSecurityDescriptor(descriptor *windows.SECURITY_DESCRIPTOR, directory bool) error {
	want, err := currentUserSID()
	if err != nil {
		return err
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return err
	}
	if owner == nil || !owner.Equals(want) {
		return fmt.Errorf("owner is not the current user")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	if dacl == nil || dacl.AceCount != 1 {
		return fmt.Errorf("DACL has %d entries, want exactly one", aclCount(dacl))
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil {
		return err
	}
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Mask != userFileAccess {
		return fmt.Errorf("DACL entry does not grant the expected current-user access")
	}
	aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if !aceSID.Equals(want) {
		return fmt.Errorf("DACL entry belongs to a different user")
	}
	if directory {
		wantFlags := uint8(windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE)
		if ace.Header.AceFlags&wantFlags != wantFlags {
			return fmt.Errorf("directory DACL entry is not inheritable by child files and directories")
		}
	}
	return nil
}

func aclCount(acl *windows.ACL) uint16 {
	if acl == nil {
		return 0
	}
	return acl.AceCount
}

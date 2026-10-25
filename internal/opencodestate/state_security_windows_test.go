//go:build windows

package opencodestate

import (
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestSaveProtectsCredentialsWithWindowsACL(t *testing.T) {
	townRoot := t.TempDir()
	state := State{
		GasTownSession:  "gt-rig-worker",
		OpenCodeSession: "ses_test",
		Directory:       t.TempDir(),
		URL:             "http://127.0.0.1:1234",
		Username:        "opencode",
		Password:        "secret",
	}
	if err := Save(townRoot, state); err != nil {
		t.Fatal(err)
	}

	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		t.Fatal(err)
	}
	assertRestrictedDACL(t, Path(townRoot, state.GasTownSession), user.User.Sid, system)
	assertRestrictedDACL(t, filepath.Dir(Path(townRoot, state.GasTownSession)), user.User.Sid, system)
}

func TestAcquireSessionLockProtectsWindowsACL(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-rig-worker"
	release, err := AcquireSessionLock(townRoot, session)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		t.Fatal(err)
	}
	assertRestrictedDACL(t, Path(townRoot, session)+".lock", user.User.Sid, system)
}

func assertRestrictedDACL(t *testing.T, path string, user, system *windows.SID) {
	t.Helper()
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		t.Fatal(err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("%s inherits its DACL", path)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if dacl.AceCount < 2 {
		t.Fatalf("%s DACL entries = %d, want at least 2", path, dacl.AceCount)
	}
	var hasUser, hasSystem bool
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			t.Fatal(err)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		switch {
		case user.Equals(sid):
			hasUser = true
		case system.Equals(sid):
			hasSystem = true
		default:
			t.Fatalf("%s DACL grants unexpected SID %s", path, sid.String())
		}
	}
	if !hasUser || !hasSystem {
		t.Fatalf("%s DACL missing user or Local System access", path)
	}
}

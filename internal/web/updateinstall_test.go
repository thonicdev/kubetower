package web

import (
	"context"
	"errors"
	"testing"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/updater"
)

type fakeUpdater struct {
	status updater.Status
	err    error
}

func (f *fakeUpdater) Status() updater.Status { return f.status }
func (f *fakeUpdater) SetEnabled(on bool) (updater.Status, error) {
	f.status.Enabled = on
	return f.status, f.err
}
func (f *fakeUpdater) Download() (updater.Status, error) { return f.status, f.err }
func (f *fakeUpdater) Install(context.Context) (updater.Status, error) {
	return f.status, f.err
}
func (f *fakeUpdater) OpenFolder() (updater.Status, error) { return f.status, f.err }

func TestTheInstallerIsNilWhereNothingInstalls(t *testing.T) {
	if got := (&Server{}).updateInstaller(); got != nil {
		t.Fatalf("an installer over no updater: %T", got)
	}
}

func TestSettingsCarryTheInstallAndWhatTheRestartEnds(t *testing.T) {
	s := &Server{setup: SetupInfo{
		Updates:   func() UpdateReport { return UpdateReport{State: "newer", Latest: "0.1.3"} },
		Installer: &fakeUpdater{status: updater.Status{Phase: updater.Ready, Enabled: true, Supported: true, Installation: updater.LinuxAppImage, Version: "0.1.3"}},
		OpenWork:  func() (int, int) { return 2, 1 },
	}}
	got := settingsInfoOf(s).Update.Install
	if got == nil {
		t.Fatal("info.update carries no install")
	}
	want := api.UpdateInstall{Phase: "ready", Enabled: true, Supported: true, Installation: "linux-appimage",
		Version: "0.1.3", Ends: api.UpdateEnds{Terminals: 2, Forwards: 1}}
	if *got != want {
		t.Errorf("%+v, want %+v", *got, want)
	}
	// Where nothing installs, no install member at all.
	none := &Server{setup: SetupInfo{Updates: func() UpdateReport { return UpdateReport{State: "newer"} }}}
	if settingsInfoOf(none).Update.Install != nil {
		t.Error("an install member where nothing installs")
	}
}

func TestARefusedUpdateIsAConflictAndAnUnsavedSettingSaysSo(t *testing.T) {
	s := &Server{setup: SetupInfo{Installer: &fakeUpdater{err: updater.ErrDisabled}}}
	i := s.updateInstaller()
	if _, err := i.DownloadUpdate(); !errors.Is(err, api.ErrConflict) {
		t.Errorf("download: %v, want ErrConflict", err)
	}
	if _, err := i.InstallUpdate(context.Background()); !errors.Is(err, api.ErrConflict) {
		t.Errorf("install: %v, want ErrConflict", err)
	}
	if _, err := i.OpenUpdateFolder(); !errors.Is(err, api.ErrConflict) {
		t.Errorf("open folder: %v, want ErrConflict", err)
	}
	if out, err := i.SetInstallUpdates(false); !errors.Is(err, api.ErrNotSaved) || out.Enabled {
		t.Errorf("setting: %+v %v, want not saved and off", out, err)
	}
}

func TestTheManualPhaseCarriesItsFolder(t *testing.T) {
	s := &Server{setup: SetupInfo{Installer: &fakeUpdater{status: updater.Status{
		Phase: updater.Manual, Enabled: true, Supported: true, Installation: updater.LinuxTar,
		Version: "0.1.3", Folder: "/home/alice/.local/state/kubetower/update/install-1"}}}}
	got, err := s.updateInstaller().OpenUpdateFolder()
	if err != nil || got.Phase != "manual" || got.Folder != "/home/alice/.local/state/kubetower/update/install-1" {
		t.Fatalf("%+v %v", got, err)
	}
}

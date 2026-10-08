package manager

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/config"
)

// CloneInput copies a bench to a new bench.
type CloneInput struct {
	Source string
	Target string
	// NoFiles leaves the site's attachments behind.
	NoFiles bool
	// VendorApps archives these apps' working trees, uncommitted changes
	// included ("all" for every app). Without it each app is cloned again at
	// the commit the source is on.
	VendorApps []string
	// Domain is the new bench's domain, required when cloning a prod bench.
	Domain string
	NoSSL  bool
	LAN    bool
	// KeepArchive keeps the intermediate backup archive.
	KeepArchive bool
}

// Clone backs the source up and restores the archive as a new bench.
//
// It is `ffm backup` plus `ffm restore` under another name, with each app
// pinned to the source's commit. That path already works across hosts, uids
// and architectures; a direct volume copy would be faster but only on one
// host and only with the source stopped.
func (s *Service) Clone(in CloneInput, pw ProgressWriter) error {
	if pw == nil {
		pw = CLIProgress{}
	}
	if in.Source == in.Target {
		return fmt.Errorf("the clone needs a different name than %q", in.Source)
	}
	if _, err := s.GetBench(in.Target); err == nil {
		return fmt.Errorf("bench %q already exists", in.Target)
	}
	src, err := s.GetBench(in.Source)
	if err != nil {
		return err
	}
	if src.IsProd() && in.Domain == "" {
		return fmt.Errorf("cloning a production bench needs --domain for the new bench")
	}

	backups, err := config.EnsureBenchBackupsDir(src.Name)
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(backups, ".clone-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	var archive string
	pw.Printf("Cloning %q to %q: backing up the source...\n", src.Name, in.Target)
	if err := s.Backup(BackupInput{
		BenchName: src.Name, Out: tmp, NoFiles: in.NoFiles, VendorApps: in.VendorApps,
		Label: "clone to " + in.Target, writtenTo: &archive,
	}, pw); err != nil {
		return fmt.Errorf("back up %q: %w", src.Name, err)
	}
	if in.KeepArchive {
		kept := filepath.Join(backups, src.Name+"_clone-to-"+in.Target+".ffm.tar")
		if err := os.Rename(archive, kept); err == nil {
			archive = kept
			pw.Printf("Keeping the archive at %s\n", kept)
		}
	}

	pw.Printf("Restoring it as %q...\n", in.Target)
	return s.Restore(RestoreInput{
		Archive: archive, TargetName: in.Target, WithFiles: !in.NoFiles,
		PinApps: true, ReallocatePorts: true, Domain: in.Domain, NoSSL: in.NoSSL, LAN: in.LAN,
	}, pw)
}

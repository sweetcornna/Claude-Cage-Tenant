package workbench

import (
	"context"
	"os"
	"path/filepath"

	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/host"
	"github.com/sylvinus/agent-vm/internal/hostimage"
)

// reuseLocalVMInstallMedia stages only the selected, pinned archive. It runs
// in the installer and never delays opening or switching workbench views.
// Prepare performs the regular digest check on the staged file before use.
func reuseLocalVMInstallMedia(ctx context.Context, profile, cache string, plan hostimage.Plan) {
	if plan.Path != "" || plan.Digest == "" || plan.Validate() != nil || ctx.Err() != nil {
		return
	}
	family := cage.LocalVMFamily(profile)
	if family == profile {
		return
	}
	destination := plan.CachedPath(cache)
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		return
	}
	if _, err := os.Lstat(destination + ".partial"); !os.IsNotExist(err) {
		return
	}
	sourceDir := filepath.Join(family, "state", "guest-media", plan.Host.System)
	sourcePlan, err := hostimage.Load(sourceDir)
	if err != nil || sourcePlan.Host != plan.Host || sourcePlan.Version != plan.Version || sourcePlan.Build != plan.Build || sourcePlan.Format != plan.Format || sourcePlan.Digest != plan.Digest {
		return
	}
	source := plan.CachedPath(sourceDir)
	info, err := os.Lstat(source)
	if err != nil || !info.Mode().IsRegular() || info.Size() != sourcePlan.Bytes || info.ModTime().UnixNano() != sourcePlan.Modified || plan.Bytes > 0 && info.Size() != plan.Bytes {
		return
	}
	if host.EnsurePrivateDir(cache) == nil {
		// Unsupported filesystems fall back to the regular download path.
		// The destination always has a separate inode.
		_ = cloneLocalVMFile(source, destination)
	}
}

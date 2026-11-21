package workbench

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/guestos"
	"github.com/sylvinus/agent-vm/internal/mounts"
	"github.com/sylvinus/agent-vm/internal/paths"
)

func (s *Server) validateStoragePlacement(directory string) error {
	target, err := cage.StoragePhysicalPath(directory)
	if err != nil {
		return err
	}
	projects, err := s.projectPaths()
	if err != nil {
		return err
	}
	for _, project := range projects {
		physical, err := cage.StoragePhysicalPath(project)
		if err != nil {
			return err
		}
		if cage.StoragePathsOverlap(physical, target) {
			return errors.New("环境存储目录与已共享的项目重叠，请选择项目以外的专用文件夹")
		}
	}
	return nil
}

// Resolve both sides of the sharing boundary. A profile or retained backup
// can be reached through a Windows junction even if its saved path looks
// unrelated to the requested project.
func privateProjectDirectories(profile string) ([]string, error) {
	directories, err := cage.StoragePrivatePaths(profile)
	if err != nil {
		return nil, err
	}
	directories = append(directories, profile)
	directories = append(directories, guestos.WindowsPrivateStoragePaths()...)
	for _, directory := range directories {
		physical, err := cage.StoragePhysicalPath(directory)
		if os.IsNotExist(err) {
			// A detached backup drive still reserves its recorded location.
			continue
		}
		if err != nil {
			return nil, err
		}
		if !paths.Equal(physical, directory) {
			directories = append(directories, physical)
		}
	}
	return directories, nil
}

// localVMSiblingProfiles lists the other profiles of the desktop VM family
// that contains profile, and of the default ~/.claude-cage family.
func localVMSiblingProfiles(profile string) ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	candidates, err := cage.LocalVMProfiles(profile)
	if err != nil {
		return nil, errors.New("无法读取本机虚拟机列表，请检查配置目录的访问权限")
	}
	defaults, err := cage.LocalVMProfiles(filepath.Join(home, ".claude-cage"))
	if err != nil {
		return nil, errors.New("无法读取本机虚拟机列表，请检查配置目录的访问权限")
	}
	candidates = append(candidates, defaults...)
	var siblings []string
	for _, candidate := range candidates {
		candidate = filepath.Clean(candidate)
		known := paths.Equal(candidate, filepath.Clean(profile))
		for _, sibling := range siblings {
			known = known || paths.Equal(candidate, sibling)
		}
		if !known {
			siblings = append(siblings, candidate)
		}
	}
	return siblings, nil
}

func validateHostProjectData(root, profile string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	defaultProfile := filepath.Join(home, ".claude-cage")
	// Every other profile of the desktop VM family, the default workload
	// included, keeps its configuration, disks and backups out of this
	// profile's shares.
	siblings, err := localVMSiblingProfiles(profile)
	if err != nil {
		return err
	}
	for _, other := range siblings {
		private, err := privateProjectDirectories(other)
		if err != nil {
			return fmt.Errorf("无法确认虚拟机「%s」的环境存储位置，暂不能共享项目：%w", localVMLabel(other), err)
		}
		for _, dir := range private {
			if cage.StoragePathsOverlap(root, dir) {
				return errors.New("Super Cage 配置、环境存储及备份不能共享")
			}
		}
	}
	refs := mounts.Refs(paths.Host(home), "", paths.Host(defaultProfile))
	if _, unsafe := mounts.UnsafeProject(paths.Host(root), refs); unsafe {
		return errors.New("此目录包含或属于凭据、用户资料或版本控制数据，无法共享")
	}
	return nil
}

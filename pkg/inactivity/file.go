package inactivity

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/loft-sh/devpod/pkg/agent"
	provider2 "github.com/loft-sh/devpod/pkg/provider"
)

// LoadWorkspaces builds in-memory workspace snapshots from the workspace
// configs below the agent home folder
// (contexts/<context>/workspaces/<id>/workspace.json). The busy flag is
// read via agent.HasWorkspaceBusyFile. Files that cannot be read, parsed
// or stated are skipped; the encountered errors are joined and returned
// together with the snapshots that could be built.
func LoadWorkspaces(baseFolder string) ([]Workspace, error) {
	matches, err := WorkspaceConfigFiles(baseFolder)
	if err != nil {
		return nil, err
	}

	var errs []error
	workspaces := make([]Workspace, 0, len(matches))
	for _, match := range matches {
		workspace, err := loadWorkspace(match)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		workspaces = append(workspaces, workspace)
	}
	return workspaces, errors.Join(errs...)
}

// WorkspaceConfigFiles returns the workspace config files below the agent
// home folder.
func WorkspaceConfigFiles(baseFolder string) ([]string, error) {
	return filepath.Glob(baseFolder + "/contexts/*/workspaces/*/" + provider2.WorkspaceConfigFile)
}

func loadWorkspace(workspaceConfig string) (Workspace, error) {
	info, err := agent.ParseAgentWorkspaceInfo(workspaceConfig)
	if err != nil {
		return Workspace{}, err
	}

	stat, err := os.Stat(workspaceConfig)
	if err != nil {
		return Workspace{}, err
	}

	id := filepath.Base(filepath.Dir(workspaceConfig))
	if info.Workspace != nil && info.Workspace.ID != "" {
		id = info.Workspace.ID
	}

	return Workspace{
		ID:                 id,
		ModTime:            stat.ModTime(),
		Timeout:            info.Agent.Timeout,
		HasShutdownCommand: len(info.Agent.Exec.Shutdown) > 0,
		Busy:               agent.HasWorkspaceBusyFile(filepath.Dir(workspaceConfig)),
	}, nil
}

// TouchWorkspaceConfigs sets the modification time of every workspace
// config below baseFolder to now. It is used at daemon startup so a
// machine is not judged idle immediately after the daemon (re)starts.
// Individual failures do not stop the remaining files from being touched;
// the errors are joined and returned.
func TouchWorkspaceConfigs(baseFolder string, now time.Time) error {
	matches, err := WorkspaceConfigFiles(baseFolder)
	if err != nil {
		return err
	}
	return TouchFiles(matches, now)
}

// TouchFiles sets the modification time of every given file to now,
// continuing past individual failures and joining the errors.
func TouchFiles(files []string, now time.Time) error {
	var errs []error
	for _, file := range files {
		if err := os.Chtimes(file, now, now); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ContainerActivity returns the effective last-activity timestamp of the
// container layer. A missing (or unreadable) activity file means "no
// activity recorded yet" and falls back to the daemon start time, so the
// idle timeout is measured from daemon start instead of never expiring.
func ContainerActivity(activityFile string, daemonStart time.Time) time.Time {
	stat, err := os.Stat(activityFile)
	if err != nil {
		return daemonStart
	}
	return stat.ModTime()
}

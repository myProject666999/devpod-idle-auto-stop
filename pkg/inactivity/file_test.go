package inactivity

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"gotest.tools/assert"
)

// writeWorkspaceConfig writes a workspace.json below
// <dir>/contexts/<ctx>/workspaces/<id>/ and returns the config path.
func writeWorkspaceConfig(t *testing.T, dir, ctx, id, content string) string {
	t.Helper()
	folder := filepath.Join(dir, "contexts", ctx, "workspaces", id)
	assert.NilError(t, os.MkdirAll(folder, 0o755))
	config := filepath.Join(folder, "workspace.json")
	assert.NilError(t, os.WriteFile(config, []byte(content), 0o600))
	return config
}

func TestLoadWorkspaces(t *testing.T) {
	dir := t.TempDir()

	withShutdown := writeWorkspaceConfig(t, dir, "default", "ws1",
		`{"workspace":{"id":"ws1"},"agent":{"inactivityTimeout":"10m","exec":{"shutdown":["shutdown","now"]}}}`)
	withoutShutdown := writeWorkspaceConfig(t, dir, "default", "ws2",
		`{"workspace":{"id":"ws2"},"agent":{"inactivityTimeout":"5m"}}`)

	modTime := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	assert.NilError(t, os.Chtimes(withShutdown, modTime, modTime))

	// ws2 is busy
	assert.NilError(t, os.WriteFile(filepath.Join(filepath.Dir(withoutShutdown), "workspace.lock"), nil, 0o600))

	workspaces, err := LoadWorkspaces(dir)
	assert.NilError(t, err)
	assert.Equal(t, len(workspaces), 2)

	byID := map[string]Workspace{}
	for _, w := range workspaces {
		byID[w.ID] = w
	}

	ws1 := byID["ws1"]
	assert.Equal(t, ws1.Timeout, "10m")
	assert.Equal(t, ws1.HasShutdownCommand, true)
	assert.Equal(t, ws1.Busy, false)
	assert.Assert(t, ws1.ModTime.Equal(modTime), "modtime: %s", ws1.ModTime)

	ws2 := byID["ws2"]
	assert.Equal(t, ws2.HasShutdownCommand, false)
	assert.Equal(t, ws2.Busy, true)
}

func TestLoadWorkspacesSkipsBrokenConfig(t *testing.T) {
	dir := t.TempDir()
	writeWorkspaceConfig(t, dir, "default", "good",
		`{"workspace":{"id":"good"},"agent":{"exec":{"shutdown":["true"]}}}`)
	writeWorkspaceConfig(t, dir, "default", "broken", `{not json`)

	workspaces, err := LoadWorkspaces(dir)
	assert.Assert(t, err != nil)
	assert.Equal(t, len(workspaces), 1)
	assert.Equal(t, workspaces[0].ID, "good")
}

// TestTouchWorkspaceConfigs pins the initialTouch behaviour: at daemon
// startup all workspace config modtimes are set to now, so a machine is
// not judged idle immediately after the daemon starts.
func TestTouchWorkspaceConfigs(t *testing.T) {
	dir := t.TempDir()
	config := writeWorkspaceConfig(t, dir, "default", "ws1",
		`{"workspace":{"id":"ws1"},"agent":{"exec":{"shutdown":["true"]}}}`)

	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	assert.NilError(t, os.Chtimes(config, old, old))

	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	assert.NilError(t, TouchWorkspaceConfigs(dir, now))

	stat, err := os.Stat(config)
	assert.NilError(t, err)
	assert.Assert(t, stat.ModTime().Equal(now), "modtime: %s", stat.ModTime)

	// and the workspace is therefore not idle right after the touch
	workspaces, err := LoadWorkspaces(dir)
	assert.NilError(t, err)
	d := Evaluate(now, workspaces, 0, Options{})
	assert.Equal(t, d.Idle, false)
	assert.Equal(t, d.StopIn, 20*time.Minute) // default timeout
}

// TestTouchFilesContinuesOnError pins that a failing Chtimes does not stop
// the remaining files from being touched.
func TestTouchFilesContinuesOnError(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first")
	second := filepath.Join(dir, "second")
	assert.NilError(t, os.WriteFile(first, nil, 0o600))
	assert.NilError(t, os.WriteFile(second, nil, 0o600))

	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	err := TouchFiles([]string{first, filepath.Join(dir, "missing"), second}, now)
	assert.Assert(t, err != nil)

	for _, f := range []string{first, second} {
		stat, err := os.Stat(f)
		assert.NilError(t, err)
		assert.Assert(t, stat.ModTime().Equal(now), "%s modtime: %s", f, stat.ModTime)
	}
}

// TestContainerActivityMissingFile pins that a missing activity file falls
// back to the daemon start time instead of never timing out.
func TestContainerActivityMissingFile(t *testing.T) {
	daemonStart := time.Date(2026, 11, 24, 11, 0, 0, 0, time.UTC)
	activity := ContainerActivity(filepath.Join(t.TempDir(), "devpod.activity"), daemonStart)
	assert.Assert(t, activity.Equal(daemonStart))
}

func TestContainerActivityExistingFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "devpod.activity")
	assert.NilError(t, os.WriteFile(file, nil, 0o600))
	modTime := time.Date(2026, 11, 24, 11, 30, 0, 0, time.UTC)
	assert.NilError(t, os.Chtimes(file, modTime, modTime))

	daemonStart := time.Date(2026, 11, 24, 11, 0, 0, 0, time.UTC)
	activity := ContainerActivity(file, daemonStart)
	assert.Assert(t, activity.Equal(modTime))
}

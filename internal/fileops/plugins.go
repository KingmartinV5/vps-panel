package fileops

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type PluginFile struct {
	Name  string
	Size  int64
	MTime time.Time
}

// InstallPlugin copies the jar at srcJarPath (already resolved/validated by
// the caller, e.g. from the plugin catalog) into <serverDataRoot>/plugins/
// under filename. Goes through Join like everything else here. Copies to a
// ".tmp" sibling first and renames into place, so a copy that fails partway
// (disk full, process killed) never leaves a half-written jar for the game
// server to try to load on its next start.
func InstallPlugin(serverDataRoot, srcJarPath, filename string) error {
	pluginsDir, err := Join(serverDataRoot, "plugins")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		return err
	}
	dest, err := Join(serverDataRoot, filepath.Join("plugins", filename))
	if err != nil {
		return err
	}

	src, err := os.Open(srcJarPath)
	if err != nil {
		return err
	}
	defer src.Close()

	tmp := dest + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, src); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// ListInstalledPlugins lists *.jar files directly under
// <serverDataRoot>/plugins/ -- both catalog installs and anything a customer
// uploaded manually via the file manager. Empty (not an error) if the
// directory doesn't exist yet.
func ListInstalledPlugins(serverDataRoot string) ([]PluginFile, error) {
	pluginsDir, err := Join(serverDataRoot, "plugins")
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(pluginsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]PluginFile, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".jar") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, PluginFile{Name: e.Name(), Size: info.Size(), MTime: info.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

// RemoveInstalledPlugin deletes <serverDataRoot>/plugins/<filename> only.
// filename arrives from a form POST (customer-supplied, untrusted), so on
// top of Join's escape-the-data-dir check, this also refuses anything that
// doesn't resolve to a direct child of the plugins/ directory specifically
// -- Join alone would still allow e.g. filename="../other/x.jar" as long as
// it stayed inside the server's data dir, which is not what this function is
// for.
func RemoveInstalledPlugin(serverDataRoot, filename string) error {
	pluginsDir, err := Join(serverDataRoot, "plugins")
	if err != nil {
		return err
	}
	target, err := Join(serverDataRoot, filepath.Join("plugins", filename))
	if err != nil {
		return err
	}
	if filepath.Dir(target) != pluginsDir {
		return fmt.Errorf("%w: refusing to remove a path outside the plugins directory", ErrPathSecurity)
	}
	if err := os.Remove(target); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return nil
}

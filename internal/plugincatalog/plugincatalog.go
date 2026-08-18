// Package plugincatalog reads an admin-curated list of installable plugin
// jars (name/description/category + a filename) from a catalog.json file.
// This panel never fetches plugin binaries from the internet on a customer's
// behalf -- the catalog and the jars it references are supplied by whoever
// runs the panel, entirely offline as far as this code is concerned. That's
// deliberate: real plugin jars are third-party copyrighted software with
// their own licensing terms, and this panel has no legitimate way to source
// or verify them itself.
package plugincatalog

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
)

type Entry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Category    string `json:"category"`
	Filename    string `json:"filename"`
}

// Load reads <root>/catalog.json. A missing file is not an error -- it just
// means the admin hasn't set up a catalog yet, so callers get an empty list
// and the UI shows an empty state rather than a broken page. Entries with an
// unsafe or empty filename/id are skipped (logged, not fatal) rather than
// failing the whole catalog over one bad entry.
func Load(root string) ([]Entry, error) {
	path := filepath.Join(root, "catalog.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []Entry{}, nil
		}
		return nil, err
	}

	var raw []Entry
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	out := make([]Entry, 0, len(raw))
	for _, e := range raw {
		if e.ID == "" || e.Filename == "" {
			log.Printf("plugincatalog: skipping entry with empty id/filename: %+v", e)
			continue
		}
		if strings.ContainsAny(e.Filename, "/\\") || strings.Contains(e.Filename, "..") {
			log.Printf("plugincatalog: skipping entry %q with unsafe filename %q", e.ID, e.Filename)
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

// JarPath returns the on-disk path of the jar an entry refers to, alongside
// catalog.json in the same root directory.
func JarPath(root string, e Entry) string {
	return filepath.Join(root, e.Filename)
}

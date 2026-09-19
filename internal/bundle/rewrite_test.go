package bundle

import (
	"os"

	"github.com/sebastianhutter/agentctx/internal/store"
	"github.com/sebastianhutter/agentctx/internal/vault"
)

// removeAndRecreate stands in for the consolidation pass, which is the only
// thing allowed to rewrite a note body. Tests only.
func removeAndRecreate(path string, note *store.TaskNote) error {
	data, err := vault.Render(note, "")
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return vault.Create(path, data)
}

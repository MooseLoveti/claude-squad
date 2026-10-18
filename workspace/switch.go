package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// The panel asks the attached claude-squad to reopen the dashboard elsewhere
// by leaving a switch request, then stopping the dashboard; the attached
// process finds the request when its tmux client exits.
const switchFileName = "switch.json"

type switchRequest struct {
	Dir string `json:"dir"`
}

// RequestSwitch asks for the dashboard to be reopened on dir.
func RequestSwitch(configDir, dir string) error {
	data, err := json.Marshal(switchRequest{Dir: dir})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(configDir, switchFileName), data, 0o644)
}

// TakeSwitch returns and clears a pending switch request.
func TakeSwitch(configDir string) (string, bool) {
	path := filepath.Join(configDir, switchFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	_ = os.Remove(path)
	var req switchRequest
	if json.Unmarshal(data, &req) != nil || req.Dir == "" {
		return "", false
	}
	return req.Dir, true
}

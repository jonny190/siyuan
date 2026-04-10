package admin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// seedConfJSON writes a minimal <workspace>/conf/conf.json so the kernel boots with the
// portal-generated api.token and accessAuthCode already in place.
//
// The SiYuan kernel will merge this minimal JSON into its full AppConf at startup — any
// fields we don't provide get their Go zero values, then InitConf applies per-field
// defaults. Only api.token and accessAuthCode actually need to be pre-seeded; everything
// else can be omitted.
//
// If the file already exists (e.g. recreating a user's container for a workspace that
// survived from a previous lifecycle) we leave it untouched so we don't clobber user
// preferences. The caller is responsible for only calling this on first provision.
func seedConfJSON(workspacePath, apiToken, accessAuthCode string) error {
	confDir := filepath.Join(workspacePath, "conf")
	confPath := filepath.Join(confDir, "conf.json")

	// Don't overwrite an existing file.
	if _, err := os.Stat(confPath); err == nil {
		return nil
	}

	if err := os.MkdirAll(confDir, 0o755); err != nil {
		return fmt.Errorf("mkdir conf: %w", err)
	}

	// Minimal structure matching kernel/model/conf.go AppConf field names.
	seed := map[string]any{
		"api": map[string]any{
			"token": apiToken,
		},
		"accessAuthCode": accessAuthCode,
		"sync": map[string]any{
			"provider": 4, // conf.ProviderLocal - matches the self-host default
			"enabled":  false,
		},
	}
	data, err := json.MarshalIndent(seed, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal seed: %w", err)
	}
	if err := os.WriteFile(confPath, data, 0o644); err != nil {
		return fmt.Errorf("write conf.json: %w", err)
	}
	return nil
}

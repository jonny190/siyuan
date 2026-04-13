package orchestrator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// seedConfJSON ensures <workspace>/conf/conf.json exists and that its api.token and
// accessAuthCode fields match what the portal has in its user store.
//
// Why this has to be more than a one-shot write:
//
//  1. First-boot case — file doesn't exist. Write a minimal seed. The kernel's InitConf
//     applies defaults to every other field.
//
//  2. Token drift — file exists but the api.token inside doesn't match the portal's
//     record. This happens if (a) an older portal version created the workspace without
//     seeding, (b) an operator wiped conf.json and let the kernel regenerate it,
//     (c) the portal DB was restored from a different backup than the workspace. In all
//     three, the portal's injected Authorization: Token header will be rejected by the
//     kernel until we reconcile. We merge-in the correct token while leaving every
//     other field untouched, so user preferences (editor settings, themes, etc.) survive.
//
//  3. Already correct — file exists and api.token / accessAuthCode already match.
//     Cheap no-op: just parse, compare, and return without writing.
//
// The kernel reads conf.json only at startup, so this needs to run before any
// createContainer or StartContainer call.
func seedConfJSON(workspacePath, apiToken, accessAuthCode string) error {
	confDir := filepath.Join(workspacePath, "conf")
	confPath := filepath.Join(confDir, "conf.json")

	if err := os.MkdirAll(confDir, 0o755); err != nil {
		return fmt.Errorf("mkdir conf: %w", err)
	}

	// Load existing file if present; otherwise start from an empty map.
	conf := map[string]any{}
	existing, err := os.ReadFile(confPath)
	switch {
	case err == nil:
		if len(existing) > 0 {
			if err := json.Unmarshal(existing, &conf); err != nil {
				// Corrupted JSON — refuse to touch it rather than stomping data the
				// user might recover manually.
				return fmt.Errorf("parse existing conf.json: %w", err)
			}
		}
	case !os.IsNotExist(err):
		return fmt.Errorf("read conf.json: %w", err)
	}

	// Reconcile api.token.
	api, _ := conf["api"].(map[string]any)
	if api == nil {
		api = map[string]any{}
	}
	existingToken, _ := api["token"].(string)

	// Reconcile accessAuthCode.
	existingAuth, _ := conf["accessAuthCode"].(string)

	// If both already correct, don't touch the file — keeps mtime stable and avoids
	// unnecessary writes on every kernel spawn.
	if existingToken == apiToken && existingAuth == accessAuthCode && len(existing) > 0 {
		return nil
	}

	api["token"] = apiToken
	conf["api"] = api
	conf["accessAuthCode"] = accessAuthCode

	// First-boot case only: also seed a sensible sync default so the user doesn't
	// accidentally leak data to the legacy provider. If the file already existed we
	// leave sync alone because the user may have configured WebDAV/S3.
	if len(existing) == 0 {
		conf["sync"] = map[string]any{
			"provider": 4, // conf.ProviderLocal
			"enabled":  false,
		}
	}

	data, err := json.MarshalIndent(conf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal conf: %w", err)
	}
	if err := os.WriteFile(confPath, data, 0o644); err != nil {
		return fmt.Errorf("write conf.json: %w", err)
	}
	return nil
}

// SiYuan - Refactor your thinking
// Copyright (c) 2020-present, b3log.org
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

// Package model: local package (plugin / widget / theme / icon / template) metadata helpers.
// This file replaces the legacy kernel/bazaar package for the self-host fork. It intentionally
// contains only the local-disk surface area that plugin.go, widget.go, and appearance.go need.
// There is no marketplace, no remote stage, no install/uninstall, no persisted package info.

package model

import (
	"html"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/88250/gulu"
	"github.com/siyuan-note/filelock"
	"github.com/siyuan-note/logging"
	"github.com/siyuan-note/siyuan/kernel/util"
	"golang.org/x/mod/semver"
)

// PkgLocaleStrings maps locale keys (e.g. "default", "en_US", "zh_CN") to display strings.
type PkgLocaleStrings map[string]string

type PkgFunding struct {
	OpenCollective string   `json:"openCollective"`
	Patreon        string   `json:"patreon"`
	GitHub         string   `json:"github"`
	Custom         []string `json:"custom"`
}

// Package describes a locally-installed plugin/widget/theme/icon/template.
// Only the fields actually read from on-disk *.json manifests are populated; marketplace-only
// fields (stars, downloads, previews, etc.) are retained for JSON compatibility with the
// frontend but never filled.
type Package struct {
	Author            string           `json:"author"`
	URL               string           `json:"url"`
	Version           string           `json:"version"`
	MinAppVersion     string           `json:"minAppVersion"`
	DisabledInPublish bool             `json:"disabledInPublish"`
	Backends          []string         `json:"backends"`
	Frontends         []string         `json:"frontends"`
	DisplayName       PkgLocaleStrings `json:"displayName"`
	Description       PkgLocaleStrings `json:"description"`
	Readme            PkgLocaleStrings `json:"readme"`
	Funding           *PkgFunding      `json:"funding"`
	Keywords          []string         `json:"keywords"`

	Name     string `json:"name"`    // package name (directory name on disk)
	RepoURL  string `json:"repoURL"` // optional; populated from plugin.json if present
	RepoHash string `json:"repoHash"`

	Installed    bool      `json:"installed"`
	Incompatible *bool     `json:"incompatible,omitempty"` // plugin-only
	Enabled      *bool     `json:"enabled,omitempty"`      // plugin-only
	Modes        *[]string `json:"modes,omitempty"`        // theme-only
}

// ParsePackageJSON reads and parses a plugin.json / widget.json / theme.json / icon.json /
// template.json file from disk and returns a Package. Display strings are HTML-escaped
// defensively so that untrusted manifests cannot inject script into settings UI.
func ParsePackageJSON(filePath string) (ret *Package, err error) {
	if !filelock.IsExist(filePath) {
		err = os.ErrNotExist
		return
	}
	data, err := filelock.ReadFile(filePath)
	if err != nil {
		logging.LogErrorf("read [%s] failed: %s", filePath, err)
		return
	}
	if err = gulu.JSON.UnmarshalJSON(data, &ret); err != nil {
		logging.LogErrorf("parse [%s] failed: %s", filePath, err)
		return
	}
	sanitizePackageDisplayStrings(ret)
	ret.URL = strings.TrimSuffix(ret.URL, "/")
	return
}

func sanitizePackageDisplayStrings(pkg *Package) {
	if pkg == nil {
		return
	}
	for k, v := range pkg.DisplayName {
		pkg.DisplayName[k] = html.EscapeString(v)
	}
	for k, v := range pkg.Description {
		pkg.Description[k] = html.EscapeString(v)
	}
}

// GetPreferredLocaleString picks a locale-appropriate string from a LocaleStrings map,
// falling back through util.Lang -> "default" -> "en_US" -> the provided fallback.
func GetPreferredLocaleString(m PkgLocaleStrings, fallback string) string {
	if len(m) == 0 {
		return fallback
	}
	if v := strings.TrimSpace(m[util.Lang]); v != "" {
		return v
	}
	if v := strings.TrimSpace(m["default"]); v != "" {
		return v
	}
	if v := strings.TrimSpace(m["en_US"]); v != "" {
		return v
	}
	return fallback
}

// ParseInstalledPlugin scans `data/plugins/<name>/plugin.json` and reports whether the
// plugin exists, its preferred display name, and compatibility flags for the given frontend.
func ParseInstalledPlugin(name, frontend string) (found bool, displayName string, incompatible, disabledInPublish, disallowInstall bool) {
	pluginsPath := filepath.Join(util.DataDir, "plugins")
	if !util.IsPathRegularDirOrSymlinkDir(pluginsPath) {
		return
	}

	pluginDirs, err := os.ReadDir(pluginsPath)
	if err != nil {
		logging.LogWarnf("read plugins folder failed: %s", err)
		return
	}

	for _, pluginDir := range pluginDirs {
		if !util.IsDirRegularOrSymlink(pluginDir) {
			continue
		}
		if name != pluginDir.Name() {
			continue
		}

		plugin, parseErr := ParsePackageJSON(filepath.Join(pluginsPath, name, "plugin.json"))
		if parseErr != nil || plugin == nil {
			return
		}

		found = true
		displayName = GetPreferredLocaleString(plugin.DisplayName, plugin.Name)
		incompatible = IsIncompatiblePlugin(plugin, frontend)
		disabledInPublish = plugin.DisabledInPublish
		disallowInstall = isBelowRequiredAppVersion(plugin)
		return
	}
	return
}

// IsIncompatiblePlugin checks whether a plugin declares support for the current backend
// (OS / container) and the requested frontend.
func IsIncompatiblePlugin(plugin *Package, frontend string) bool {
	if !isTargetSupported(plugin.Backends, getCurrentBackend()) {
		return true
	}
	if !isTargetSupported(plugin.Frontends, frontend) {
		return true
	}
	return false
}

var cachedBackend string

func getCurrentBackend() string {
	if cachedBackend == "" {
		if util.Container == util.ContainerStd {
			cachedBackend = runtime.GOOS
		} else {
			cachedBackend = util.Container
		}
	}
	return cachedBackend
}

func isTargetSupported(platforms []string, target string) bool {
	if len(platforms) == 0 {
		return true
	}
	for _, v := range platforms {
		if v == target || v == "all" {
			return true
		}
	}
	return false
}

func isBelowRequiredAppVersion(pkg *Package) bool {
	if pkg.MinAppVersion == "" {
		return false
	}
	return semver.Compare("v"+pkg.MinAppVersion, "v"+util.Ver) > 0
}

// FilterPackages keeps only packages that match every whitespace-separated keyword
// against name, author, display names, descriptions, keywords, or repo basename.
func FilterPackages(packages []*Package, keyword string) []*Package {
	keywords := getSearchKeywords(keyword)
	if len(keywords) == 0 {
		return packages
	}
	ret := []*Package{}
	for _, pkg := range packages {
		if packageContainsKeywords(pkg, keywords) {
			ret = append(ret, pkg)
		}
	}
	return ret
}

func getSearchKeywords(query string) (ret []string) {
	query = strings.TrimSpace(query)
	if query == "" {
		return
	}
	for _, k := range strings.Split(query, " ") {
		if k != "" {
			ret = append(ret, strings.ToLower(k))
		}
	}
	return
}

func packageContainsKeywords(pkg *Package, keywords []string) bool {
	if len(keywords) == 0 {
		return true
	}
	if pkg == nil {
		return false
	}
	for _, kw := range keywords {
		if !packageContainsKeyword(pkg, kw) {
			return false
		}
	}
	return true
}

func packageContainsKeyword(pkg *Package, kw string) bool {
	if strings.Contains(strings.ToLower(pkg.Name), kw) ||
		strings.Contains(strings.ToLower(pkg.Author), kw) {
		return true
	}
	for _, s := range pkg.DisplayName {
		if strings.Contains(strings.ToLower(s), kw) {
			return true
		}
	}
	for _, s := range pkg.Description {
		if strings.Contains(strings.ToLower(s), kw) {
			return true
		}
	}
	for _, s := range pkg.Keywords {
		if strings.Contains(strings.ToLower(s), kw) {
			return true
		}
	}
	if pkg.RepoURL != "" && strings.Contains(strings.ToLower(path.Base(pkg.RepoURL)), kw) {
		return true
	}
	return false
}

// RemovePackageInfo is a no-op in the self-host fork. The upstream marketplace tracked
// per-package install timestamps in `data/storage/bazaar.json`; sideload-only installations
// do not need this bookkeeping.
func RemovePackageInfo(pkgType, pkgName string) {}

// isBuiltInTheme reports whether a theme directory name is one of the two built-in themes
// shipped with the kernel appearance tree.
func isBuiltInTheme(name string) bool {
	return name == "daylight" || name == "midnight"
}

package natsx

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/adrg/xdg"
)

// configParent mirrors the directory logic in orbit.go's natscontext and in
// nats-cli (jsm.go/natscontext): $XDG_CONFIG_HOME when set, otherwise
// $HOME/.config — on *every* platform, Windows and macOS included.
//
// This is deliberately NOT xdg.ConfigHome. adrg/xdg follows platform
// convention, so ConfigHome is %LOCALAPPDATA% on Windows and
// ~/Library/Application Support on macOS. Contexts written there are invisible
// to both `nats` and natscontext.Connect, which is exactly how sync-context
// could report success and then fail with `unknown context` on the next call.
func configParent() string {
	if p := os.Getenv("XDG_CONFIG_HOME"); p != "" {
		return p
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".config")
	}
	return xdg.ConfigHome // last resort; still better than an empty path
}

// ContextDir is the directory nats-cli reads context JSON files from.
func ContextDir() string { return filepath.Join(configParent(), "nats", "context") }

// ContextPath is the file nats-cli reads the named context from.
func ContextPath(name string) string { return filepath.Join(ContextDir(), name+".json") }

// SelectedContextPath is nats-cli's pointer to the selected context.
func SelectedContextPath() string { return filepath.Join(configParent(), "nats", "context.txt") }

// legacyContextPath is where stone used to write contexts (adrg/xdg's
// ConfigHome). On Linux it is the same path as ContextPath; on Windows and
// macOS it is a dead drop nats-cli never reads. Kept so we can find those
// orphans and clean them up.
func legacyContextPath(name string) string {
	return filepath.Join(xdg.ConfigHome, "nats", "context", name+".json")
}

// ContextExists reports whether nats-cli can see the named context.
func ContextExists(name string) bool {
	if name == "" {
		return false
	}
	_, err := os.Stat(ContextPath(name))
	return err == nil
}

// ResolveContextFile returns an absolute path to the named nats-cli context,
// which natscontext.Connect accepts directly (bypassing its own lookup, so the
// two can never disagree about where contexts live). An absolute path is
// passed through untouched.
func ResolveContextFile(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("no nats context set on this stone context.\n" +
			"       run: stone nats sync-context   (or pass --nats-context <name>)")
	}
	if filepath.IsAbs(name) {
		if _, err := os.Stat(name); err != nil {
			return "", fmt.Errorf("nats context file %s: %w", name, err)
		}
		return name, nil
	}
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return "", fmt.Errorf("invalid nats context name %q", name)
	}
	path := ContextPath(name)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	if legacy := legacyContextPath(name); legacy != path {
		if _, err := os.Stat(legacy); err == nil {
			return "", fmt.Errorf("nats context %q lives at %s, where nats-cli does not look for it (it reads %s).\n"+
				"       run: stone nats sync-context   to rewrite it in the right place", name, legacy, path)
		}
	}
	return "", fmt.Errorf("nats context %q not found at %s.\n"+
		"       run: stone nats sync-context   to generate it", name, path)
}

// cleanupLegacyContext removes a stone-managed context left behind in the old
// (xdg.ConfigHome) location. It only deletes files it can read, parse, and
// confirm were written by stone; anything else is left alone. Returns the path
// removed, or "" when there was nothing to do.
func cleanupLegacyContext(name string) string {
	legacy := legacyContextPath(name)
	if legacy == ContextPath(name) {
		return ""
	}
	data, err := os.ReadFile(legacy)
	if err != nil {
		return ""
	}
	var f natsCtxFile
	if err := json.Unmarshal(data, &f); err != nil {
		return ""
	}
	if !strings.HasPrefix(f.Description, managedPrefix) {
		return ""
	}
	if err := os.Remove(legacy); err != nil {
		return ""
	}
	return legacy
}

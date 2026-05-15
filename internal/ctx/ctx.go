package ctx

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/adrg/xdg"
	"gopkg.in/yaml.v3"
)

// GlobalConfig is the top-level CLI config stored at ~/.config/stone/config.yaml.
type GlobalConfig struct {
	ActiveContext string `yaml:"active_context,omitempty"`
	Output        string `yaml:"output,omitempty"` // table | json | yaml
}

// AuthSection holds the PocketBase auth state for a context.
type AuthSection struct {
	Collection string `yaml:"collection"`
	Token      string `yaml:"token,omitempty"`
	Expires    string `yaml:"expires,omitempty"`
	UserID     string `yaml:"user_id,omitempty"`
	Email      string `yaml:"email,omitempty"`
}

// Context is a single named environment: URL, auth, org, NATS context, workspace.
type Context struct {
	Name                string      `yaml:"name"`
	URL                 string      `yaml:"url"`
	Auth                AuthSection `yaml:"auth"`
	CurrentOrganization string      `yaml:"current_organization,omitempty"`
	NATSURL             string      `yaml:"nats_url,omitempty"`     // e.g. nats://host:4222 — used when generating per-org nats-cli contexts
	NATSContext         string      `yaml:"nats_context,omitempty"` // nats-cli context name 'stone org switch' last wrote
	Workspace           string      `yaml:"workspace,omitempty"`
}

var validName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,50}$`)

// ValidateName ensures a context name is filesystem-safe.
func ValidateName(name string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("invalid context name %q: must match %s", name, validName.String())
	}
	return nil
}

// Root returns the stone config root, creating it on demand.
func Root() (string, error) {
	root := filepath.Join(xdg.ConfigHome, "stone")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("create config dir: %w", err)
	}
	return root, nil
}

func globalConfigPath() (string, error) {
	r, err := Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(r, "config.yaml"), nil
}

func contextDir(name string) (string, error) {
	r, err := Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(r, "contexts", name), nil
}

func contextFile(name string) (string, error) {
	d, err := contextDir(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "context.yaml"), nil
}

// LoadGlobal returns the global config, an empty struct if missing.
func LoadGlobal() (GlobalConfig, error) {
	var g GlobalConfig
	p, err := globalConfigPath()
	if err != nil {
		return g, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return g, nil
	}
	if err != nil {
		return g, fmt.Errorf("read global config: %w", err)
	}
	if err := yaml.Unmarshal(data, &g); err != nil {
		return g, fmt.Errorf("parse global config: %w", err)
	}
	return g, nil
}

// SaveGlobal persists the global config.
func SaveGlobal(g GlobalConfig) error {
	p, err := globalConfigPath()
	if err != nil {
		return err
	}
	data, err := yaml.Marshal(g)
	if err != nil {
		return fmt.Errorf("marshal global config: %w", err)
	}
	return os.WriteFile(p, data, 0o600)
}

// Load returns a named context.
func Load(name string) (Context, error) {
	var c Context
	if err := ValidateName(name); err != nil {
		return c, err
	}
	p, err := contextFile(name)
	if err != nil {
		return c, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return c, fmt.Errorf("context %q not found", name)
	}
	if err != nil {
		return c, fmt.Errorf("read context: %w", err)
	}
	if err := yaml.Unmarshal(data, &c); err != nil {
		return c, fmt.Errorf("parse context: %w", err)
	}
	if c.Name == "" {
		c.Name = name
	}
	return c, nil
}

// Save persists a context to disk.
func Save(c Context) error {
	if err := ValidateName(c.Name); err != nil {
		return err
	}
	d, err := contextDir(c.Name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(d, 0o755); err != nil {
		return fmt.Errorf("create context dir: %w", err)
	}
	p := filepath.Join(d, "context.yaml")
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal context: %w", err)
	}
	return os.WriteFile(p, data, 0o600)
}

// Delete removes a context and its directory.
func Delete(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	d, err := contextDir(name)
	if err != nil {
		return err
	}
	return os.RemoveAll(d)
}

// List returns all known context names.
func List() ([]string, error) {
	r, err := Root()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(r, "contexts"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list contexts: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// Active returns the active context, considering an optional override name.
// If override is empty, falls back to global config's active_context.
func Active(override string) (Context, error) {
	name := override
	if name == "" {
		g, err := LoadGlobal()
		if err != nil {
			return Context{}, err
		}
		name = g.ActiveContext
	}
	if name == "" {
		return Context{}, errors.New("no active context. Run: stone context create <name> --url <url>")
	}
	return Load(name)
}

// SetActive updates the global config's active_context, validating that the named context exists.
func SetActive(name string) error {
	if _, err := Load(name); err != nil {
		return err
	}
	g, err := LoadGlobal()
	if err != nil {
		return err
	}
	g.ActiveContext = name
	return SaveGlobal(g)
}

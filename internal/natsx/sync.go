package natsx

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/adrg/xdg"
	"github.com/stone-age-io/stone-cli/internal/ctx"
	"github.com/stone-age-io/stone-cli/internal/pb"
)

// natsCtxFile mirrors the on-disk shape used by nats-cli and orbit.go.
// We only set the fields a JWT/creds-based user needs; the rest default to
// the zero value, which orbit.go and nats-cli both tolerate.
type natsCtxFile struct {
	Description string `json:"description"`
	URL         string `json:"url"`
	SocksProxy  string `json:"socks_proxy"`
	Token       string `json:"token"`
	User        string `json:"user"`
	Password    string `json:"password"`
	Creds       string `json:"creds"`
	NKey        string `json:"nkey"`
	Cert        string `json:"cert"`
	Key         string `json:"key"`
	CA          string `json:"ca"`
	NSCLookup   string `json:"nsc"`
	JSDomain    string `json:"jetstream_domain"`
	JSAPIPrefix string `json:"jetstream_api_prefix"`
	JSEventPfx  string `json:"jetstream_event_prefix"`
	InboxPrefix string `json:"inbox_prefix"`
	UserJwt     string `json:"user_jwt"`
	TLSFirst    bool   `json:"tls_first"`
}

// SyncOptions configures a nats-cli context write.
type SyncOptions struct {
	StoneContext string // stone context name (for namespacing)
	OrgID        string
	OrgName      string
	NATSURL      string
	NATSUser     pb.Record // the nats_users record (must have creds_file)
	Description  string
	SetSelected  bool // also update ~/.config/nats/context.txt
}

// SyncResult reports where files landed and what label to use.
type SyncResult struct {
	Name      string // nats-cli context name
	CredsPath string // absolute path to the written .creds file
	CtxPath   string // absolute path to the written nats-cli context JSON
}

// natsContextName returns the canonical nats-cli context name for a stone
// context + org pair: "stone-<stoneCtx>-<sanitizedOrgName>".
func natsContextName(stoneCtx, orgName string) string {
	return "stone-" + sanitize(stoneCtx) + "-" + sanitize(orgName)
}

// SyncContextForOrg writes the .creds file plus a nats-cli context JSON for
// the given org, returning paths and the resulting nats-cli context name.
func SyncContextForOrg(opts SyncOptions) (SyncResult, error) {
	var res SyncResult
	if opts.NATSURL == "" {
		return res, errors.New("no NATS URL set on the stone context (run: stone context create --nats-url ...)")
	}
	if opts.OrgName == "" {
		return res, errors.New("org name is required")
	}

	credsContent, _ := opts.NATSUser["creds_file"].(string)
	credsContent = strings.TrimSpace(credsContent)
	if credsContent == "" {
		// Fall back to building from JWT + seed if those are populated.
		if jwt, ok := opts.NATSUser["jwt"].(string); ok && jwt != "" {
			if seed, ok2 := opts.NATSUser["seed"].(string); ok2 && seed != "" {
				credsContent = buildCredsFromJWTAndSeed(jwt, seed)
			}
		}
	}
	if credsContent == "" {
		return res, errors.New("nats_users record has no creds_file (and no jwt+seed to build one); set 'regenerate=true' on the record to re-issue")
	}

	res.Name = natsContextName(opts.StoneContext, opts.OrgName)
	credsPath, err := writeCredsFile(res.Name, credsContent)
	if err != nil {
		return res, err
	}
	res.CredsPath = credsPath
	ctxPath, err := writeNATSContextFile(res.Name, natsCtxFile{
		Description: defaultDescription(opts),
		URL:         opts.NATSURL,
		Creds:       credsPath,
	})
	if err != nil {
		return res, err
	}
	res.CtxPath = ctxPath
	if opts.SetSelected {
		if err := setSelectedContext(res.Name); err != nil {
			return res, fmt.Errorf("wrote %s but failed to set selected: %w", ctxPath, err)
		}
	}
	return res, nil
}

func defaultDescription(opts SyncOptions) string {
	parts := []string{"managed by stone"}
	if opts.StoneContext != "" {
		parts = append(parts, "stone-context="+opts.StoneContext)
	}
	if opts.OrgName != "" {
		parts = append(parts, "org="+opts.OrgName)
	}
	if opts.OrgID != "" {
		parts = append(parts, "org-id="+opts.OrgID)
	}
	return strings.Join(parts, "; ")
}

// writeCredsFile writes the .creds content to a stone-owned path and returns it.
func writeCredsFile(name, content string) (string, error) {
	dir := filepath.Join(xdg.ConfigHome, "stone", "creds")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create creds dir: %w", err)
	}
	path := filepath.Join(dir, name+".creds")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", fmt.Errorf("write creds: %w", err)
	}
	return path, nil
}

// writeNATSContextFile writes the JSON file nats-cli/orbit.go expect.
func writeNATSContextFile(name string, f natsCtxFile) (string, error) {
	dir := filepath.Join(xdg.ConfigHome, "nats", "context")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create nats context dir: %w", err)
	}
	path := filepath.Join(dir, name+".json")
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("write nats context: %w", err)
	}
	return path, nil
}

// setSelectedContext updates ~/.config/nats/context.txt.
func setSelectedContext(name string) error {
	dir := filepath.Join(xdg.ConfigHome, "nats")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "context.txt"), []byte(name+"\n"), 0o644)
}

// sanitize replaces characters that orbit.go's validName rejects.
func sanitize(s string) string {
	if s == "" {
		return "default"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-' || r == '_':
			b.WriteRune(r)
		case r == ' ', r == '.', r == '/', r == '\\':
			b.WriteRune('-')
		}
	}
	out := b.String()
	if out == "" {
		out = "default"
	}
	return strings.ToLower(out)
}

// buildCredsFromJWTAndSeed reconstructs a .creds file from its two parts.
// This mirrors the nsc/nats-cli "creds" format precisely.
func buildCredsFromJWTAndSeed(jwt, seed string) string {
	return "-----BEGIN NATS USER JWT-----\n" + strings.TrimSpace(jwt) +
		"\n------END NATS USER JWT------\n\n" +
		"************************* IMPORTANT *************************\n" +
		"NKEY Seed printed below can be used to sign and prove identity.\n" +
		"NKEYs are sensitive and should be treated as secrets.\n\n" +
		"-----BEGIN USER NKEY SEED-----\n" + strings.TrimSpace(seed) +
		"\n------END USER NKEY SEED------\n\n" +
		"*************************************************************\n"
}

// MembershipForOrg returns the calling user's membership record for orgID,
// or nil if there is none. Caller must be authenticated.
func MembershipForOrg(c *pb.Client, userCollection, userID, orgID string) (pb.Record, error) {
	filter := fmt.Sprintf(`user="%s" && organization="%s"`, escape(userID), escape(orgID))
	lr, err := c.List("memberships", pb.ListOptions{Filter: filter, PerPage: 2})
	if err != nil {
		return nil, err
	}
	switch len(lr.Items) {
	case 0:
		return nil, nil
	case 1:
		return lr.Items[0], nil
	default:
		return lr.Items[0], nil // shouldn't happen — index is unique
	}
}

// FetchNATSUserForMembership returns the nats_users record linked to the
// given membership, or nil if the membership has no nats_user.
func FetchNATSUserForMembership(c *pb.Client, m pb.Record) (pb.Record, error) {
	id, _ := m["nats_user"].(string)
	if id == "" {
		return nil, nil
	}
	return c.Get("nats_users", id)
}

func escape(s string) string {
	return strings.ReplaceAll(s, `"`, `\"`)
}

// Silence "imported and not used" if ctx isn't directly referenced — the
// package imports it for the public type it references in SyncOptions.
var _ = ctx.Context{}

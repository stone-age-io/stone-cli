package cmd

// jsync.go -- `stone js pull` / `stone js apply`: JetStream stream and KV
// bucket configuration as files in the workspace, beside the PocketBase
// records `stone pull` writes. Configuration only: no messages, no consumers.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/spf13/cobra"
	"github.com/stone-age-io/stone-cli/internal/ctx"
	"github.com/stone-age-io/stone-cli/internal/natsx"
	"github.com/stone-age-io/stone-cli/internal/pb"
	"gopkg.in/yaml.v3"
)

const (
	// jsDir is the workspace subdirectory `stone js pull` writes and
	// `stone apply` steps over.
	jsDir        = "jetstream"
	jsStreamsDir = "streams"
	jsKVDir      = "kv"
	// jsDomainsDir holds one directory per leaf node's JetStream domain,
	// jetstream/domains/<code>/streams|kv/, beside the hub's jetstream/streams|kv/.
	jsDomainsDir = "domains"

	// Keys a workspace file carries that are not stream or bucket config.
	// apply strips them before comparing or sending.
	jsKeyOrg    = "organization"
	jsKeyHash   = "config_hash"
	jsKeyDomain = "domain"

	kvStreamPrefix  = "KV_"
	objStreamPrefix = "OBJ_"

	jsErrStreamNotFound = 10059
)

type jsKind string

const (
	kindStream jsKind = "stream"
	kindKV     jsKind = "kv"
)

// jsDurationPaths are the fields, as dotted paths, that the server encodes as
// integer nanoseconds. Files carry them as Go duration strings ("24h"), and
// apply accepts either.
var jsDurationPaths = map[jsKind][]string{
	kindStream: {"max_age", "duplicate_window", "subject_delete_marker_ttl", "consumer_limits.inactive_threshold"},
	kindKV:     {"ttl", "limit_marker_ttl"},
}

// jsDefaults are the top-level values the server fills in for a field the
// request left out, so normalizing drops them: leaving a field out of a
// create or update gives exactly this value (verified against nats-server
// 2.14). Every zero value -- false, 0, "", {}, [] -- is dropped as well,
// because omitting a field decodes to its zero value on the server.
//
// duplicate_window's default is 2m only when max_age is 0 or at least 2m;
// otherwise it is max_age, which then stays in the file. Sending it
// explicitly is the same as letting the server derive it.
var jsDefaults = map[jsKind]map[string]any{
	kindStream: {
		"retention":            "limits",
		"discard":              "old",
		"storage":              "file",
		"compression":          "none",
		"max_consumers":        int64(-1),
		"max_msgs":             int64(-1),
		"max_bytes":            int64(-1),
		"max_msgs_per_subject": int64(-1),
		"max_msg_size":         int64(-1),
		"num_replicas":         int64(1),
		"duplicate_window":     int64(2 * time.Minute),
	},
	kindKV: {
		"history":      int64(1),
		"num_replicas": int64(1),
	},
}

// ---- commands ---------------------------------------------------------------

var jsPullCmd = &cobra.Command{
	Use:   "pull",
	Short: "Write stream and KV bucket config into <workspace>/jetstream/",
	Long: `Pull writes the configuration of every stream in the organization's NATS
account into the workspace, one YAML file each:

  <workspace>/jetstream/streams/<stream>.yaml   a stream config
  <workspace>/jetstream/kv/<bucket>.yaml        a KV bucket config

KV buckets are written as buckets (history, ttl, ...), not as their backing
KV_<bucket> stream, so the settings a bucket depends on cannot be edited away.
Object store buckets (OBJ_*) are skipped. Messages and consumers are not
pulled: this is configuration, not a backup.

Values the server fills in by default are left out, and durations are written
as "24h" rather than nanoseconds. Each file also records the organization
and a config_hash of the server's config, which apply uses to refuse a file
the server has changed since. Pull overwrites files: commit edits first.

--domain <thing> pulls an edge site's leaf node instead of the hub. A leaf's
JetStream domain is its Thing's code; name the thing by code or id. Its files
go to <workspace>/jetstream/domains/<code>/streams|kv/ and record the domain.
The requests cross the leaf link, and the hub forwards to a leaf only what the
leaf's uplink credential (the Thing's NATS user) may subscribe to, so its NATS
role must allow subscribe on $JS.<code>.API.> (or $JS.*.API.>).`,
	Args: cobra.NoArgs,
	RunE: runJSPull,
}

var jsApplyCmd = &cobra.Command{
	Use:   "apply [<path>...]",
	Short: "Create or update streams and KV buckets from <workspace>/jetstream/",
	Long: `Apply reads stream files from jetstream/streams/ and bucket files from
jetstream/kv/ (the workspace's, or the paths given), compares each with the
server, and creates what is missing and updates what differs. Unchanged
streams are not sent. --dry-run prints what would change without sending it.

A file holds the whole config: a field left out is set back to the server's
default, the same as 'nats stream edit' with a config file. Apply never
deletes a stream or bucket that has no file.

Apply refuses, and sends nothing, when:
  - a file's config_hash is not the server's: the stream changed since the
    file was pulled, and the file would overwrite that change;
  - a file has a config_hash but its stream no longer exists: someone deleted
    it, and apply would bring it back;
  - a bucket's backing stream has settings a bucket file cannot express
    (made with 'nats stream edit', say), which an update would reset.
--force sends anyway. A file whose organization is not the current one stops
apply too, unless --any-org is given.

--domain <thing> applies to an edge site's leaf node instead of the hub (see
'stone js pull --help'), reading jetstream/domains/<code>/ by default. Without
it apply reads jetstream/ and steps over its domains/ directory. A file whose
domain -- its domain key, or the domains/<code>/ directory it is in -- is not
the one apply is connected to stops apply before anything is sent.

The server checks every config and refuses changes it cannot make (storage
type, for one); apply reports those per file and carries on with the rest.
After each write, apply records the new config_hash in the file, which
drops any comments in it.

Run over the whole workspace, apply also lists the streams and buckets on the
server that no file declares ("no file"). It leaves them alone.

For CI: --dry-run --exit-code exits 2 when apply would create or update
something, 0 when the workspace and the server agree, and 1 on an error.
"no file" streams do not count.`,
	RunE: runJSApply,
}

func init() {
	jsPullCmd.Flags().String("workspace", "", "workspace directory (default: context's workspace, then cwd)")
	jsApplyCmd.Flags().String("workspace", "", "workspace directory (default: context's workspace, then cwd)")
	jsApplyCmd.Flags().Bool("dry-run", false, "print what would be created and updated (and which fields) without sending it")
	jsApplyCmd.Flags().Bool("force", false, "send files the server has changed or deleted since they were pulled")
	jsApplyCmd.Flags().Bool("any-org", false, "apply files that belong to organizations other than the current one")
	jsApplyCmd.Flags().Bool("exit-code", false, "with --dry-run: exit 2 when apply would create or update something, 0 when not")
	jsPullCmd.Flags().String("domain", "", "pull an edge site's leaf node: the JetStream domain of this thing (code or id)")
	jsApplyCmd.Flags().String("domain", "", "apply to an edge site's leaf node: the JetStream domain of this thing (code or id)")
	jsCmd.AddCommand(jsPullCmd, jsApplyCmd)
}

func runJSPull(cmd *cobra.Command, args []string) error {
	c, err := ctx.Active(flagContext)
	if err != nil {
		return err
	}
	ws, err := resolveWorkspace(cmd, c)
	if err != nil {
		return err
	}
	org, err := jsConnOrg(c)
	if err != nil {
		return err
	}
	domainArg, _ := cmd.Flags().GetString("domain")
	domain, err := resolveJSDomain(c, domainArg)
	if err != nil {
		return err
	}
	js, cleanup, err := openJSDomain(domain)
	if err != nil {
		return err
	}
	defer cleanup()
	cctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := checkJSDomain(cctx, newJSAPI(js)); err != nil {
		return err
	}
	return jsPull(cctx, js, jsWorkspaceDir(ws, domain), org, os.Stdout, os.Stderr)
}

func runJSApply(cmd *cobra.Command, args []string) error {
	c, err := ctx.Active(flagContext)
	if err != nil {
		return err
	}
	ws, err := resolveWorkspace(cmd, c)
	if err != nil {
		return err
	}
	var opt jsApplyOptions
	opt.DryRun, _ = cmd.Flags().GetBool("dry-run")
	opt.Force, _ = cmd.Flags().GetBool("force")
	opt.AnyOrg, _ = cmd.Flags().GetBool("any-org")
	opt.ExitCode, _ = cmd.Flags().GetBool("exit-code")
	if opt.ExitCode && !opt.DryRun {
		return errors.New("--exit-code needs --dry-run")
	}
	// With paths given, every stream outside them would look unmanaged.
	opt.ReportNoFile = len(args) == 0
	domainArg, _ := cmd.Flags().GetString("domain")
	if opt.Domain, err = resolveJSDomain(c, domainArg); err != nil {
		return err
	}

	roots := args
	if len(roots) == 0 {
		roots = []string{jsWorkspaceDir(ws, opt.Domain)}
	}
	files, err := readJSFiles(roots, os.Stderr)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no stream or bucket files found under: %v (run `stone js pull` first)", roots)
	}
	org, err := jsConnOrg(c)
	if err != nil {
		return err
	}
	js, cleanup, err := openJSDomain(opt.Domain)
	if err != nil {
		return err
	}
	defer cleanup()
	cctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if err := checkJSDomain(cctx, newJSAPI(js)); err != nil {
		return err
	}
	return jsApply(cctx, js, files, org, opt, os.Stdout, os.Stderr)
}

// jsWorkspaceDir is where pull writes, and where apply reads by default: the
// hub's files at jetstream/, a leaf's at jetstream/domains/<code>/.
func jsWorkspaceDir(ws, domain string) string {
	if domain == "" {
		return filepath.Join(ws, jsDir)
	}
	return filepath.Join(ws, jsDir, jsDomainsDir, domain)
}

// resolveJSDomain turns --domain into a JetStream domain. A leaf node's domain
// is its Thing's code: the platform computes it, and the agent writes it into
// nats-leaf.conf. Looking the thing up, rather than taking the string as given,
// makes a typo "no thing" here instead of a request no leaf answers, and turns
// an id or a differently cased code into the exact code the leaf uses.
func resolveJSDomain(c ctx.Context, arg string) (string, error) {
	if arg == "" {
		return "", nil
	}
	spec, ok := specByCollection("things")
	if !ok {
		return "", errors.New("no thing entity") // cannot happen: entity.go declares it
	}
	client := newPBClient(c)
	id, err := resolveRecordID(client, spec, c.CurrentOrganization, arg)
	if err != nil {
		return "", fmt.Errorf("--domain: %w", err)
	}
	rec, err := client.Get(spec.Collection, id, pb.GetOptions{Fields: "code"})
	if err != nil {
		return "", fmt.Errorf("--domain: %w", err)
	}
	code, _ := rec["code"].(string)
	if code == "" {
		return "", fmt.Errorf("--domain: thing %s has no code, and a leaf node's JetStream domain is its code", id)
	}
	return code, nil
}

// checkJSDomain asks a leaf's JetStream for its account info before anything
// else, so an unreachable domain fails once, with the reason, rather than as
// whatever the first stream request makes of it. A no-op for the hub.
func checkJSDomain(cctx context.Context, api jsAPI) error {
	if api.domain == "" {
		return nil
	}
	_, err := api.nc.RequestWithContext(cctx, api.prefix+"INFO", nil)
	if errors.Is(err, nats.ErrNoResponders) {
		return api.noResponders("INFO")
	}
	if err != nil {
		return fmt.Errorf("JetStream domain %s: %w", api.domain, err)
	}
	return nil
}

// jsConnOrg names the organization whose NATS account the connection will
// reach. The stone context's current organization and its nats_context are
// set by separate steps (`org switch --no-nats` does only the first), so they
// can disagree; pull would then label one org's streams with another's id,
// and apply would write a workspace into the wrong account. The nats-cli
// context stone writes records its org id, which decides it; a context stone
// did not write falls back to the current organization.
func jsConnOrg(c ctx.Context) (string, error) {
	name := c.NATSContext
	if flagNATSContext != "" {
		name = flagNATSContext
	}
	path, err := natsx.ResolveContextFile(name)
	if err != nil {
		return "", err
	}
	id := natsx.ContextOrgID(path)
	if id == "" {
		return c.CurrentOrganization, nil
	}
	if c.CurrentOrganization != "" && id != c.CurrentOrganization {
		return "", fmt.Errorf("nats context %q belongs to organization %s, but the current organization is %s.\n"+
			"       run: stone nats sync-context   to connect as the current organization",
			name, jsOrgLabel(c, id), orgLabel(c))
	}
	return id, nil
}

// ---- pull -------------------------------------------------------------------

func jsPull(cctx context.Context, js jetstream.JetStream, dir, org string, out, errOut io.Writer) error {
	api := newJSAPI(js)
	domain := js.Options().Domain
	lister := js.StreamNames(cctx)
	var names []string
	for n := range lister.Name() {
		names = append(names, n)
	}
	if err := lister.Err(); err != nil {
		return fmt.Errorf("list streams: %w", err)
	}
	sort.Strings(names)

	var nStreams, nKV int
	var objects []string
	used := map[string]bool{}
	for _, name := range names {
		if strings.HasPrefix(name, objStreamPrefix) {
			objects = append(objects, strings.TrimPrefix(name, objStreamPrefix))
			continue
		}
		raw, found, err := api.streamConfig(cctx, name)
		if err != nil {
			return fmt.Errorf("stream %s: %w", name, err)
		}
		if !found {
			continue // deleted since it was listed
		}
		hash, err := jsConfigHash(raw)
		if err != nil {
			return fmt.Errorf("stream %s: %w", name, err)
		}

		var rec map[string]any
		var sub, base string
		if strings.HasPrefix(name, kvStreamPrefix) {
			kv := kvConfigFromStream(raw)
			if rec, err = kvFileForm(kv); err != nil {
				return fmt.Errorf("bucket %s: %w", kv.Bucket, err)
			}
			unrep, err := kvUnrepresentable(raw)
			if err != nil {
				return fmt.Errorf("bucket %s: %w", kv.Bucket, err)
			}
			if len(unrep) > 0 {
				fmt.Fprintf(errOut, "warning  kv/%s: its stream %s has settings a bucket file cannot express (%s); "+
					"apply will refuse to update it without --force, which would reset them\n",
					kv.Bucket, name, strings.Join(unrep, ", "))
			}
			sub, base = jsKVDir, kv.Bucket
			nKV++
		} else {
			if rec, err = normalizeJSConfig(kindStream, raw); err != nil {
				return fmt.Errorf("stream %s: %w", name, err)
			}
			sub, base = jsStreamsDir, name
			nStreams++
		}
		if org != "" {
			rec[jsKeyOrg] = org
		}
		if domain != "" {
			rec[jsKeyDomain] = domain
		}
		rec[jsKeyHash] = hash

		fname := sanitizeFilename(base)
		for i := 2; used[sub+"/"+fname]; i++ {
			fname = fmt.Sprintf("%s-%d", sanitizeFilename(base), i)
		}
		used[sub+"/"+fname] = true
		path := filepath.Join(dir, sub, fname+".yaml")
		if err := pb.MarshalFile(path, rec); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	fmt.Fprintf(out, "%-10s %4d → %s\n", "streams", nStreams, filepath.Join(dir, jsStreamsDir))
	fmt.Fprintf(out, "%-10s %4d → %s\n", "kv", nKV, filepath.Join(dir, jsKVDir))
	if len(objects) > 0 {
		fmt.Fprintf(out, "skipped %d object store bucket(s): %s (not managed by js pull/apply)\n",
			len(objects), strings.Join(objects, ", "))
	}
	return nil
}

// ---- apply ------------------------------------------------------------------

type jsApplyOptions struct {
	DryRun, Force, AnyOrg bool
	ExitCode              bool // dry run: errChangesPending when something would be sent
	ReportNoFile          bool // list streams and buckets on the server that no file declares

	// Domain is the leaf node's JetStream domain apply is connected to, or ""
	// for the hub (the nats context's own JetStream).
	Domain string
}

// jsFile is one workspace file and, once planned, what apply will do with it.
type jsFile struct {
	Path string
	Kind jsKind
	Name string         // stream name, or bucket name for kind kv
	Raw  pb.Record      // the whole file, for write-back
	Body map[string]any // Raw minus the reserved keys
	Org  string
	Hash string

	// Domain is the file's domain key; DirDomain is <code> when the file is in
	// jetstream/domains/<code>/. Both "" is a hub file.
	Domain    string
	DirDomain string

	server  map[string]any // the server's raw stream config; nil = no such stream
	changes []string       // top-level keys that differ from the server
}

func (f *jsFile) label() string {
	return filepath.Base(filepath.Dir(f.Path)) + "/" + filepath.Base(f.Path)
}

// domain is the JetStream domain f belongs to: its domain key, else the
// domains/<code>/ directory it is in, else "" for the hub.
func (f *jsFile) domain() string {
	if f.Domain != "" {
		return f.Domain
	}
	return f.DirDomain
}

// belongsTo reports whether f may be applied where opt points. hubDomain is
// the domain of the hub connection, if its nats context names one: a hub file
// pulled through such a context records it.
func (f *jsFile) belongsTo(opt jsApplyOptions, hubDomain string) bool {
	d := f.domain()
	if opt.Domain != "" {
		return d == opt.Domain
	}
	return d == "" || d == hubDomain
}

func (f *jsFile) streamName() string {
	if f.Kind == kindKV {
		return kvStreamPrefix + f.Name
	}
	return f.Name
}

// readJSFiles walks roots for stream and bucket files. The kind comes from the
// parent directory, as collections do for `stone apply`.
func readJSFiles(roots []string, errOut io.Writer) ([]*jsFile, error) {
	var files []*jsFile
	for _, root := range roots {
		abs, err := filepath.Abs(root)
		if err != nil {
			return nil, err
		}
		err = filepath.WalkDir(abs, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if strings.HasPrefix(d.Name(), ".") && path != abs {
					return filepath.SkipDir
				}
				// The leaves' files, inside the hub's. They are applied
				// with --domain, whose walk starts inside one of them.
				if d.Name() == jsDomainsDir && path != abs {
					return filepath.SkipDir
				}
				return nil
			}
			ext := strings.ToLower(filepath.Ext(path))
			if ext != ".yaml" && ext != ".yml" && ext != ".json" {
				return nil
			}
			var kind jsKind
			switch filepath.Base(filepath.Dir(path)) {
			case jsStreamsDir:
				kind = kindStream
			case jsKVDir:
				kind = kindKV
			default:
				fmt.Fprintf(errOut, "skip     %s (not in a %s/ or %s/ directory)\n", path, jsStreamsDir, jsKVDir)
				return nil
			}
			f, err := parseJSFile(path, kind)
			if err != nil {
				return err
			}
			files = append(files, f)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return files, nil
}

func parseJSFile(path string, kind jsKind) (*jsFile, error) {
	raw, err := pb.UnmarshalFile(path)
	if err != nil {
		return nil, err
	}
	f := &jsFile{Path: path, Kind: kind, Raw: raw, Body: map[string]any{}}
	for k, v := range raw {
		switch k {
		case jsKeyOrg:
			f.Org, _ = v.(string)
		case jsKeyHash:
			f.Hash, _ = v.(string)
		case jsKeyDomain:
			f.Domain, _ = v.(string)
		default:
			f.Body[k] = v
		}
	}
	nameKey := "name"
	if kind == kindKV {
		nameKey = "bucket"
	}
	// .../domains/<code>/streams/<file>
	if kindDir := filepath.Dir(path); filepath.Base(filepath.Dir(filepath.Dir(kindDir))) == jsDomainsDir {
		f.DirDomain = filepath.Base(filepath.Dir(kindDir))
	}
	f.Name, _ = f.Body[nameKey].(string)
	if f.Name == "" {
		return nil, fmt.Errorf("%s: no %q", path, nameKey)
	}
	if kind == kindStream && (strings.HasPrefix(f.Name, kvStreamPrefix) || strings.HasPrefix(f.Name, objStreamPrefix)) {
		return nil, fmt.Errorf("%s: %s backs a KV or object store bucket; manage a KV bucket as %s/%s.yaml",
			path, f.Name, jsKVDir, strings.TrimPrefix(strings.TrimPrefix(f.Name, kvStreamPrefix), objStreamPrefix))
	}
	return f, nil
}

func jsApply(cctx context.Context, js jetstream.JetStream, files []*jsFile, org string, opt jsApplyOptions, out, errOut io.Writer) error {
	sort.SliceStable(files, func(i, j int) bool {
		if files[i].Kind != files[j].Kind {
			return files[i].Kind == kindStream // streams before buckets
		}
		return files[i].Name < files[j].Name
	})

	// Refuse up front, as `stone apply` does, rather than half-apply.
	hubDomain := ""
	if opt.Domain == "" {
		hubDomain = js.Options().Domain
	}
	seen := map[string]string{}
	var problems []string
	for _, f := range files {
		if prev, ok := seen[f.streamName()]; ok {
			problems = append(problems, fmt.Sprintf("  %s and %s both declare stream %s", prev, f.Path, f.streamName()))
		}
		seen[f.streamName()] = f.Path
		if !opt.AnyOrg && org != "" && f.Org != "" && f.Org != org {
			problems = append(problems, fmt.Sprintf("  %s belongs to organization %s, not the current %s (--any-org to apply it anyway)", f.Path, f.Org, org))
		}
		if !f.belongsTo(opt, hubDomain) {
			problems = append(problems, fmt.Sprintf("  %s belongs to %s, but apply is connected to %s", f.Path, jsDomainLabel(f.domain()), jsDomainLabel(opt.Domain)))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("nothing was sent:\n%s", strings.Join(problems, "\n"))
	}

	api := newJSAPI(js)
	var creates, updates []*jsFile
	var refused []string
	unchanged := 0
	for _, f := range files {
		server, found, err := api.streamConfig(cctx, f.streamName())
		if err != nil {
			return fmt.Errorf("%s: %w", f.label(), err)
		}
		if !found {
			if f.Hash != "" && !opt.Force {
				refused = append(refused, fmt.Sprintf("  %s: stream %s was deleted on the server since this file was pulled (delete the file, or --force to recreate it)", f.Path, f.streamName()))
				continue
			}
			creates = append(creates, f)
			continue
		}
		f.server = server
		local, err := f.compareForm()
		if err != nil {
			return fmt.Errorf("%s: %w", f.Path, err)
		}
		remote, err := serverCompareForm(f.Kind, server)
		if err != nil {
			return fmt.Errorf("%s: %w", f.label(), err)
		}
		if f.changes = changedKeys(remote, local); len(f.changes) == 0 {
			unchanged++
			continue
		}
		if !opt.Force {
			if now, err := jsConfigHash(server); err != nil {
				return fmt.Errorf("%s: %w", f.label(), err)
			} else if f.Hash != "" && f.Hash != now {
				refused = append(refused, fmt.Sprintf("  %s: stream %s changed on the server since this file was pulled (file: %s, server: %s)", f.Path, f.streamName(), f.Hash, now))
				continue
			}
			if f.Kind == kindKV {
				unrep, err := kvUnrepresentable(server)
				if err != nil {
					return fmt.Errorf("%s: %w", f.label(), err)
				}
				if len(unrep) > 0 {
					refused = append(refused, fmt.Sprintf("  %s: stream %s has settings a bucket file cannot express (%s), which an update would reset", f.Path, f.streamName(), strings.Join(unrep, ", ")))
					continue
				}
			}
		}
		updates = append(updates, f)
	}
	if len(refused) > 0 {
		return fmt.Errorf("%d file(s) refused; nothing was sent.\n%s\n"+
			"commit your edits, run `stone js pull`, and use `git diff` to put your changes back; or pass --force",
			len(refused), strings.Join(refused, "\n"))
	}

	var noFile []string
	noFileSummary := ""
	if opt.ReportNoFile {
		var err error
		if noFile, err = serverOnlyStreams(cctx, js, files); err != nil {
			return err
		}
		noFileSummary = fmt.Sprintf("  no file %d", len(noFile))
	}

	if opt.DryRun {
		for _, f := range creates {
			fmt.Fprintf(out, "would create  %s\n", f.label())
		}
		for _, f := range updates {
			fmt.Fprintf(out, "would update  %s (%s)\n", f.label(), strings.Join(f.changes, ", "))
		}
		printJSNoFile(out, noFile)
		fmt.Fprintf(out, "dry run: would create %d  would update %d  unchanged %d%s\n", len(creates), len(updates), unchanged, noFileSummary)
		if opt.ExitCode && len(creates)+len(updates) > 0 {
			return errChangesPending
		}
		return nil
	}

	var created, updated, failed int
	for _, f := range creates {
		if err := f.send(cctx, api, js, org, true, errOut); err != nil {
			fmt.Fprintf(errOut, "failed   %s: %v\n", f.label(), err)
			failed++
			continue
		}
		fmt.Fprintf(out, "created  %s\n", f.label())
		created++
	}
	for _, f := range updates {
		if err := f.send(cctx, api, js, org, false, errOut); err != nil {
			fmt.Fprintf(errOut, "failed   %s: %v\n", f.label(), err)
			failed++
			continue
		}
		fmt.Fprintf(out, "updated  %s (%s)\n", f.label(), strings.Join(f.changes, ", "))
		updated++
	}
	printJSNoFile(out, noFile)
	fmt.Fprintf(out, "created %d  updated %d  unchanged %d  failed %d%s\n", created, updated, unchanged, failed, noFileSummary)
	if failed > 0 {
		return fmt.Errorf("%d operation(s) failed", failed)
	}
	return nil
}

// send creates or updates f's stream or bucket, then records the server's new
// config_hash in the file. A stream is identified by its name, so a file that
// keeps its old hash is not created twice; the next apply just refuses it as
// stale until it is pulled. That makes a failed write-back a warning.
func (f *jsFile) send(cctx context.Context, api jsAPI, js jetstream.JetStream, org string, create bool, errOut io.Writer) error {
	var after map[string]any
	domain := js.Options().Domain
	switch f.Kind {
	case kindStream:
		body, err := f.streamBody()
		if err != nil {
			return err
		}
		if create {
			after, err = api.createStream(cctx, f.Name, body)
		} else {
			after, err = api.updateStream(cctx, f.Name, body)
		}
		if err != nil {
			return err
		}
	case kindKV:
		cfg, err := f.kvConfig()
		if err != nil {
			return err
		}
		if create {
			_, err = js.CreateKeyValue(cctx, cfg)
		} else {
			_, err = js.UpdateKeyValue(cctx, cfg)
		}
		if err != nil {
			return err
		}
		if after, _, err = api.streamConfig(cctx, f.streamName()); err != nil {
			fmt.Fprintf(errOut, "warning  %s: written, but could not read it back for config_hash (%v); pull it before applying it again\n", f.label(), err)
			return nil
		}
	}
	hash, err := jsConfigHash(after)
	if err == nil {
		f.Raw[jsKeyHash] = hash
		if org != "" && f.Org == "" {
			f.Raw[jsKeyOrg] = org
		}
		if domain != "" && f.Domain == "" {
			f.Raw[jsKeyDomain] = domain
		}
		err = pb.MarshalFile(f.Path, f.Raw)
	}
	if err != nil {
		fmt.Fprintf(errOut, "warning  %s: written, but could not record its config_hash in the file (%v); pull it before applying it again\n", f.label(), err)
	}
	return nil
}

// streamBody is the request body for a stream create or update: the file's
// config with durations in nanoseconds, which is what the server takes.
func (f *jsFile) streamBody() (map[string]any, error) {
	body := deepCopyMap(f.Body)
	if err := durationsToNanos(body, jsDurationPaths[kindStream]); err != nil {
		return nil, err
	}
	return body, nil
}

// kvConfig decodes a bucket file strictly, so a misspelt key is an error
// rather than a field nats.go quietly ignores.
func (f *jsFile) kvConfig() (jetstream.KeyValueConfig, error) {
	var cfg jetstream.KeyValueConfig
	err := decodeJSConfig(f.Body, kindKV, &cfg)
	return cfg, err
}

// decodeJSConfig decodes a config map into nats.go's StreamConfig or
// KeyValueConfig through their JSON tags -- the only tags they have, so
// decoding YAML into them directly reads max_age as a field named "maxage"
// and drops it. Durations may be strings, and the keys pull adds are ignored,
// so a pulled file works anywhere a config does. Unknown keys are an error:
// the struct would drop them without a word.
func decodeJSConfig(m map[string]any, kind jsKind, out any) error {
	body := deepCopyMap(m)
	delete(body, jsKeyOrg)
	delete(body, jsKeyHash)
	if err := durationsToNanos(body, jsDurationPaths[kind]); err != nil {
		return err
	}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		what := "stream"
		if kind == kindKV {
			what = "KV bucket"
		}
		return fmt.Errorf("not a %s config: %w", what, err)
	}
	return nil
}

// readJSConfigFile reads a --config file: YAML by extension, JSON otherwise.
func readJSConfigFile(path string, kind jsKind, out any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read --config: %w", err)
	}
	var m map[string]any
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		err = yaml.Unmarshal(data, &m)
	default:
		err = json.Unmarshal(data, &m)
	}
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	if err := decodeJSConfig(m, kind, out); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// compareForm is the file normalized the way pull writes it, so a file that
// spells a default out (max_msgs: -1) or a duration differently (24h0m0s)
// does not count as a change.
func (f *jsFile) compareForm() (map[string]any, error) {
	if f.Kind == kindKV {
		cfg, err := f.kvConfig()
		if err != nil {
			return nil, err
		}
		return kvFileForm(cfg)
	}
	return normalizeJSConfig(kindStream, f.Body)
}

func serverCompareForm(kind jsKind, raw map[string]any) (map[string]any, error) {
	if kind == kindKV {
		return kvFileForm(kvConfigFromStream(raw))
	}
	return normalizeJSConfig(kindStream, raw)
}

// changedKeys lists the top-level keys whose values differ, sorted.
func changedKeys(a, b map[string]any) []string {
	var out []string
	for k, v := range a {
		if w, ok := b[k]; !ok || !reflect.DeepEqual(v, w) {
			out = append(out, k)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// ---- KV buckets ---------------------------------------------------------------

// kvConfigFromStream reads a bucket's config back out of its KV_ stream: the
// inverse of what nats.go does when it creates a bucket.
func kvConfigFromStream(raw map[string]any) jetstream.KeyValueConfig {
	var sc jetstream.StreamConfig
	if data, err := json.Marshal(raw); err == nil {
		_ = json.Unmarshal(data, &sc)
	}
	kv := jetstream.KeyValueConfig{
		Bucket:      strings.TrimPrefix(sc.Name, kvStreamPrefix),
		Description: sc.Description,
		TTL:         sc.MaxAge,
		Storage:     sc.Storage,
		Replicas:    sc.Replicas,
		Placement:   sc.Placement,
		RePublish:   sc.RePublish,
		Mirror:      sc.Mirror,
		Sources:     sc.Sources,
		Compression: sc.Compression == jetstream.S2Compression,
		Metadata:    userMetadata(sc.Metadata),
	}
	if sc.MaxMsgSize > 0 {
		kv.MaxValueSize = sc.MaxMsgSize
	}
	if sc.MaxMsgsPerSubject >= 1 && sc.MaxMsgsPerSubject <= jetstream.KeyValueMaxHistory {
		kv.History = uint8(sc.MaxMsgsPerSubject)
	}
	if sc.MaxBytes > 0 {
		kv.MaxBytes = sc.MaxBytes
	}
	if sc.AllowMsgTTL {
		kv.LimitMarkerTTL = sc.SubjectDeleteMarkerTTL
	}
	return kv
}

// kvExpectedStream is the stream nats.go builds for a bucket config, copied
// from its prepareKeyValueConfig (nats.go v1.52). Mirror and sources are taken
// as stored: they come from the server, already in the form nats.go wrote.
func kvExpectedStream(kv jetstream.KeyValueConfig) jetstream.StreamConfig {
	history := int64(1)
	if kv.History > 0 {
		history = int64(kv.History)
	}
	replicas := kv.Replicas
	if replicas == 0 {
		replicas = 1
	}
	maxBytes := kv.MaxBytes
	if maxBytes == 0 {
		maxBytes = -1
	}
	maxMsgSize := kv.MaxValueSize
	if maxMsgSize == 0 {
		maxMsgSize = -1
	}
	dup := 2 * time.Minute
	if kv.TTL > 0 && kv.TTL < dup {
		dup = kv.TTL
	}
	sc := jetstream.StreamConfig{
		Name:                   kvStreamPrefix + kv.Bucket,
		Description:            kv.Description,
		MaxMsgsPerSubject:      history,
		MaxBytes:               maxBytes,
		MaxAge:                 kv.TTL,
		MaxMsgSize:             maxMsgSize,
		Storage:                kv.Storage,
		Replicas:               replicas,
		Placement:              kv.Placement,
		AllowRollup:            true,
		DenyDelete:             true,
		Duplicates:             dup,
		MaxMsgs:                -1,
		MaxConsumers:           -1,
		AllowDirect:            true,
		RePublish:              kv.RePublish,
		Discard:                jetstream.DiscardNew,
		AllowMsgTTL:            kv.LimitMarkerTTL != 0,
		SubjectDeleteMarkerTTL: kv.LimitMarkerTTL,
		Metadata:               kv.Metadata,
		Mirror:                 kv.Mirror,
		Sources:                kv.Sources,
	}
	if kv.Compression {
		sc.Compression = jetstream.S2Compression
	}
	if kv.Mirror != nil {
		sc.MirrorDirect = true
	} else {
		sc.Subjects = []string{"$KV." + kv.Bucket + ".>"}
	}
	return sc
}

// kvUnrepresentable lists the settings of a bucket's stream that no bucket
// config would produce: made by hand with `nats stream edit`, by an older
// client with different defaults, or added by a server newer than nats.go.
// UpdateKeyValue rebuilds the whole stream from the bucket config, so it would
// reset every one of them.
func kvUnrepresentable(raw map[string]any) ([]string, error) {
	exp, err := structToMap(kvExpectedStream(kvConfigFromStream(raw)))
	if err != nil {
		return nil, err
	}
	a, err := normalizeJSConfig(kindStream, exp)
	if err != nil {
		return nil, err
	}
	b, err := normalizeJSConfig(kindStream, raw)
	if err != nil {
		return nil, err
	}
	return changedKeys(a, b), nil
}

// kvFileForm is a bucket config as a file holds it.
func kvFileForm(kv jetstream.KeyValueConfig) (map[string]any, error) {
	m, err := structToMap(kv)
	if err != nil {
		return nil, err
	}
	return normalizeJSConfig(kindKV, m)
}

func userMetadata(md map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range md {
		if !strings.HasPrefix(k, "_nats.") {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ---- normalizing --------------------------------------------------------------

// normalizeJSConfig turns a config -- the server's, or a file's -- into the
// form pull writes: numbers as int64, durations as strings, the server's own
// _nats.* metadata removed, and every value equal to what the server would
// fill in for a missing field left out. Two configs the server treats alike
// normalize to the same map, which is what makes the diff and the hash mean
// something.
func normalizeJSConfig(kind jsKind, m map[string]any) (map[string]any, error) {
	out, _ := canonicalJSON(deepCopyMap(m)).(map[string]any)
	if out == nil {
		out = map[string]any{}
	}
	if err := durationsToNanos(out, jsDurationPaths[kind]); err != nil {
		return nil, err
	}
	if md, ok := out["metadata"].(map[string]any); ok {
		for k := range md {
			if strings.HasPrefix(k, "_nats.") {
				delete(md, k)
			}
		}
	}
	for k, d := range jsDefaults[kind] {
		if reflect.DeepEqual(out[k], d) {
			delete(out, k)
		}
	}
	dropZero(out)
	for _, p := range jsDurationPaths[kind] {
		if v, ok := getPath(out, p).(int64); ok {
			setPath(out, p, formatDuration(time.Duration(v)))
		}
	}
	return out, nil
}

// jsConfigHash fingerprints a stream's config as the server reports it. It is
// apply's optimistic lock: a file records the hash it was pulled at, and a
// different hash on the server means someone has changed the stream since.
// It is taken over the normalized raw stream config, for buckets too, so a
// change to a setting a bucket file cannot show still counts.
func jsConfigHash(raw map[string]any) (string, error) {
	n, err := normalizeJSConfig(kindStream, raw)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(n) // map keys marshal sorted
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8]), nil
}

// canonicalJSON makes values decoded from JSON or YAML comparable: every
// whole number becomes an int64 (json.Number, int, uint64, float64 alike).
func canonicalJSON(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, e := range t {
			t[k] = canonicalJSON(e)
		}
		return t
	case pb.Record:
		return canonicalJSON(map[string]any(t))
	case []any:
		for i, e := range t {
			t[i] = canonicalJSON(e)
		}
		return t
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i
		}
		if f, err := t.Float64(); err == nil {
			return canonicalJSON(f)
		}
		return t.String()
	case int:
		return int64(t)
	case int32:
		return int64(t)
	case uint64:
		if t <= math.MaxInt64 {
			return int64(t)
		}
		return t
	case float64:
		if t == math.Trunc(t) && math.Abs(t) < 1<<53 {
			return int64(t)
		}
		return t
	}
	return v
}

// dropZero deletes, recursively, every map entry whose value is a zero value.
// Slice elements are kept: a subject list is not shortened.
func dropZero(m map[string]any) {
	for k, v := range m {
		switch t := v.(type) {
		case map[string]any:
			dropZero(t)
		case []any:
			for _, e := range t {
				if em, ok := e.(map[string]any); ok {
					dropZero(em)
				}
			}
		}
		if isZeroJS(m[k]) {
			delete(m, k)
		}
	}
}

func isZeroJS(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case bool:
		return !t
	case string:
		return t == ""
	case int64:
		return t == 0
	case float64:
		return t == 0
	case map[string]any:
		return len(t) == 0
	case []any:
		return len(t) == 0
	}
	return false
}

// durationsToNanos converts the duration fields at paths from Go duration
// strings ("24h") to integer nanoseconds. Numbers are taken as nanoseconds.
func durationsToNanos(m map[string]any, paths []string) error {
	for _, p := range paths {
		switch v := getPath(m, p).(type) {
		case nil:
		case string:
			d, err := time.ParseDuration(v)
			if err != nil {
				return fmt.Errorf("%s: %q is not a duration (e.g. 90s, 24h)", p, v)
			}
			setPath(m, p, int64(d))
		default:
			n, ok := canonicalJSON(v).(int64)
			if !ok {
				return fmt.Errorf("%s: %v is not a duration (e.g. 90s, 24h)", p, v)
			}
			setPath(m, p, n)
		}
	}
	return nil
}

// formatDuration writes 24h and 2m rather than Go's 24h0m0s and 2m0s.
func formatDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

func getPath(m map[string]any, path string) any {
	parts := strings.Split(path, ".")
	cur := m
	for _, p := range parts[:len(parts)-1] {
		next, ok := cur[p].(map[string]any)
		if !ok {
			return nil
		}
		cur = next
	}
	return cur[parts[len(parts)-1]]
}

// setPath sets an existing value; it does not create intermediate maps.
func setPath(m map[string]any, path string, v any) {
	parts := strings.Split(path, ".")
	cur := m
	for _, p := range parts[:len(parts)-1] {
		next, ok := cur[p].(map[string]any)
		if !ok {
			return
		}
		cur = next
	}
	cur[parts[len(parts)-1]] = v
}

// deepCopyMap copies maps and slices so normalizing never edits a file's map.
func deepCopyMap(m map[string]any) map[string]any {
	out, _ := deepCopyValue(m).(map[string]any)
	return out
}

func deepCopyValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = deepCopyValue(e)
		}
		return out
	case pb.Record:
		return deepCopyValue(map[string]any(t))
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = deepCopyValue(e)
		}
		return out
	}
	return v
}

func structToMap(v any) (map[string]any, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	out, _ := canonicalJSON(m).(map[string]any)
	return out, nil
}

// ---- raw JetStream API --------------------------------------------------------

// jsAPI speaks the stream half of the JetStream API directly, with configs as
// maps rather than nats.go's StreamConfig. Going through the struct would drop
// any field a server knows and this build's nats.go does not, and an update
// sends the whole config, so apply would quietly reset that field to its
// default. As maps, a file holds exactly what the server reported, and an
// unknown or misspelt key is the server's to refuse (it decodes strictly).
type jsAPI struct {
	nc     *nats.Conn
	prefix string
	domain string
}

func newJSAPI(js jetstream.JetStream) jsAPI {
	o := js.Options()
	prefix := jetstream.DefaultAPIPrefix
	switch {
	case o.Domain != "":
		prefix = "$JS." + o.Domain + ".API."
	case o.APIPrefix != "":
		prefix = strings.TrimSuffix(o.APIPrefix, ".") + "."
	}
	return jsAPI{nc: js.Conn(), prefix: prefix, domain: o.Domain}
}

// noResponders explains a request nothing answered. Through the hub, a leaf's
// domain has a cause that is not obvious: the hub forwards a request to a leaf
// only if the leaf's uplink credential may subscribe to its subject, so a role
// without $JS.<code>.API.> on its subscribe list drops it at the hub. Verified
// against nats-server 2.14 with the agent's nats-leaf.conf shape.
func (a jsAPI) noResponders(subj string) error {
	if a.domain == "" {
		return fmt.Errorf("JetStream is not available to this account (no responders on %s)", a.prefix+subj)
	}
	return fmt.Errorf("no JetStream answered in domain %s (no responders on %s). Either\n"+
		"  - the leaf node for %s is not connected to the hub, or\n"+
		"  - its uplink credential (the thing's NATS user) may not subscribe to $JS.%s.API.>:\n"+
		"    the hub forwards to a leaf only what that credential may subscribe to, so add\n"+
		"    $JS.%s.API.> (or $JS.*.API.>) to the subscribe list of its NATS role",
		a.domain, a.prefix+subj, a.domain, a.domain, a.domain)
}

type jsAPIError struct {
	Code        int    `json:"code"`
	ErrCode     int    `json:"err_code"`
	Description string `json:"description"`
}

func (e *jsAPIError) Error() string { return e.Description }

func (a jsAPI) call(cctx context.Context, subj string, body any) (map[string]any, error) {
	var data []byte
	if body != nil {
		var err error
		if data, err = json.Marshal(body); err != nil {
			return nil, err
		}
	}
	msg, err := a.nc.RequestWithContext(cctx, a.prefix+subj, data)
	if err != nil {
		if errors.Is(err, nats.ErrNoResponders) {
			return nil, a.noResponders(subj)
		}
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(msg.Data))
	dec.UseNumber()
	var resp struct {
		Error  *jsAPIError    `json:"error"`
		Config map[string]any `json:"config"`
	}
	if err := dec.Decode(&resp); err != nil {
		return nil, fmt.Errorf("decode %s response: %w", subj, err)
	}
	if resp.Error != nil {
		return nil, resp.Error
	}
	if resp.Config == nil {
		return nil, fmt.Errorf("%s: response carried no config", subj)
	}
	cfg, _ := canonicalJSON(resp.Config).(map[string]any)
	return cfg, nil
}

// streamConfig returns the server's config for a stream, and false if there is
// no such stream.
func (a jsAPI) streamConfig(cctx context.Context, name string) (map[string]any, bool, error) {
	cfg, err := a.call(cctx, "STREAM.INFO."+name, nil)
	var apiErr *jsAPIError
	if errors.As(err, &apiErr) && apiErr.ErrCode == jsErrStreamNotFound {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return cfg, true, nil
}

func (a jsAPI) createStream(cctx context.Context, name string, cfg map[string]any) (map[string]any, error) {
	return a.call(cctx, "STREAM.CREATE."+name, cfg)
}

func (a jsAPI) updateStream(cctx context.Context, name string, cfg map[string]any) (map[string]any, error) {
	return a.call(cctx, "STREAM.UPDATE."+name, cfg)
}

// jsDomainLabel names a domain for a message; "" is the hub.
func jsDomainLabel(domain string) string {
	if domain == "" {
		return "the hub"
	}
	return "domain " + domain
}

func jsOrgLabel(c ctx.Context, id string) string {
	code, name := lookupOrg(c, id)
	return formatOrg(id, code, name)
}

// serverOnlyStreams lists the streams and buckets on the server that no file
// declares, as "streams/<name>" or "kv/<bucket>". apply never deletes, so
// without this a stream made with `nats stream add` is invisible to it.
// Object stores are left out, as pull leaves them out.
func serverOnlyStreams(cctx context.Context, js jetstream.JetStream, files []*jsFile) ([]string, error) {
	have := map[string]bool{}
	for _, f := range files {
		have[f.streamName()] = true
	}
	lister := js.StreamNames(cctx)
	var out []string
	for name := range lister.Name() {
		switch {
		case have[name], strings.HasPrefix(name, objStreamPrefix):
		case strings.HasPrefix(name, kvStreamPrefix):
			out = append(out, jsKVDir+"/"+strings.TrimPrefix(name, kvStreamPrefix))
		default:
			out = append(out, jsStreamsDir+"/"+name)
		}
	}
	if err := lister.Err(); err != nil {
		return nil, fmt.Errorf("list streams: %w", err)
	}
	sort.Strings(out)
	return out, nil
}

// printJSNoFile reports what is on the server with no file. It is not a
// change apply would make, so it does not count toward --exit-code.
func printJSNoFile(out io.Writer, lines []string) {
	for _, l := range lines {
		fmt.Fprintf(out, "no file  %s (on the server only; apply leaves it alone)\n", l)
	}
}

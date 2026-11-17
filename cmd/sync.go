package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/stone-age-io/stone-cli/internal/ctx"
	"github.com/stone-age-io/stone-cli/internal/pb"
	"gopkg.in/yaml.v3"
)

const batchSize = 50

var pullCmd = &cobra.Command{
	Use:   "pull [<collection>...]",
	Short: "Pull records into the workspace as YAML files",
	Long: `Pull writes one YAML file per record into <workspace>/<collection>/,
named by the record's natural key (code, name, hostname, ...; falls back to id).
Org-scoped collections, and memberships, are filtered to the current
organization. organizations, nats_accounts and nebula_ca are pulled only for a
Platform Operator, because only an operator can apply them back.
Pass collection names to limit; otherwise all known collections are pulled.`,
	RunE: runPull,
}

var applyCmd = &cobra.Command{
	Use:   "apply [<path>...]",
	Short: "Apply YAML/JSON files in the workspace to the server",
	Long: `Apply walks the given paths (or the workspace root) for .yaml/.yml/.json
files, infers each file's collection from its parent directory, and creates
or updates records via the PocketBase batch API.

Records with an "id" field are compared with the server and updated with only
the fields that differ; unchanged records are not sent. Records without an id
are created and the returned id is written back into the file. Apply is safe
to re-run. --dry-run prints what would be sent without sending it.

Files in a directory that is not a collection are skipped with a note. A
record whose organization is not the current one stops the whole apply before
anything is sent, unless --any-org is given.`,
	RunE: runApply,
}

func init() {
	pullCmd.Flags().String("workspace", "", "workspace directory (default: context's workspace, then cwd)")
	pullCmd.Flags().Bool("set-workspace", false, "save --workspace into the context after a successful pull")
	applyCmd.Flags().String("workspace", "", "workspace directory (default: context's workspace, then cwd)")
	applyCmd.Flags().Bool("dry-run", false, "print what would be created and updated (and which fields) without sending it")
	applyCmd.Flags().Bool("any-org", false, "apply records that belong to organizations other than the current one")
	rootCmd.AddCommand(pullCmd, applyCmd)
}

func resolveWorkspace(cmd *cobra.Command, c ctx.Context) (string, error) {
	w, _ := cmd.Flags().GetString("workspace")
	if w == "" {
		w = c.Workspace
	}
	if w == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		w = cwd
	}
	abs, err := filepath.Abs(w)
	if err != nil {
		return "", err
	}
	return abs, nil
}

func runPull(cmd *cobra.Command, args []string) error {
	c, err := ctx.Active(flagContext)
	if err != nil {
		return err
	}
	if c.Auth.Token == "" {
		return errors.New("not logged in. run: stone auth login")
	}
	ws, err := resolveWorkspace(cmd, c)
	if err != nil {
		return err
	}

	wanted := map[string]bool{}
	for _, a := range args {
		wanted[a] = true
	}

	client := newPBClient(c)
	operator, err := isOperator(client, c)
	if err != nil {
		return err
	}
	totals := 0
	for _, spec := range entitySpecs {
		if len(wanted) > 0 && !wanted[spec.Collection] && !wanted[spec.Name] && !wanted[spec.Plural] {
			continue
		}
		// A read-only entity has nothing to reconcile: every record it could
		// write already has an id, so apply would PATCH it, and a collection
		// with no update rule answers 404. Named explicitly, say so; swept up
		// in a full pull, stay quiet.
		if !spec.syncable() {
			if len(wanted) > 0 {
				fmt.Fprintf(os.Stderr, "skip %s (read-only; apply has nothing to send)\n", spec.Collection)
			}
			continue
		}
		if operatorOnlyUpdate[spec.Collection] && !operator {
			fmt.Fprintf(os.Stderr, "skip %s (only a Platform Operator can update it, so apply could not write it back)\n", spec.Collection)
			continue
		}
		if pullScopedToOrg(spec) && c.CurrentOrganization == "" {
			fmt.Fprintf(os.Stderr, "skip %s (org-scoped; no current organization set)\n", spec.Collection)
			continue
		}
		// Sort by id so filename collision suffixes land on the same
		// records across pulls (stable workspace diffs).
		opts := pb.ListOptions{Sort: "id"}
		if pullScopedToOrg(spec) {
			opts.Filter = fmt.Sprintf(`organization="%s"`, c.CurrentOrganization)
		}
		items, err := client.ListAll(spec.Collection, opts)
		if err != nil {
			return fmt.Errorf("%s: %w", spec.Collection, err)
		}
		dir := filepath.Join(ws, spec.Collection)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		count := 0
		omitted := false
		used := map[string]bool{}
		for _, r := range items {
			pb.Strip(r)
			if stripWorkspaceFields(spec.Collection, r) {
				omitted = true
			}
			base := recordFilename(spec, r)
			if used[base] {
				if id, _ := r["id"].(string); id != "" {
					base = base + "-" + id
				}
			}
			used[base] = true
			fname := base + ".yaml"
			path := filepath.Join(dir, fname)
			if err := pb.MarshalFile(path, r); err != nil {
				return fmt.Errorf("write %s: %w", path, err)
			}
			count++
		}
		fmt.Printf("%-25s %4d → %s\n", spec.Collection, count, dir)
		if omitted {
			fmt.Printf("%-25s      (omitted %s — credentials and server-generated state are not written to the workspace)\n",
				"", strings.Join(workspaceOmit[spec.Collection], ", "))
		}
		totals += count
	}

	saveWS, _ := cmd.Flags().GetBool("set-workspace")
	if saveWS {
		c.Workspace = ws
		if err := ctx.Save(c); err != nil {
			return err
		}
		fmt.Printf("saved workspace=%s to context %q\n", ws, c.Name)
	}
	fmt.Printf("pulled %d records\n", totals)
	return nil
}

// operatorOnlyUpdate names the collections whose updateRule admits only a
// Platform Operator. An owner or admin can READ them, so pull used to write them,
// and every record in them then failed on apply: each file has an id, so apply
// PATCHes it, and the rule answers 404. A workspace is a list of things the
// caller can reconcile, so pull leaves these out for anyone else.
// cmd/sync_test.go checks this list against the vendored schema's rules.
var operatorOnlyUpdate = map[string]bool{
	"organizations": true,
	"nats_accounts": true,
	"nebula_ca":     true,
}

// pullScopedToOrg reports whether pull filters a collection to the current
// organization. memberships is not OrgScoped -- its organization is a flag,
// not injected -- but its listRule returns the caller's own memberships in
// EVERY organization (the switcher needs them), and those in other
// organizations are not the caller's to update from this one.
func pullScopedToOrg(spec EntitySpec) bool {
	return spec.OrgScoped || spec.Collection == "memberships"
}

// isOperator reports whether the authenticated caller is a Platform Operator:
// `is_operator` on its own users record. A superuser passes every rule, so it
// counts as one; any other auth collection (a thing, a host) does not.
func isOperator(client *pb.Client, c ctx.Context) (bool, error) {
	if c.Auth.Collection == "_superusers" {
		return true, nil
	}
	if c.Auth.Collection != "users" {
		return false, nil
	}
	id := c.Auth.UserID
	if id == "" {
		var err error
		if id, err = pb.DecodeJWTUserID(c.Auth.Token); err != nil {
			return false, fmt.Errorf("cannot determine user id: %w", err)
		}
	}
	r, err := client.Get("users", id, pb.GetOptions{Fields: "is_operator"})
	if err != nil {
		return false, fmt.Errorf("read own users record: %w", err)
	}
	op, _ := r["is_operator"].(bool)
	return op, nil
}

// workspaceOmit names, per collection, fields that `pull` must not write into a
// workspace file.
//
// PocketBase already withholds the fields marked hidden in the schema -- every
// private_key and seed -- so those never reach this CLI at all. The ones below
// are the material the API DOES return to an owner or admin and that has no
// business in a directory the docs tell you to commit to git:
//
//   - nats_users.creds_file IS the credential. It is the exact payload
//     natsx.SyncContextForOrg writes to disk under 0600 so nats-cli can connect
//     with it -- a user JWT and an nkey seed. Pulling an organization wrote one
//     per NATS identity into world-readable YAML.
//   - nebula_hosts.config_yaml carries the host's Nebula private key INLINE.
//     pb-nebula says so itself (internal/types/types.go): the standalone
//     private_key column can be encrypted at rest and is hidden from the API,
//     and the rendered config that contains the same key is neither.
//   - invites.token is a bearer credential to join the organization. Anyone who
//     can read the file can redeem it as the invited address.
//
// The rest are server-generated state and action triggers. Neither belongs in a
// declarative file: apply sends back every key in it, so a pulled certificate or
// JWT is a stale value racing whatever the server has rotated to since, and a
// pulled trigger re-asserts an action that already happened.
//
// locations.path (platform ADR 0004) is server-generated in the plainest sense:
// the platform's hook recomputes it on every save and overwrites whatever a
// client sends, and moving an ancestor rewrites it for the whole subtree. A
// pulled path is therefore harmless to apply and stale the first time anything
// above it moves -- noise in a file people diff.
//
// Every `active` flag the platform acts on is omitted for the trigger reason,
// not the secret one. Apply sends back every key in a file, so a pulled `true`
// re-applied after an admin set `false` silently undoes the decision:
//   - nats_users.active: pb-nats acts on its EDGE (true->false revokes the key
//     with a permanent cutoff, false->true re-mints).
//   - things.active: false->true reactivates a decommissioned device. The
//     platform re-enables its NATS user and Nebula host and refreshes its
//     tokens, so a stale file hands a retired device working credentials.
//   - nebula_hosts.active: false->true re-issues a revoked host's certificate.
//   - nats_accounts.active and organizations.active: reactivating lifts a
//     suspension and puts the account back on the NATS server.
//
// TestEdgeTriggeredFlagsAreNeverPulled and TestRevocationStateIsNeverPulled pin
// this list, so deleting a line fails.
//
// This is a pull-side filter only. A hand-written file that sets one of these
// still applies -- `revoke: true` or `active: false` is a legitimate thing to
// write by hand -- so nothing here removes a capability. It only stops the CLI
// from putting secrets on disk unasked.
var workspaceOmit = map[string][]string{
	"nats_users":    {"creds_file", "jwt", "public_key", "active", "regenerate", "revoke"},
	"nats_accounts": {"jwt", "public_key", "signing_public_key", "signing_keys", "active", "revocations", "rotate_keys", "add_signing_key", "remove_signing_key"},
	"nebula_hosts": {"config_yaml", "certificate", "ca_certificate", "expires_at", "renew", "active",
		// pb-nebula (v0.6.0+) keeps the certificates a host no longer holds here
		// and builds every peer's pki.blocklist from them. A stale copy applied
		// back would drop a later revocation, and so lift it.
		"revoked_certificates", "retired_certificates"},
	"nebula_ca":     {"certificate", "expires_at", "rotate", "next_certificate", "previous_certificate", "rotated_at"},
	"invites":       {"token", "resend_invite"},
	"locations":     {"path"},
	"things":        {"active"},
	"organizations": {"active"},
	// pb-nats writes a cutoff here when a key leaves allowed_importers. A stale
	// copy applied back would drop a later cutoff, and so lift a revocation.
	"nats_account_exports": {"revocations"},
}

// stripWorkspaceFields removes the fields above in place, and reports whether it
// removed anything -- pull says so per collection, because a user who expected a
// full record dump deserves to know what is not in the file rather than discover
// it when a restore comes up short.
func stripWorkspaceFields(collection string, r pb.Record) bool {
	var removed bool
	for _, f := range workspaceOmit[collection] {
		if _, ok := r[f]; ok {
			delete(r, f)
			removed = true
		}
	}
	return removed
}

func recordFilename(spec EntitySpec, r pb.Record) string {
	// No collection the CLI pulls has a composite natural key any more. There
	// was a namespace__name__version branch here for message_schemas, whose
	// LookupKey ("name") repeated across versions; that collection is gone, and
	// a speculative branch for the next one would be untested code guessing at
	// field names. Filenames are cosmetic anyway -- apply keys on the id inside
	// each file -- so the collision suffix is a sufficient backstop.
	if spec.LookupKey != "" {
		if k, _ := r[spec.LookupKey].(string); k != "" {
			return sanitizeFilename(k)
		}
	}
	if name, ok := r["name"].(string); ok && name != "" {
		return sanitizeFilename(name)
	}
	id, _ := r["id"].(string)
	return id
}

// sanitizeFilename keeps filenames cross-platform safe.
func sanitizeFilename(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		case r == ' ', r == '/', r == '\\':
			b.WriteRune('-')
		default:
			// drop other chars
		}
	}
	out := b.String()
	if out == "" {
		out = "record"
	}
	return out
}

// applyFile holds the parsed state of one workspace file.
type applyFile struct {
	Path       string
	Ext        string
	Collection string
	Record     pb.Record
	ID         string    // empty for create
	Patch      pb.Record // update only: the keys that differ from the server
}

// name is how apply refers to a file in its output.
func (f applyFile) name() string {
	return f.Collection + "/" + filepath.Base(f.Path)
}

func runApply(cmd *cobra.Command, args []string) error {
	c, err := ctx.Active(flagContext)
	if err != nil {
		return err
	}
	if c.Auth.Token == "" {
		return errors.New("not logged in. run: stone auth login")
	}
	ws, err := resolveWorkspace(cmd, c)
	if err != nil {
		return err
	}
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	anyOrg, _ := cmd.Flags().GetBool("any-org")

	// Only the collections pull writes. A directory named after a read-only
	// entity is left where it is rather than sent: apply would PATCH records
	// whose collection has no update rule and count every one of them a failure.
	specs := map[string]EntitySpec{}
	for _, s := range entitySpecs {
		if s.syncable() {
			specs[s.Collection] = s
		}
	}

	roots := args
	if len(roots) == 0 {
		roots = []string{ws}
	}

	var files []applyFile
	for _, root := range roots {
		abs, err := filepath.Abs(root)
		if err != nil {
			return err
		}
		err = filepath.WalkDir(abs, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if strings.HasPrefix(d.Name(), ".") && path != abs {
					return filepath.SkipDir
				}
				return nil
			}
			ext := strings.ToLower(filepath.Ext(path))
			if ext != ".yaml" && ext != ".yml" && ext != ".json" {
				return nil
			}
			coll := filepath.Base(filepath.Dir(path))
			if _, ok := specs[coll]; !ok {
				// Say so: a typo'd directory (thing/ for things/) or a nested
				// one (things/old/) otherwise drops its files without a word.
				fmt.Fprintf(os.Stderr, "skip     %s (%q is not a collection apply writes)\n", path, coll)
				return nil
			}
			rec, err := pb.UnmarshalFile(path)
			if err != nil {
				return err
			}
			pb.Strip(rec)
			id, _ := rec["id"].(string)
			files = append(files, applyFile{Path: path, Ext: ext, Collection: coll, Record: rec, ID: id})
			return nil
		})
		if err != nil {
			return err
		}
	}

	if len(files) == 0 {
		return fmt.Errorf("no .yaml/.yml/.json files found under: %v", roots)
	}

	if !anyOrg {
		if err := checkOrganizations(files, specs, c.CurrentOrganization); err != nil {
			return err
		}
	}

	creates := map[string][]int{}
	updates := map[string][]int{}
	for i, f := range files {
		if f.ID == "" {
			// Auto-inject organization for org-scoped collections when missing on create.
			if specs[f.Collection].OrgScoped {
				if _, ok := f.Record["organization"]; !ok {
					if c.CurrentOrganization == "" {
						return fmt.Errorf("%s: collection is org-scoped but no current organization set", f.Path)
					}
					files[i].Record["organization"] = c.CurrentOrganization
				}
			}
			creates[f.Collection] = append(creates[f.Collection], i)
		} else {
			updates[f.Collection] = append(updates[f.Collection], i)
		}
	}

	client := newPBClient(c)

	// Compare every update with the server first, the way edit does, so a
	// record is PATCHed only with the keys that differ and an unchanged one is
	// not PATCHed at all. A blind PATCH still bumps `updated` and fires the
	// platform's hooks on every record in the workspace.
	unchanged := 0
	for _, s := range entitySpecs {
		idxs := updates[s.Collection]
		if len(idxs) == 0 {
			continue
		}
		changed, err := diffUpdates(client, s.Collection, files, idxs)
		if err != nil {
			return err
		}
		unchanged += len(idxs) - len(changed)
		updates[s.Collection] = changed
	}

	if dryRun {
		var nc, nu int
		for _, s := range entitySpecs {
			for _, i := range creates[s.Collection] {
				fmt.Printf("would create  %s\n", files[i].name())
				nc++
			}
		}
		for _, s := range entitySpecs {
			for _, i := range updates[s.Collection] {
				fmt.Printf("would update  %s (%s)\n", files[i].name(), strings.Join(sortedKeys(files[i].Patch), ", "))
				nu++
			}
		}
		fmt.Printf("dry run: would create %d  would update %d  unchanged %d\n", nc, nu, unchanged)
		return nil
	}

	// entitySpecs order, not map order, so the output is the same on every
	// run and can be diffed.
	var created, updated, failed int
	for _, s := range entitySpecs {
		c2, f2 := applyCreates(client, s.Collection, files, creates[s.Collection])
		created += c2
		failed += f2
	}
	for _, s := range entitySpecs {
		u2, f2 := applyUpdates(client, s.Collection, files, updates[s.Collection])
		updated += u2
		failed += f2
	}
	fmt.Printf("created %d  updated %d  unchanged %d  failed %d\n", created, updated, unchanged, failed)
	if failed > 0 {
		return fmt.Errorf("%d operation(s) failed", failed)
	}
	return nil
}

// checkOrganizations refuses a workspace holding records of another
// organization. pull does not clear a workspace, so pulling a second
// organization into the same directory leaves the first one's files beside it,
// and apply would PATCH both: ids are global, and an admin of both may write
// either. --any-org is for the operator who means it.
func checkOrganizations(files []applyFile, specs map[string]EntitySpec, current string) error {
	if current == "" {
		return nil
	}
	var bad []string
	for _, f := range files {
		if !pullScopedToOrg(specs[f.Collection]) {
			continue
		}
		if org, _ := f.Record["organization"].(string); org != "" && org != current {
			bad = append(bad, fmt.Sprintf("  %s (organization %s)", f.Path, org))
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf("%d file(s) belong to an organization other than the current one (%s); nothing was sent.\n%s\nswitch organization, remove them, or pass --any-org",
		len(bad), current, strings.Join(bad, "\n"))
}

// diffUpdates lists the collection once and sets each file's Patch to the keys
// that differ from the server, returning the indexes that still have something
// to send. Both sides go through the same YAML round trip editPatch's caller
// uses, so a JSON float and a YAML int of the same number compare equal.
//
// A record the listing does not contain (deleted since the pull, or not
// visible) keeps its whole body, so the PATCH reports whatever the server
// says about it. This does not detect a server that changed since the pull:
// a stale value in a file differs from the server and is sent like any edit.
func diffUpdates(client *pb.Client, coll string, files []applyFile, idxs []int) ([]int, error) {
	items, err := client.ListAll(coll, pb.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", coll, err)
	}
	server := make(map[string]pb.Record, len(items))
	for _, r := range items {
		if id, _ := r["id"].(string); id != "" {
			server[id] = r
		}
	}
	var changed []int
	for _, i := range idxs {
		f := &files[i]
		local, err := yamlRoundTrip(f.Record)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.Path, err)
		}
		cur, ok := server[f.ID]
		if !ok {
			f.Patch = editPatch(pb.Record{}, local)
			changed = append(changed, i)
			continue
		}
		remote, err := yamlRoundTrip(cur)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.Path, err)
		}
		if f.Patch = editPatch(remote, local); len(f.Patch) > 0 {
			changed = append(changed, i)
		}
	}
	return changed, nil
}

func yamlRoundTrip(r pb.Record) (pb.Record, error) {
	data, err := yaml.Marshal(r)
	if err != nil {
		return nil, err
	}
	out := pb.Record{}
	if err := yaml.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func sortedKeys(r pb.Record) []string {
	keys := make([]string, 0, len(r))
	for k := range r {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// recordCreated writes the server-assigned id back into the file. If that
// fails, the record exists but the file does not say so, and the next apply
// would create it a second time -- so it counts as a failure and says what to
// do about it.
func recordCreated(f *applyFile, id string) bool {
	if id == "" {
		fmt.Fprintf(os.Stderr, "failed   %s: created, but the server returned no id; find it with `ls` and add \"id: <id>\" to the file, or the next apply creates it again\n", f.name())
		return false
	}
	f.Record["id"] = id
	if err := pb.MarshalFile(f.Path, f.Record); err != nil {
		fmt.Fprintf(os.Stderr, "failed   %s: created as %s, but could not write the id back (%v); add \"id: %s\" to the file, or the next apply creates it again\n", f.name(), id, err, id)
		return false
	}
	fmt.Printf("created  %s -> %s\n", f.name(), id)
	return true
}

// batchRejected reports whether PocketBase itself answered a batch with an
// error. It runs a batch in one transaction, so a refusal (including batch
// requests being turned off) means nothing was written and the records can be
// re-sent one at a time. Anything else -- a timeout, a dropped connection, a
// proxy's 502 page, a 200 that would not decode -- leaves it unknown whether
// the batch committed.
func batchRejected(err error) bool {
	var pe *pb.PBError
	return errors.As(err, &pe)
}

func applyCreates(client *pb.Client, coll string, files []applyFile, idxs []int) (int, int) {
	var ok, failed int
	for start := 0; start < len(idxs); start += batchSize {
		end := min(start+batchSize, len(idxs))
		chunk := idxs[start:end]
		ops := make([]pb.BatchOp, 0, len(chunk))
		for _, i := range chunk {
			ops = append(ops, pb.BatchOp{
				Method: "POST",
				URL:    "/api/collections/" + coll + "/records",
				Body:   stripIDForBatch(files[i].Record),
			})
		}
		items, err := client.Batch(ops)
		if err != nil && !batchRejected(err) {
			// Re-sending would create the chunk twice if it did commit.
			fmt.Fprintf(os.Stderr, "failed   batch of %d %s creates: %v\n", len(chunk), coll, err)
			fmt.Fprintf(os.Stderr, "         the server may have created them anyway; check `stone %s ls` before re-running apply:\n", coll)
			for _, i := range chunk {
				fmt.Fprintf(os.Stderr, "           %s\n", files[i].Path)
			}
			failed += len(chunk)
			continue
		}
		if err != nil || hasBatchError(items) {
			// PocketBase refused the batch and rolled it back, so nothing was
			// written: re-send one at a time to name the offender.
			o, f := perOpCreate(client, coll, files, chunk)
			ok += o
			failed += f
			continue
		}
		for j, item := range items {
			var rec pb.Record
			_ = json.Unmarshal(item.Body, &rec)
			id, _ := rec["id"].(string)
			if recordCreated(&files[chunk[j]], id) {
				ok++
			} else {
				failed++
			}
		}
	}
	return ok, failed
}

func applyUpdates(client *pb.Client, coll string, files []applyFile, idxs []int) (int, int) {
	var ok, failed int
	for start := 0; start < len(idxs); start += batchSize {
		end := min(start+batchSize, len(idxs))
		chunk := idxs[start:end]
		ops := make([]pb.BatchOp, 0, len(chunk))
		for _, i := range chunk {
			ops = append(ops, pb.BatchOp{
				Method: "PATCH",
				URL:    "/api/collections/" + coll + "/records/" + files[i].ID,
				Body:   files[i].Patch,
			})
		}
		items, err := client.Batch(ops)
		if err != nil || hasBatchError(items) {
			// Unlike a create, re-sending a PATCH that may already have
			// committed is harmless, so any error falls back.
			o, f := perOpUpdate(client, coll, files, chunk)
			ok += o
			failed += f
			continue
		}
		for _, i := range chunk {
			fmt.Printf("updated  %s (%s)\n", files[i].name(), strings.Join(sortedKeys(files[i].Patch), ", "))
			ok++
		}
	}
	return ok, failed
}

func perOpCreate(client *pb.Client, coll string, files []applyFile, idxs []int) (int, int) {
	var ok, failed int
	for _, i := range idxs {
		r, err := client.Create(coll, pb.Record(stripIDForBatch(files[i].Record)))
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed   %s: %v\n", files[i].name(), err)
			failed++
			continue
		}
		id, _ := r["id"].(string)
		if recordCreated(&files[i], id) {
			ok++
		} else {
			failed++
		}
	}
	return ok, failed
}

func perOpUpdate(client *pb.Client, coll string, files []applyFile, idxs []int) (int, int) {
	var ok, failed int
	for _, i := range idxs {
		if _, err := client.Update(coll, files[i].ID, files[i].Patch); err != nil {
			fmt.Fprintf(os.Stderr, "failed   %s: %v\n", files[i].name(), err)
			failed++
			continue
		}
		fmt.Printf("updated  %s (%s)\n", files[i].name(), strings.Join(sortedKeys(files[i].Patch), ", "))
		ok++
	}
	return ok, failed
}

func stripIDForBatch(r pb.Record) map[string]any {
	out := make(map[string]any, len(r))
	for k, v := range r {
		if k == "id" {
			continue
		}
		out[k] = v
	}
	return out
}

func hasBatchError(items []pb.BatchResponseItem) bool {
	for _, it := range items {
		if it.Status < 200 || it.Status >= 300 {
			return true
		}
	}
	return false
}

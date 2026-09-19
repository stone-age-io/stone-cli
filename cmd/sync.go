package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/stone-age-io/stone-cli/internal/ctx"
	"github.com/stone-age-io/stone-cli/internal/pb"
)

const batchSize = 50

var pullCmd = &cobra.Command{
	Use:   "pull [<collection>...]",
	Short: "Pull records into the workspace as YAML files",
	Long: `Pull writes one YAML file per record into <workspace>/<collection>/,
named by the record's natural key (code, name, hostname, ...; falls back to id).
Org-scoped collections are filtered to the current organization.
Pass collection names to limit; otherwise all known collections are pulled.`,
	RunE: runPull,
}

var applyCmd = &cobra.Command{
	Use:   "apply [<path>...]",
	Short: "Apply YAML/JSON files in the workspace to the server",
	Long: `Apply walks the given paths (or the workspace root) for .yaml/.yml/.json
files, infers each file's collection from its parent directory, and creates
or updates records via the PocketBase batch API.

Records with an "id" field are updated; records without are created and the
returned id is written back into the file. Apply is safe to re-run.`,
	RunE: runApply,
}

func init() {
	pullCmd.Flags().String("workspace", "", "workspace directory (default: context's workspace, then cwd)")
	pullCmd.Flags().Bool("set-workspace", false, "save --workspace into the context after a successful pull")
	applyCmd.Flags().String("workspace", "", "workspace directory (default: context's workspace, then cwd)")
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
		if spec.OrgScoped && c.CurrentOrganization == "" {
			fmt.Fprintf(os.Stderr, "skip %s (org-scoped; no current organization set)\n", spec.Collection)
			continue
		}
		// Sort by id so filename collision suffixes land on the same
		// records across pulls (stable workspace diffs).
		opts := pb.ListOptions{Sort: "id"}
		if spec.OrgScoped {
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
// This is a pull-side filter only. A hand-written file that sets one of these
// still applies -- `revoke: true` is a legitimate thing to write by hand -- so
// nothing here removes a capability. It only stops the CLI from putting secrets
// on disk unasked.
var workspaceOmit = map[string][]string{
	"nats_users":    {"creds_file", "jwt", "public_key", "active", "regenerate", "revoke"},
	"nats_accounts": {"jwt", "public_key", "signing_public_key", "signing_keys", "active", "revocations", "rotate_keys", "add_signing_key", "remove_signing_key"},
	"nebula_hosts":  {"config_yaml", "certificate", "ca_certificate", "expires_at", "renew"},
	"nebula_ca":     {"certificate", "expires_at", "rotate", "next_certificate", "previous_certificate", "rotated_at"},
	"invites":       {"token", "resend_invite"},
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
	ID         string // empty for create
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

	// Only the collections pull writes. A directory named after a read-only
	// entity is left where it is rather than sent: apply would PATCH records
	// whose collection has no update rule and count every one of them a failure.
	collections := map[string]bool{}
	for _, s := range entitySpecs {
		if s.syncable() {
			collections[s.Collection] = true
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
			if !collections[coll] {
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

	// Auto-inject organization for org-scoped collections when missing on create.
	orgScoped := map[string]bool{}
	for _, s := range entitySpecs {
		orgScoped[s.Collection] = s.OrgScoped
	}

	creates := map[string][]int{}
	updates := map[string][]int{}
	for i, f := range files {
		if f.ID == "" {
			if orgScoped[f.Collection] {
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
	var created, updated, failed int

	for coll, idxs := range creates {
		c2, f2, err := applyCreates(client, coll, files, idxs)
		if err != nil {
			return err
		}
		created += c2
		failed += f2
	}
	for coll, idxs := range updates {
		u2, f2, err := applyUpdates(client, coll, files, idxs)
		if err != nil {
			return err
		}
		updated += u2
		failed += f2
	}
	fmt.Printf("created %d  updated %d  failed %d\n", created, updated, failed)
	if failed > 0 {
		return fmt.Errorf("%d operation(s) failed", failed)
	}
	return nil
}

func applyCreates(client *pb.Client, coll string, files []applyFile, idxs []int) (int, int, error) {
	var ok, failed int
	for start := 0; start < len(idxs); start += batchSize {
		end := start + batchSize
		if end > len(idxs) {
			end = len(idxs)
		}
		chunk := idxs[start:end]
		ops := make([]pb.BatchOp, 0, len(chunk))
		for _, i := range chunk {
			body := stripIDForBatch(files[i].Record)
			ops = append(ops, pb.BatchOp{
				Method: "POST",
				URL:    "/api/collections/" + coll + "/records",
				Body:   body,
			})
		}
		items, err := client.Batch(ops)
		if err != nil || hasBatchError(items) {
			// Fall back to per-op to identify offenders.
			o, f, ferr := perOpCreate(client, coll, files, chunk)
			ok += o
			failed += f
			if ferr != nil {
				return ok, failed, ferr
			}
			continue
		}
		for j, item := range items {
			var rec pb.Record
			if err := json.Unmarshal(item.Body, &rec); err == nil {
				if id, _ := rec["id"].(string); id != "" {
					files[chunk[j]].Record["id"] = id
					_ = pb.MarshalFile(files[chunk[j]].Path, files[chunk[j]].Record)
					fmt.Printf("created  %s/%s -> %s\n", coll, filepath.Base(files[chunk[j]].Path), id)
				}
			}
			ok++
		}
	}
	return ok, failed, nil
}

func applyUpdates(client *pb.Client, coll string, files []applyFile, idxs []int) (int, int, error) {
	var ok, failed int
	for start := 0; start < len(idxs); start += batchSize {
		end := start + batchSize
		if end > len(idxs) {
			end = len(idxs)
		}
		chunk := idxs[start:end]
		ops := make([]pb.BatchOp, 0, len(chunk))
		for _, i := range chunk {
			body := stripIDForBatch(files[i].Record)
			ops = append(ops, pb.BatchOp{
				Method: "PATCH",
				URL:    "/api/collections/" + coll + "/records/" + files[i].ID,
				Body:   body,
			})
		}
		items, err := client.Batch(ops)
		if err != nil || hasBatchError(items) {
			o, f, ferr := perOpUpdate(client, coll, files, chunk)
			ok += o
			failed += f
			if ferr != nil {
				return ok, failed, ferr
			}
			continue
		}
		for _, i := range chunk {
			fmt.Printf("updated  %s/%s\n", coll, filepath.Base(files[i].Path))
			ok++
		}
	}
	return ok, failed, nil
}

func perOpCreate(client *pb.Client, coll string, files []applyFile, idxs []int) (int, int, error) {
	var ok, failed int
	for _, i := range idxs {
		body := stripIDForBatch(files[i].Record)
		r, err := client.Create(coll, pb.Record(body))
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed   %s/%s: %v\n", coll, filepath.Base(files[i].Path), err)
			failed++
			continue
		}
		if id, _ := r["id"].(string); id != "" {
			files[i].Record["id"] = id
			_ = pb.MarshalFile(files[i].Path, files[i].Record)
			fmt.Printf("created  %s/%s -> %s\n", coll, filepath.Base(files[i].Path), id)
		}
		ok++
	}
	return ok, failed, nil
}

func perOpUpdate(client *pb.Client, coll string, files []applyFile, idxs []int) (int, int, error) {
	var ok, failed int
	for _, i := range idxs {
		body := stripIDForBatch(files[i].Record)
		if _, err := client.Update(coll, files[i].ID, pb.Record(body)); err != nil {
			fmt.Fprintf(os.Stderr, "failed   %s/%s: %v\n", coll, filepath.Base(files[i].Path), err)
			failed++
			continue
		}
		fmt.Printf("updated  %s/%s\n", coll, filepath.Base(files[i].Path))
		ok++
	}
	return ok, failed, nil
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

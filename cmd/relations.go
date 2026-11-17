package cmd

import (
	"fmt"
	"strings"

	"github.com/stone-age-io/stone-cli/internal/pb"
)

// Relation columns print a natural key rather than a PocketBase id.
//
// `thing ls` used to render `type` and `location` as 15-char ids, which is a
// column of noise: nobody recognises pbc247972991-shaped strings, and seven of
// the entity specs have at least one such column. PocketBase can answer this
// itself -- `expand` returns the related record inline, in the same query, at no
// extra round trip -- so the CLI asks for it and prints the key a human typed to
// create the thing in the first place.
//
// WHY THIS IS HUMAN-OUTPUT ONLY. `-o json` and `-o yaml` are the scripting
// surface and stay byte-for-byte what the server sent, ids and all. The expand
// parameter is not even added to those requests. `--fields id -o json` remains
// the documented way to discover an id, and a pipeline that parses `type` keeps
// getting a relation id.
//
// HOW THE LABEL IS CHOSEN. An expanded record carries its own `collectionName`,
// so the CLI looks the spec up by that and uses its LookupKey -- the same key
// `get`/`update`/`delete` accept positionally. Output does not read Field.Target:
// the server says which collection a relation points at. Input has no server
// answer to read yet, which is why Target exists (resolveRelationFlags, below),
// and the drift test checks it against the schema.
//
// WHAT WAS VERIFIED against a live server rather than assumed, because the
// failure modes here are all silent:
//
//   - An expanded record does carry `collectionName`.
//   - `?fields=` DROPS the `expand` key unless `expand` is named in it. So a
//     projection that includes a relation has to ask for both.
//   - An unset relation is simply absent from `expand` -- no entry, no error.
//   - A relation the caller may not read is also absent. Even with the target
//     collection's viewRule set to null (superuser-only), the list request
//     returned 200 with the expand key missing rather than failing. Expansion is
//     cosmetic and cannot break a command that would otherwise work, which is
//     why there is no fallback path here.
//   - An expand naming a field that does not exist is ignored, also 200.
//   - PocketBase MASKS an auth record's email unless it is the caller's own or
//     emailVisibility is set. An owner expanding a colleague's membership gets
//     `email: null` and `name: "Mo Member"`. Hence the users fallback below.

// relationLabelFallbacks names the display key for collections with no
// EntitySpec, in preference order.
//
// `users` is the only one that matters: it is the target of memberships.user,
// invites.invited_by and organizations.owner, and the CLI models no entity for
// it (see notAnEntity in schema_drift_test.go). Email is its natural key -- it
// is what `invite` itself keys on -- but the server masks it on anyone else's
// record, so `name` is the fallback to the fallback. A blank cell for a colleague
// would defeat the entire point of the column.
var relationLabelFallbacks = map[string][]string{
	"users": {"email", "name"},
}

// genericLabelKeys is the last resort, for a collection that is neither modelled
// nor listed above. Cosmetic by definition: an unlabelled cell renders empty.
var genericLabelKeys = []string{"code", "name"}

// relationColumns returns the subset of cols that name a relation field on spec.
//
// `organization` is accepted although no spec declares it as a Field: it is
// injected from the active context rather than typed, which is why it has no
// flag -- but `get` still prints it, and printing it as an id while every other
// relation on the same record reads as a code is the inconsistency this whole
// file exists to remove.
func relationColumns(spec EntitySpec, cols []string) []string {
	var out []string
	for _, c := range cols {
		if c == "organization" && spec.OrgScoped {
			out = append(out, c)
			continue
		}
		f := spec.field(c)
		if f == nil {
			continue
		}
		if f.Type == FID || f.Type == FIDs {
			out = append(out, c)
		}
	}
	return out
}

// withExpandField adds `expand` to a --fields projection.
//
// Required rather than tidy: PocketBase applies `fields` to the whole response
// body, and `expand` is a top-level key like any other, so `fields=code,type`
// returns the relation id and silently drops everything the expand was for.
func withExpandField(fields string) string {
	if fields == "" {
		return ""
	}
	for _, f := range splitFields(fields) {
		if f == "expand" {
			return fields
		}
	}
	return fields + ",expand"
}

// resolveRelationColumns rewrites each named relation column in place, replacing
// the id with the related record's natural key, and drops the `expand` key it
// read from. Records with nothing to show for a relation keep an empty cell --
// an unset relation and one the caller may not read are indistinguishable here,
// deliberately: the alternative is printing an id in a column whose whole
// purpose is not to.
//
// Mutating the records is safe because this runs on the way to a human-readable
// printer and nowhere else. Nothing downstream reads them again.
func resolveRelationColumns(items []pb.Record, cols []string) {
	if len(cols) == 0 {
		return
	}
	for _, r := range items {
		expand, _ := r["expand"].(map[string]any)
		for _, c := range cols {
			r[c] = relationLabelFor(expand[c])
		}
		delete(r, "expand")
	}
}

// relationLabelFor renders one expanded value: a single record, or a list of
// them for a multi-relation.
func relationLabelFor(v any) string {
	switch t := v.(type) {
	case map[string]any:
		return relationLabel(t)
	case []any:
		parts := make([]string, 0, len(t))
		for _, e := range t {
			if rec, ok := e.(map[string]any); ok {
				if label := relationLabel(rec); label != "" {
					parts = append(parts, label)
				}
			}
		}
		return strings.Join(parts, ",")
	default:
		return ""
	}
}

// relationLabel picks the human-readable key out of one expanded record.
func relationLabel(rec map[string]any) string {
	collection, _ := rec["collectionName"].(string)

	var keys []string
	if spec, ok := specByCollection(collection); ok && spec.LookupKey != "" {
		keys = []string{spec.LookupKey}
	}
	keys = append(keys, relationLabelFallbacks[collection]...)
	keys = append(keys, genericLabelKeys...)

	for _, k := range keys {
		if s, ok := rec[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// specByCollection finds the entity spec driving a PocketBase collection.
func specByCollection(collection string) (EntitySpec, bool) {
	if collection == "" {
		return EntitySpec{}, false
	}
	for _, s := range entitySpecs {
		if s.Collection == collection {
			return s, true
		}
	}
	return EntitySpec{}, false
}

// relationColumnsForFields returns the relation columns to expand for a request
// whose projection is `fields` -- empty meaning the whole record, which is what
// `get` asks for by default.
func relationColumnsForFields(spec EntitySpec, fields string) []string {
	if fields != "" {
		return relationColumns(spec, splitFields(fields))
	}
	names := make([]string, 0, len(spec.Fields)+1)
	if spec.OrgScoped {
		names = append(names, "organization")
	}
	for _, f := range spec.Fields {
		names = append(names, f.Name)
	}
	return relationColumns(spec, names)
}

// resolveRelationFlags lets a relation flag take what a positional argument
// takes: `--location hq` as well as `--location <id>`. Each value of a field
// with a Target goes through resolveRecordID, the function `get`, `update`,
// `delete` and `edit` already use, so the rules are the same everywhere: an
// id-shaped value is tried as an id first, then the target's natural key,
// scoped to the current organization, ignoring case for a code, and refused
// when it matches more than one record. An empty value is left alone, because
// it clears the relation.
//
// It runs on typed create and update, and `thing provision` calls
// resolveFlagValue for its own id flags. `apply` and `edit` send ids, the way
// the server stores them.
func resolveRelationFlags(client *pb.Client, spec EntitySpec, orgID string, data pb.Record) error {
	for _, f := range spec.Fields {
		if f.Target == "" {
			continue
		}
		switch v := data[f.Name].(type) {
		case string:
			id, err := resolveFlagValue(client, orgID, f.flagName(), f.Target, v)
			if err != nil {
				return err
			}
			data[f.Name] = id
		case []string:
			ids := make([]string, 0, len(v))
			for _, s := range v {
				id, err := resolveFlagValue(client, orgID, f.flagName(), f.Target, s)
				if err != nil {
					return err
				}
				ids = append(ids, id)
			}
			data[f.Name] = ids
		}
	}
	return nil
}

// resolveFlagValue resolves one flag value against the entity that owns
// collection. "" stays "".
func resolveFlagValue(client *pb.Client, orgID, flag, collection, v string) (string, error) {
	if v == "" {
		return "", nil
	}
	target, ok := specByCollection(collection)
	if !ok {
		return "", fmt.Errorf("--%s: no entity for collection %q", flag, collection)
	}
	id, err := resolveRecordID(client, target, orgID, v)
	if err != nil {
		return "", fmt.Errorf("--%s: %w", flag, err)
	}
	return id, nil
}

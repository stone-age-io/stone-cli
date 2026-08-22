package cmd

import (
	"fmt"
	"strings"
	"testing"
)

// The entity table in entity.go is the CLI's whole surface: registerCRUD turns
// each spec into a command tree, and pull/apply enumerate the same list. It is
// also hand-maintained -- deliberately, since it is not derived from the
// platform's schema.json -- so the failure modes below are all silent. Nothing
// in the Go type system objects to a typo'd field type, a duplicated alias, or a
// verb spelled "list", and none of them produce an error at startup: they
// produce a CLI that is quietly missing a flag, a command, or an entity.
//
// These tests are that objection.

// knownFieldTypes is the set addFieldFlags and collectFields actually dispatch
// on. Both switch without a default, so a type outside this set registers no
// flag at all and silently drops the field from create and update.
var knownFieldTypes = map[FieldType]bool{
	FString:  true,
	FInt:     true,
	FBool:    true,
	FJSON:    true,
	FID:      true,
	FIDs:     true,
	FSelect:  true,
	FMSelect: true,
}

// knownVerbs is what hasVerb is asked about and what registerCRUD builds. A
// spec listing a verb outside this set does not fail -- hasVerb simply returns
// false for the real verb it meant, so the command goes missing.
var knownVerbs = map[string]bool{
	"ls": true, "get": true, "create": true, "update": true, "delete": true, "edit": true,
}

// reservedFlags maps a verb to the flags its command registers itself. A field
// whose flag collides with one of these on the same command is a duplicate
// registration in cobra, not a merge.
var reservedFlags = map[string][]string{
	"ls":     {"filter", "sort", "fields"},
	"get":    {"fields"},
	"delete": {"yes", "y"},
	// create only registers this when the spec has a password field, which is
	// checked per-spec below rather than here.
}

// persistentFlags are registered on the root command in root.go and are
// therefore in scope for every subcommand.
var persistentFlags = []string{"context", "output", "o", "debug"}

func TestEntitySpecsAreWellFormed(t *testing.T) {
	if len(entitySpecs) == 0 {
		t.Fatal("entitySpecs is empty: the CLI would expose no entity commands at all")
	}

	for _, spec := range entitySpecs {
		t.Run(spec.Name, func(t *testing.T) {
			if spec.Name == "" || spec.Plural == "" || spec.Collection == "" {
				t.Fatalf("Name/Plural/Collection must all be set, got %q/%q/%q",
					spec.Name, spec.Plural, spec.Collection)
			}

			// Every list view needs columns; without them `ls` prints rows of
			// nothing but ids.
			if len(spec.KeyColumns) == 0 {
				t.Error("KeyColumns is empty, so `ls` has no columns to show")
			}

			for _, v := range spec.Verbs {
				if !knownVerbs[v] {
					t.Errorf("unknown verb %q: hasVerb will answer false for every verb this spec meant to allow", v)
				}
			}
			if dup := firstDuplicate(spec.Verbs); dup != "" {
				t.Errorf("verb %q is listed twice", dup)
			}

			// LookupKey is used to build a PocketBase filter in
			// resolveRecordID. Naming a field the collection does not have
			// turns every natural-key lookup into an opaque 400.
			if spec.LookupKey != "" && spec.field(spec.LookupKey) == nil {
				t.Errorf("LookupKey %q is not a declared field, so natural-key lookup filters on a column that may not exist", spec.LookupKey)
			}

			seenField := map[string]bool{}
			seenFlag := map[string]bool{}
			for _, f := range spec.Fields {
				if f.Name == "" {
					t.Error("a field has no Name, so it has no JSON key to write")
					continue
				}
				if seenField[f.Name] {
					t.Errorf("field %q is declared twice", f.Name)
				}
				seenField[f.Name] = true

				if !knownFieldTypes[f.Type] {
					t.Errorf("field %q has type %q, which addFieldFlags does not dispatch on: no flag would be registered and the field would be silently unsettable", f.Name, f.Type)
				}

				// Values is meaningful only for the two enum types, and
				// required for them: collectFields validates against it and
				// an empty list rejects every value the user could pass.
				switch f.Type {
				case FSelect, FMSelect:
					if len(f.Values) == 0 {
						t.Errorf("field %q is a %s with no Values, so collectFields rejects every value passed to it", f.Name, f.Type)
					}
				default:
					if len(f.Values) > 0 {
						t.Errorf("field %q is type %s but declares Values, which nothing reads", f.Name, f.Type)
					}
				}

				flag := f.flagName()
				if seenFlag[flag] {
					t.Errorf("field %q maps to flag --%s, which another field already claims", f.Name, flag)
				}
				seenFlag[flag] = true

				for _, p := range persistentFlags {
					if flag == p {
						t.Errorf("field %q maps to --%s, which root.go registers as a persistent flag", f.Name, flag)
					}
				}

				// A required field on an entity that cannot be created is
				// dead config: nothing ever enforces it.
				if f.Required && !spec.hasVerb("create") {
					t.Errorf("field %q is Required, but this spec has no create verb", f.Name)
				}
			}

			// Per-verb collisions, only for the verbs this spec actually has.
			for verb, reserved := range reservedFlags {
				if !spec.hasVerb(verb) {
					continue
				}
				for _, r := range reserved {
					if seenFlag[r] {
						t.Errorf("a field maps to --%s, which the %s command registers itself", r, verb)
					}
				}
			}
			// buildCreateCmd adds --random-password only when a password field
			// exists, so the collision is only possible on those specs.
			if spec.hasVerb("create") && spec.field("password") != nil && seenFlag["random-password"] {
				t.Error("a field maps to --random-password, which buildCreateCmd registers for password-bearing entities")
			}
		})
	}
}

// Command names and aliases all land in one cobra namespace. A collision is not
// an error there -- one command shadows the other, and the shadowed entity
// simply stops being reachable.
func TestEntityCommandNamesAndAliasesAreUnique(t *testing.T) {
	owner := map[string]string{}

	claim := func(token, entity, kind string) {
		if prev, ok := owner[token]; ok {
			t.Errorf("%s %q is claimed by both %q and %q: one shadows the other in cobra", kind, token, prev, entity)
			return
		}
		owner[token] = entity
	}

	for _, spec := range entitySpecs {
		claim(spec.Name, spec.Name, "command name")
		for _, a := range spec.aliases() {
			claim(a, spec.Name, "alias")
		}
	}
}

// Documented in aliases(): "nats-user" is reachable as nats-users, nats_users
// and nats_user. This pins that behaviour, including that the canonical name is
// never repeated as an alias (cobra would list it twice in help output).
func TestAliasesCoverTheFormsUsersType(t *testing.T) {
	spec := EntitySpec{Name: "nats-user", Plural: "nats-users", Collection: "nats_users"}

	got := spec.aliases()
	for _, want := range []string{"nats-users", "nats_users", "nats_user"} {
		if !containsString(got, want) {
			t.Errorf("alias %q missing from %v", want, got)
		}
	}
	if containsString(got, spec.Name) {
		t.Errorf("aliases include the canonical name %q: %v", spec.Name, got)
	}
	if dup := firstDuplicate(got); dup != "" {
		t.Errorf("alias %q is repeated in %v", dup, got)
	}
}

func TestFlagNameDerivation(t *testing.T) {
	if got := (Field{Name: "floorplan_position"}).flagName(); got != "floorplan-position" {
		t.Errorf("underscores should become hyphens, got %q", got)
	}
	// An explicit Flag wins, which is the escape hatch for a field whose JSON
	// key makes an awkward flag.
	if got := (Field{Name: "role_id", Flag: "role"}).flagName(); got != "role" {
		t.Errorf("explicit Flag should win, got %q", got)
	}
}

func TestHasVerbTreatsEmptyAsEverything(t *testing.T) {
	all := EntitySpec{}
	for verb := range knownVerbs {
		if !all.hasVerb(verb) {
			t.Errorf("an empty Verbs list should allow %q", verb)
		}
	}

	limited := EntitySpec{Verbs: []string{"ls", "get"}}
	if !limited.hasVerb("ls") || limited.hasVerb("delete") {
		t.Error("a non-empty Verbs list should allow exactly what it lists")
	}
}

func TestComposeOrgFilter(t *testing.T) {
	orgScoped := EntitySpec{OrgScoped: true}
	global := EntitySpec{}

	cases := []struct {
		name  string
		spec  EntitySpec
		org   string
		extra string
		want  string
	}{
		{"org scope only", orgScoped, "abc", "", `organization="abc"`},
		// The extra filter is parenthesised, so an OR inside it cannot escape
		// the org scope and widen the query across tenants.
		{"org scope and extra", orgScoped, "abc", `a=1 || b=2`, `organization="abc" && (a=1 || b=2)`},
		{"extra only, global entity", global, "abc", "name='x'", `(name='x')`},
		{"nothing", global, "", "", ""},
		{"org scoped but no org resolved", orgScoped, "", "", ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := composeOrgFilter(c.spec, c.org, c.extra); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestSplitFields(t *testing.T) {
	cases := map[string][]string{
		"code,name":     {"code", "name"},
		" code , name ": {"code", "name"},
		"code,,name":    {"code", "name"},
		"":              nil,
		",":             nil,
	}
	for in, want := range cases {
		got := splitFields(in)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("splitFields(%q) = %v, want %v", in, got, want)
		}
	}
}

// firstDuplicate returns the first repeated element, or "" if there are none.
func firstDuplicate(items []string) string {
	seen := map[string]bool{}
	for _, s := range items {
		if seen[s] {
			return s
		}
		seen[s] = true
	}
	return ""
}

// Guard against a spec that claims to be org-scoped for a collection the
// platform does not scope by organization, and vice versa. Kept as a short
// explicit list rather than derived: it is the one place the CLI's model of the
// platform's tenancy is written down, and it should be reviewed by a human when
// it changes.
func TestOrgScopingMatchesThePlatformsModel(t *testing.T) {
	notOrgScoped := map[string]bool{
		// An organization is not inside an organization, and a membership
		// binds a user to one rather than living in one -- the CLI filters
		// memberships by the user instead.
		"organizations": true,
		"memberships":   true,
	}

	for _, spec := range entitySpecs {
		want := !notOrgScoped[spec.Collection]
		if spec.OrgScoped != want {
			t.Errorf("%s (collection %q): OrgScoped is %v, want %v",
				spec.Name, spec.Collection, spec.OrgScoped, want)
		}
	}
}

func TestEveryEntityIsReachableByAtLeastOneVerb(t *testing.T) {
	for _, spec := range entitySpecs {
		var have []string
		for verb := range knownVerbs {
			if spec.hasVerb(verb) {
				have = append(have, verb)
			}
		}
		if len(have) == 0 {
			t.Errorf("%s has no verbs, so registerCRUD builds a command tree nobody can use", spec.Name)
		}
		if !spec.hasVerb("ls") && !spec.hasVerb("get") {
			t.Errorf("%s supports neither ls nor get: %v", spec.Name, strings.Join(have, ","))
		}
	}
}

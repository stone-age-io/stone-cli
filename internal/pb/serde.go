package pb

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"gopkg.in/yaml.v3"
)

// ServerOnlyFields are written by the server and ignored on apply.
var ServerOnlyFields = map[string]struct{}{
	"collectionId":   {},
	"collectionName": {},
	"created":        {},
	"updated":        {},
	"expand":         {},
}

// Strip removes server-only fields in place.
func Strip(r Record) Record {
	for k := range ServerOnlyFields {
		delete(r, k)
	}
	return r
}

// UnmarshalFile parses a record file by extension. Supports .yaml/.yml/.json.
func UnmarshalFile(path string) (Record, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	r := make(Record)
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		if err := yaml.Unmarshal(data, &r); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		// yaml.v3 returns map[string]interface{} keys correctly, but nested
		// maps come back as map[string]interface{}. Good.
	case ".json":
		if err := json.Unmarshal(data, &r); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
	default:
		return nil, fmt.Errorf("unsupported file extension: %s", filepath.Ext(path))
	}
	return r, nil
}

// MarshalFile writes a record by extension. Default is YAML.
func MarshalFile(path string, r Record) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var data []byte
	var err error
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		data, err = json.MarshalIndent(r, "", "  ")
	case ".yaml", ".yml", "":
		data, err = yaml.Marshal(r)
	default:
		return fmt.Errorf("unsupported file extension: %s", filepath.Ext(path))
	}
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// PrintRecord writes a single record in the requested format.
// Format: "json" | "yaml" | "" (= human-friendly key/value).
func PrintRecord(w io.Writer, r Record, format string) error {
	switch format {
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	case "yaml":
		data, err := yaml.Marshal(r)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	default:
		keys := sortedKeys(r)
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, k := range keys {
			fmt.Fprintf(tw, "%s\t%s\n", k, formatVal(r[k]))
		}
		return tw.Flush()
	}
}

// PrintList writes a list of records in the requested format.
// For "table"/"" output, the columns argument is the full ordered list of
// column names; the caller decides whether to include "id" / "name" / etc.
func PrintList(w io.Writer, items []Record, columns []string, format string) error {
	switch format {
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(items)
	case "yaml":
		data, err := yaml.Marshal(items)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	default:
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for i, c := range columns {
			if i > 0 {
				fmt.Fprint(tw, "\t")
			}
			fmt.Fprint(tw, strings.ToUpper(c))
		}
		fmt.Fprintln(tw)
		for _, it := range items {
			for i, c := range columns {
				if i > 0 {
					fmt.Fprint(tw, "\t")
				}
				fmt.Fprint(tw, formatVal(it[c]))
			}
			fmt.Fprintln(tw)
		}
		return tw.Flush()
	}
}

func sortedKeys(r Record) []string {
	keys := make([]string, 0, len(r))
	for k := range r {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func formatVal(v any) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case []any:
		parts := make([]string, len(t))
		for i, e := range t {
			parts[i] = formatVal(e)
		}
		return strings.Join(parts, ",")
	case map[string]any:
		data, _ := json.Marshal(t)
		return string(data)
	case float64:
		// JSON numbers come back as float64; render ints cleanly.
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return fmt.Sprintf("%v", t)
	default:
		return fmt.Sprintf("%v", v)
	}
}

package cmd

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stone-age-io/stone-cli/internal/pb"
)

// captureStdout runs fn with os.Stdout redirected, for commands that print
// with fmt.Printf.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		var b bytes.Buffer
		_, _ = io.Copy(&b, r)
		done <- b.String()
	}()
	runErr := fn()
	os.Stdout = prev
	w.Close()
	return <-done, runErr
}

func TestServerOnlyRecords(t *testing.T) {
	ws := t.TempDir()
	writeFile(t, filepath.Join(ws, "things", "T-1.yaml"), "id: aaaaaaaaaaaaaaa\n")
	files := []applyFile{{Collection: "things", ID: "aaaaaaaaaaaaaaa"}, {Collection: "things"}}
	listed := map[string][]pb.Record{
		"things": {
			{"id": "aaaaaaaaaaaaaaa", "organization": orgA, "code": "T-1"},
			{"id": "ccccccccccccccc", "organization": orgA, "code": "T-9"},
			{"id": "ddddddddddddddd", "organization": "orgB00000000000", "code": "B-1"}, // another org: pull would not write it
		},
		// No locations/ directory in the workspace: never pulled, so not reported.
		"locations": {{"id": "eeeeeeeeeeeeeee", "organization": orgA, "code": "HQ"}},
	}
	var asked []string
	got, err := serverOnlyRecords(ws, files, orgA, func(coll string) ([]pb.Record, error) {
		asked = append(asked, coll)
		return listed[coll], nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"things/T-9 [ccccccccccccccc]"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if !reflect.DeepEqual(asked, []string{"things"}) {
		t.Fatalf("listed %v; only collections with a directory should be", asked)
	}
}

func TestApplyCommandReportsRecordsWithNoFile(t *testing.T) {
	fake, _, run := applyWorkspace(t, nil)
	fake.records = append(fake.records, pb.Record{"id": "ccccccccccccccc", "organization": orgA, "code": "T-9"})
	out, err := captureStdout(t, run)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no file  things/T-9 [ccccccccccccccc]") || !strings.Contains(out, "failed 0  no file 1") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestApplyCommandExitCode(t *testing.T) {
	// The workspace has a create and an update pending.
	_, ws, run := applyWorkspace(t, map[string]string{"dry-run": "true", "exit-code": "true"})
	if _, err := captureStdout(t, run); !errors.Is(err, errChangesPending) {
		t.Fatalf("err = %v, want errChangesPending", err)
	}
	// In step: exit 0.
	_ = os.Remove(filepath.Join(ws, "things", "T-3.yaml"))
	writeFile(t, filepath.Join(ws, "things", "T-1.yaml"), "id: aaaaaaaaaaaaaaa\norganization: "+orgA+"\ncode: T-1\nname: one\n")
	if _, err := captureStdout(t, run); err != nil {
		t.Fatalf("err = %v, want nil when nothing is pending", err)
	}

	_ = applyCmd.Flags().Set("dry-run", "false")
	if err := run(); err == nil || !strings.Contains(err.Error(), "needs --dry-run") {
		t.Fatalf("--exit-code without --dry-run: err = %v", err)
	}
}

func TestJSApplyReportsNoFileAndExitCode(t *testing.T) {
	h := newJSHarness(t)
	h.seed()
	h.pull()
	if _, err := h.api.createStream(h.ctx, "SCRATCH", map[string]any{"name": "SCRATCH", "subjects": []string{"scratch.>"}}); err != nil {
		t.Fatal(err)
	}

	out, err := h.apply(jsApplyOptions{DryRun: true, ExitCode: true, ReportNoFile: true})
	if err != nil {
		t.Fatalf("nothing pending: err = %v\n%s", err, out)
	}
	if !strings.Contains(out, "no file  streams/SCRATCH") || strings.Contains(out, "files") ||
		!strings.Contains(out, "unchanged 2  no file 1") {
		t.Fatalf("output:\n%s", out)
	}

	orders := h.file(jsStreamsDir, "ORDERS")
	orders["max_age"] = "2h"
	h.writeFile(jsStreamsDir, "ORDERS", orders)
	if _, err := h.apply(jsApplyOptions{DryRun: true, ExitCode: true}); !errors.Is(err, errChangesPending) {
		t.Fatalf("change pending: err = %v, want errChangesPending", err)
	}
	if h.server("ORDERS")["max_age"] != int64(time.Hour) {
		t.Fatal("dry run changed the server")
	}
}

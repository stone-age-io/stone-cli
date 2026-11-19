package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stone-age-io/stone-cli/internal/ctx"
	"github.com/stone-age-io/stone-cli/internal/pb"
)

// What nats-server 2.14.2 returned for a stream created with only a name, a
// subject and metadata, and for `nats kv add twin --history=10`.
const (
	rawOrders = `{"name":"ORDERS","subjects":["orders.>"],"retention":"limits","max_consumers":-1,"max_msgs":-1,"max_bytes":-1,"max_age":0,"max_msgs_per_subject":-1,"max_msg_size":-1,"discard":"old","storage":"file","num_replicas":1,"duplicate_window":120000000000,"compression":"none","allow_direct":false,"mirror_direct":false,"sealed":false,"deny_delete":false,"deny_purge":false,"allow_rollup_hdrs":false,"consumer_limits":{},"allow_msg_ttl":false,"metadata":{"_nats.level":"4","_nats.req.level":"0","_nats.ver":"2.14.2","team":"b"}}`
	rawTwin   = `{"name":"KV_twin","subjects":["$KV.twin.>"],"retention":"limits","max_consumers":-1,"max_msgs":-1,"max_bytes":-1,"max_age":0,"max_msgs_per_subject":10,"max_msg_size":-1,"discard":"new","storage":"file","num_replicas":1,"duplicate_window":120000000000,"compression":"none","allow_direct":true,"mirror_direct":false,"sealed":false,"deny_delete":true,"deny_purge":false,"allow_rollup_hdrs":true,"consumer_limits":{},"allow_msg_ttl":false,"metadata":{"_nats.level":"4","_nats.req.level":"0","_nats.ver":"2.14.2"}}`
)

func rawConfig(t *testing.T, s string, edits map[string]any) map[string]any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		t.Fatal(err)
	}
	out := canonicalJSON(m).(map[string]any)
	for k, v := range edits {
		out[k] = canonicalJSON(v)
	}
	return out
}

func TestNormalizeDropsWhatTheServerFillsIn(t *testing.T) {
	got, err := normalizeJSConfig(kindStream, rawConfig(t, rawOrders, nil))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"name":     "ORDERS",
		"subjects": []any{"orders.>"},
		"metadata": map[string]any{"team": "b"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized\n  %v\nwant\n  %v", got, want)
	}
}

func TestNormalizeDurations(t *testing.T) {
	// A file may spell a duration any way Go parses, or as nanoseconds, and
	// spell a default out; all of it normalizes to what pull writes.
	file := map[string]any{
		"name":             "ORDERS",
		"max_age":          "24h0m0s",
		"duplicate_window": "120s",
		"max_msgs":         -1,
		"consumer_limits":  map[string]any{"inactive_threshold": int64(90 * time.Second)},
	}
	got, err := normalizeJSConfig(kindStream, file)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"name":            "ORDERS",
		"max_age":         "24h",
		"consumer_limits": map[string]any{"inactive_threshold": "1m30s"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized\n  %v\nwant\n  %v", got, want)
	}
	if file["max_age"] != "24h0m0s" {
		t.Fatal("normalizing edited the file's own map")
	}
	if _, err := normalizeJSConfig(kindStream, map[string]any{"max_age": "a day"}); err == nil {
		t.Fatal("a duration that does not parse was accepted")
	}
}

func TestConfigHash(t *testing.T) {
	base, err := jsConfigHash(rawConfig(t, rawOrders, nil))
	if err != nil {
		t.Fatal(err)
	}
	// A server upgrade rewrites _nats.ver; that is not a change to the stream.
	upgraded, _ := jsConfigHash(rawConfig(t, rawOrders, map[string]any{
		"metadata": map[string]any{"_nats.ver": "2.15.0", "team": "b"},
	}))
	if upgraded != base {
		t.Fatal("hash changed with the server's own metadata")
	}
	edited, _ := jsConfigHash(rawConfig(t, rawOrders, map[string]any{"max_age": int64(time.Hour)}))
	if edited == base {
		t.Fatal("hash did not change with max_age")
	}
}

func TestKVBucketReadsBackFromItsStream(t *testing.T) {
	raw := rawConfig(t, rawTwin, nil)
	got, err := kvFileForm(kvConfigFromStream(raw))
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]any{"bucket": "twin", "history": int64(10)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("bucket file\n  %v\nwant\n  %v", got, want)
	}
	if unrep, err := kvUnrepresentable(raw); err != nil || len(unrep) != 0 {
		t.Fatalf("a stream nats.go made reports unrepresentable settings %v (%v)", unrep, err)
	}
	// Settings no bucket config produces, which UpdateKeyValue would reset.
	for key, val := range map[string]any{
		"deny_purge":       true,
		"duplicate_window": int64(30 * time.Second),
		"discard":          "old",
	} {
		unrep, err := kvUnrepresentable(rawConfig(t, rawTwin, map[string]any{key: val}))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(unrep, []string{key}) {
			t.Errorf("%s=%v: unrepresentable = %v, want [%s]", key, val, unrep, key)
		}
	}
}

func TestParseJSFile(t *testing.T) {
	dir := t.TempDir()
	write := func(sub, name, body string) string {
		p := filepath.Join(dir, sub, name)
		writeFile(t, p, body)
		return p
	}
	f, err := parseJSFile(write("streams", "o.yaml", "name: ORDERS\norganization: orgA\nconfig_hash: abc\nmax_age: 1h\n"), kindStream)
	if err != nil {
		t.Fatal(err)
	}
	if f.Name != "ORDERS" || f.Org != "orgA" || f.Hash != "abc" {
		t.Fatalf("parsed %+v", f)
	}
	if _, ok := f.Body[jsKeyHash]; ok {
		t.Fatal("config_hash left in the body that is sent")
	}
	if _, err := parseJSFile(write("streams", "kv.yaml", "name: KV_twin\n"), kindStream); err == nil || !strings.Contains(err.Error(), "kv/twin.yaml") {
		t.Fatalf("a KV_ stream file was accepted (err = %v)", err)
	}
	if _, err := parseJSFile(write("kv", "none.yaml", "history: 3\n"), kindKV); err == nil {
		t.Fatal("a bucket file without bucket was accepted")
	}
	k, _ := parseJSFile(write("kv", "typo.yaml", "bucket: twin\nhistroy: 3\n"), kindKV)
	if _, err := k.kvConfig(); err == nil || !strings.Contains(err.Error(), "histroy") {
		t.Fatalf("a misspelt bucket key was accepted (err = %v)", err)
	}
}

func TestApplySkipsTheJetStreamDirectory(t *testing.T) {
	fake, ws, run := applyWorkspace(t, nil)
	writeFile(t, filepath.Join(ws, jsDir, jsStreamsDir, "ORDERS.yaml"), "name: ORDERS\nsubjects: [orders.>]\n")
	writeFile(t, filepath.Join(ws, jsDir, jsKVDir, "twin.yaml"), "bucket: twin\n")
	if err := run(); err != nil {
		t.Fatal(err)
	}
	for _, op := range fake.sent {
		if strings.Contains(op.URL, "streams") || strings.Contains(op.URL, "/kv/") {
			t.Fatalf("stone apply sent a JetStream file: %+v", op)
		}
	}
	if len(fake.sent) != 2 {
		t.Fatalf("sent %d ops, want the 2 the things/ files need", len(fake.sent))
	}
}

// ---- against a real nats-server ---------------------------------------------

// startJetStream runs the nats-server on PATH with JetStream on, or skips.
func startJetStream(t *testing.T) jetstream.JetStream {
	t.Helper()
	port := freePort(t)
	nc := connectNATS(t, runNATSServer(t, port, "-js", "-a", "127.0.0.1", "-p", fmt.Sprint(port), "-sd", t.TempDir()))
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	return js
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// runNATSServer starts the nats-server on PATH with args, listening on port,
// and returns its URL once it accepts connections; or skips.
func runNATSServer(t *testing.T, port int, args ...string) string {
	t.Helper()
	bin, err := exec.LookPath("nats-server")
	if err != nil {
		t.Skip("nats-server not on PATH")
	}
	srv := exec.Command(bin, args...)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Process.Kill(); _ = srv.Wait() })

	url := fmt.Sprintf("nats://127.0.0.1:%d", port)
	for deadline := time.Now().Add(10 * time.Second); ; {
		conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			conn.Close()
			return url
		}
		if time.Now().After(deadline) {
			t.Fatalf("nats-server did not come up: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func connectNATS(t *testing.T, url string, opts ...nats.Option) *nats.Conn {
	t.Helper()
	nc, err := nats.Connect(url, opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	return nc
}

// The `gateway` role the platform's demo seed gives an edge site
// (internal/demoseed/contract.go): it lets a leaf's local services reach the
// hub's JetStream, but not the hub reach the leaf's.
var (
	gatewayRolePub = []string{"telemetry.>", "event.>", "status.>", "gateway.>", "acc.>", "kiosk.>", "_INBOX.>", "$JS.API.>", "$KV.>"}
	gatewayRoleSub = []string{"telemetry.>", "event.>", "status.>", "gateway.>", "acc.>", "kiosk.>", "cmd.>", "config.>", "_INBOX.>", "$SRV.>"}
)

// edgeSite is a hub and one leaf node shaped like an edge site: the leaf runs
// JetStream in domain `s01` (a Thing's code, as in the agent's nats-leaf.conf)
// and dials the hub as `edge`, the uplink credential, whose permissions the
// hub applies to the leaf connection.
type edgeSite struct {
	hub   *nats.Conn // a tenant session at the hub, where stone connects
	local *nats.Conn // a session on the leaf itself, like the agent's
}

const edgeDomain = "s01"

func startEdgeSite(t *testing.T, uplinkPub, uplinkSub []string) edgeSite {
	t.Helper()
	if _, err := exec.LookPath("nats-server"); err != nil {
		t.Skip("nats-server not on PATH")
	}
	quote := func(ss []string) string {
		q := make([]string, len(ss))
		for i, s := range ss {
			q[i] = fmt.Sprintf("%q", s)
		}
		return "[" + strings.Join(q, ", ") + "]"
	}
	perms := ""
	if uplinkPub != nil || uplinkSub != nil {
		perms = fmt.Sprintf(", permissions: { publish: { allow: %s }, subscribe: { allow: %s } }", quote(uplinkPub), quote(uplinkSub))
	}
	hubPort, leafnodePort, leafPort := freePort(t), freePort(t), freePort(t)
	dir := t.TempDir()
	write := func(name, conf string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(conf), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	hubConf := write("hub.conf", fmt.Sprintf(`
server_name: hub
listen: "127.0.0.1:%d"
jetstream { domain: hub, store_dir: %q }
accounts {
  ACME: {
    jetstream: enabled
    users: [
      { user: console, password: console }
      { user: edge, password: edge%s }
    ]
  }
}
leafnodes { listen: "127.0.0.1:%d" }
`, hubPort, filepath.Join(dir, "hub-js"), perms, leafnodePort))
	leafConf := write("leaf.conf", fmt.Sprintf(`
server_name: %s
listen: "127.0.0.1:%d"
jetstream { domain: %s, store_dir: %q }
accounts {
  ACME: { jetstream: enabled, users: [ { user: agent, password: agent } ] }
}
leafnodes { remotes: [ { url: "nats-leaf://edge:edge@127.0.0.1:%d", account: ACME } ] }
`, edgeDomain, leafPort, edgeDomain, filepath.Join(dir, "leaf-js"), leafnodePort))

	hubURL := runNATSServer(t, hubPort, "-c", hubConf)
	leafURL := runNATSServer(t, leafPort, "-c", leafConf)
	site := edgeSite{
		hub:   connectNATS(t, hubURL, nats.UserInfo("console", "console")),
		local: connectNATS(t, leafURL, nats.UserInfo("agent", "agent")),
	}

	// Wait for the leaf link, with an ordinary request every uplink here
	// lets through, so a silent JetStream request means the uplink and not
	// a leaf that has yet to attach.
	sub, err := site.local.Subscribe("gateway.ping", func(m *nats.Msg) { _ = m.Respond([]byte("pong")) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	for deadline := time.Now().Add(10 * time.Second); ; {
		if _, err := site.hub.Request("gateway.ping", nil, 250*time.Millisecond); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the leaf never answered through the hub")
		}
		time.Sleep(50 * time.Millisecond)
	}
	return site
}

// TestJSDomainReachesALeaf runs pull and apply through the hub against a
// leaf's JetStream domain, and checks the hub's files and the leaf's stay in
// their own directories and their own servers.
func TestJSDomainReachesALeaf(t *testing.T) {
	site := startEdgeSite(t, nil, nil)
	cctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	hubJS, _ := jetstream.New(site.hub)
	leafJS, _ := jetstream.NewWithDomain(site.hub, edgeDomain)
	localJS, _ := jetstream.New(site.local)
	if err := checkJSDomain(cctx, newJSAPI(leafJS)); err != nil {
		t.Fatalf("checkJSDomain: %v", err)
	}

	if _, err := hubJS.CreateStream(cctx, jetstream.StreamConfig{Name: "HUB", Subjects: []string{"hub.>"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := localJS.CreateStream(cctx, jetstream.StreamConfig{Name: "EDGE", Subjects: []string{"edge.>"}, MaxAge: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if _, err := localJS.CreateKeyValue(cctx, jetstream.KeyValueConfig{Bucket: "cfg", History: 3}); err != nil {
		t.Fatal(err)
	}

	ws := t.TempDir()
	hubDir, leafDir := jsWorkspaceDir(ws, ""), jsWorkspaceDir(ws, edgeDomain)
	var out bytes.Buffer
	if err := jsPull(cctx, hubJS, hubDir, orgA, &out, &out); err != nil {
		t.Fatal(err)
	}
	if err := jsPull(cctx, leafJS, leafDir, orgA, &out, &out); err != nil {
		t.Fatal(err)
	}
	read := func(path string) pb.Record {
		t.Helper()
		r, err := pb.UnmarshalFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := read(filepath.Join(hubDir, jsStreamsDir, "HUB.yaml")); r[jsKeyDomain] != nil {
		t.Fatalf("a hub file records a domain: %v", r)
	}
	edge := read(filepath.Join(ws, jsDir, jsDomainsDir, edgeDomain, jsStreamsDir, "EDGE.yaml"))
	if edge[jsKeyDomain] != edgeDomain || edge[jsKeyOrg] != orgA || edge["max_age"] != "1h" {
		t.Fatalf("EDGE.yaml = %v", edge)
	}
	if r := read(filepath.Join(leafDir, jsKVDir, "cfg.yaml")); r[jsKeyDomain] != edgeDomain || r["history"] != 3 {
		t.Fatalf("cfg.yaml = %v", r)
	}
	for _, p := range []string{filepath.Join(hubDir, jsStreamsDir, "EDGE.yaml"), filepath.Join(leafDir, jsStreamsDir, "HUB.yaml")} {
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("%s was pulled from the wrong domain", p)
		}
	}

	// The hub's walk steps over domains/; the leaf's sees only its own.
	hubFiles, err := readJSFiles([]string{hubDir}, &out)
	if err != nil || len(hubFiles) != 1 || hubFiles[0].Name != "HUB" {
		t.Fatalf("hub files: %v %v", hubFiles, err)
	}

	// Edit the leaf's stream and add a bucket with no domain key: the
	// directory places it.
	edge["max_age"] = "2h"
	if err := pb.MarshalFile(filepath.Join(leafDir, jsStreamsDir, "EDGE.yaml"), edge); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(leafDir, jsKVDir, "fresh.yaml"), "bucket: fresh\nhistory: 2\n")
	leafFiles, err := readJSFiles([]string{leafDir}, &out)
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := jsApply(cctx, leafJS, leafFiles, orgA, jsApplyOptions{Domain: edgeDomain}, &out, &out); err != nil ||
		!strings.Contains(out.String(), "created 1  updated 1  unchanged 1  failed 0") {
		t.Fatalf("apply to the leaf: %v\n%s", err, out.String())
	}
	if s, err := localJS.Stream(cctx, "EDGE"); err != nil || s.CachedInfo().Config.MaxAge != 2*time.Hour {
		t.Fatalf("the leaf's EDGE after apply: %v", err)
	}
	if _, err := localJS.KeyValue(cctx, "fresh"); err != nil {
		t.Fatalf("bucket fresh is not on the leaf: %v", err)
	}
	if _, err := hubJS.Stream(cctx, "KV_fresh"); err == nil {
		t.Fatal("bucket fresh was created on the hub too")
	}
	if r := read(filepath.Join(leafDir, jsKVDir, "fresh.yaml")); r[jsKeyDomain] != edgeDomain || r[jsKeyHash] == nil {
		t.Fatalf("a created bucket's file did not get its domain and hash: %v", r)
	}

	// A leaf's file applied to the hub, and a hub file applied to the leaf,
	// both stop before anything is sent.
	out.Reset()
	if err := jsApply(cctx, hubJS, leafFiles, orgA, jsApplyOptions{}, &out, &out); err == nil ||
		!strings.Contains(err.Error(), "belongs to domain s01, but apply is connected to the hub") {
		t.Fatalf("leaf files applied to the hub: %v", err)
	}
	if err := jsApply(cctx, leafJS, hubFiles, orgA, jsApplyOptions{Domain: edgeDomain}, &out, &out); err == nil ||
		!strings.Contains(err.Error(), "belongs to the hub, but apply is connected to domain s01") {
		t.Fatalf("hub files applied to the leaf: %v", err)
	}
	if _, err := localJS.Stream(cctx, "HUB"); err == nil {
		t.Fatal("the hub's stream reached the leaf")
	}
}

// TestJSDomainNeedsTheUplinkToAllowIt pins why the demo `gateway` role cannot
// carry `--domain`: the hub forwards to a leaf only what the uplink credential
// may subscribe to. checkJSDomain must say so rather than time out.
func TestJSDomainNeedsTheUplinkToAllowIt(t *testing.T) {
	cases := []struct {
		name string
		sub  []string
		ok   bool
	}{
		{"demo gateway role", gatewayRoleSub, false},
		{"gateway role plus the domain API", append(append([]string{}, gatewayRoleSub...), "$JS.*.API.>"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			site := startEdgeSite(t, gatewayRolePub, tc.sub)
			leafJS, _ := jetstream.NewWithDomain(site.hub, edgeDomain)
			cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err := checkJSDomain(cctx, newJSAPI(leafJS))
			if tc.ok && err != nil {
				t.Fatalf("checkJSDomain: %v", err)
			}
			if !tc.ok && (err == nil || !strings.Contains(err.Error(), "subscribe list of its NATS role")) {
				t.Fatalf("checkJSDomain through a gateway uplink = %v, want the uplink explained", err)
			}
		})
	}
}

// TestResolveJSDomain: --domain names a thing, by code in any case or by id,
// and yields the code exactly as stored, which is the leaf's domain. A name
// that is no thing is an error here, not a request no leaf answers.
func TestResolveJSDomain(t *testing.T) {
	const id = "thg000000000001"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/things/records"):
			items := []pb.Record{}
			if f := r.URL.Query().Get("filter"); strings.Contains(f, `"site-01"`) && strings.Contains(f, orgA) {
				items = append(items, pb.Record{"id": id})
			}
			_ = json.NewEncoder(w).Encode(pb.ListResult{Page: 1, TotalPages: 1, Items: items})
		case strings.HasSuffix(r.URL.Path, "/things/records/"+id):
			_ = json.NewEncoder(w).Encode(pb.Record{"id": id, "code": "SITE-01"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c := ctx.Context{URL: srv.URL, Auth: ctx.AuthSection{Token: "not-a-jwt"}, CurrentOrganization: orgA}

	for _, arg := range []string{"site-01", "Site-01", id} {
		if got, err := resolveJSDomain(c, arg); err != nil || got != "SITE-01" {
			t.Errorf("resolveJSDomain(%q) = %q, %v; want SITE-01", arg, got, err)
		}
	}
	if _, err := resolveJSDomain(c, "site-1"); err == nil || !strings.Contains(err.Error(), "--domain") {
		t.Errorf("a code no thing has was accepted: %v", err)
	}
	if got, err := resolveJSDomain(c, ""); got != "" || err != nil {
		t.Errorf("no --domain = %q, %v; want the hub", got, err)
	}
}

// TestJSDomainOfAFile: a domain key wins over the directory, which wins over
// nothing (the hub); and a hub connection whose nats context names a domain
// accepts hub files that record it.
func TestJSDomainOfAFile(t *testing.T) {
	ws := t.TempDir()
	put := func(rel, body string) *jsFile {
		t.Helper()
		p := filepath.Join(ws, rel)
		writeFile(t, p, body)
		kind := kindStream
		if filepath.Base(filepath.Dir(p)) == jsKVDir {
			kind = kindKV
		}
		f, err := parseJSFile(p, kind)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	hub := put("jetstream/streams/A.yaml", "name: A\n")
	hubKeyed := put("jetstream/streams/B.yaml", "name: B\ndomain: hub\n")
	byDir := put("jetstream/domains/s01/kv/c.yaml", "bucket: c\n")
	moved := put("jetstream/domains/s02/streams/D.yaml", "name: D\ndomain: s01\n")
	if byDir.domain() != "s01" || moved.domain() != "s01" || hub.domain() != "" {
		t.Fatalf("domains: hub=%q byDir=%q moved=%q", hub.domain(), byDir.domain(), moved.domain())
	}
	if _, ok := byDir.Body[jsKeyDomain]; ok {
		t.Fatal("domain left in the body that is sent")
	}
	if _, ok := moved.Body[jsKeyDomain]; ok {
		t.Fatal("domain left in the body that is sent")
	}

	for _, tc := range []struct {
		f         *jsFile
		opt       jsApplyOptions
		hubDomain string
		want      bool
	}{
		{hub, jsApplyOptions{}, "", true},
		{hub, jsApplyOptions{}, "hub", true},
		{hub, jsApplyOptions{Domain: "s01"}, "", false},
		{hubKeyed, jsApplyOptions{}, "hub", true},
		{hubKeyed, jsApplyOptions{}, "", false},
		{byDir, jsApplyOptions{Domain: "s01"}, "", true},
		{byDir, jsApplyOptions{}, "", false},
		{moved, jsApplyOptions{Domain: "s02"}, "", false},
		{moved, jsApplyOptions{Domain: "s01"}, "", true},
	} {
		if got := tc.f.belongsTo(tc.opt, tc.hubDomain); got != tc.want {
			t.Errorf("%s belongsTo(domain=%q, hub=%q) = %v, want %v", tc.f.label(), tc.opt.Domain, tc.hubDomain, got, tc.want)
		}
	}

	files, err := readJSFiles([]string{filepath.Join(ws, jsDir)}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("the hub's walk read %d files, want A and B only", len(files))
	}
	if files, err = readJSFiles([]string{filepath.Join(ws, jsDir, jsDomainsDir)}, io.Discard); err != nil || len(files) != 2 {
		t.Fatalf("a walk rooted at domains/ read %d files (%v), want both domains'", len(files), err)
	}
	if got := jsWorkspaceDir("w", "s01"); got != filepath.Join("w", jsDir, jsDomainsDir, "s01") {
		t.Fatalf("jsWorkspaceDir = %s", got)
	}
}

type jsHarness struct {
	t   *testing.T
	js  jetstream.JetStream
	api jsAPI
	dir string // <workspace>/jetstream
	ctx context.Context
}

func newJSHarness(t *testing.T) *jsHarness {
	js := startJetStream(t)
	cctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return &jsHarness{t: t, js: js, api: newJSAPI(js), dir: filepath.Join(t.TempDir(), jsDir), ctx: cctx}
}

func (h *jsHarness) pull() string {
	h.t.Helper()
	var out, errOut bytes.Buffer
	if err := jsPull(h.ctx, h.js, h.dir, orgA, &out, &errOut); err != nil {
		h.t.Fatal(err)
	}
	return out.String() + errOut.String()
}

func (h *jsHarness) apply(opt jsApplyOptions) (string, error) {
	h.t.Helper()
	files, err := readJSFiles([]string{h.dir}, os.Stderr)
	if err != nil {
		h.t.Fatal(err)
	}
	var out bytes.Buffer
	err = jsApply(h.ctx, h.js, files, orgA, opt, &out, &out)
	return out.String(), err
}

func (h *jsHarness) server(stream string) map[string]any {
	h.t.Helper()
	cfg, found, err := h.api.streamConfig(h.ctx, stream)
	if err != nil || !found {
		h.t.Fatalf("stream %s: found=%v err=%v", stream, found, err)
	}
	return cfg
}

func (h *jsHarness) file(sub, name string) pb.Record {
	h.t.Helper()
	r, err := pb.UnmarshalFile(filepath.Join(h.dir, sub, name+".yaml"))
	if err != nil {
		h.t.Fatal(err)
	}
	return r
}

func (h *jsHarness) writeFile(sub, name string, r pb.Record) {
	h.t.Helper()
	if err := pb.MarshalFile(filepath.Join(h.dir, sub, name+".yaml"), r); err != nil {
		h.t.Fatal(err)
	}
}

func (h *jsHarness) seed() {
	h.t.Helper()
	if _, err := h.api.createStream(h.ctx, "ORDERS", map[string]any{
		"name": "ORDERS", "subjects": []string{"orders.>"}, "max_age": int64(time.Hour),
	}); err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.js.CreateKeyValue(h.ctx, jetstream.KeyValueConfig{Bucket: "twin", History: 10}); err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.js.CreateObjectStore(h.ctx, jetstream.ObjectStoreConfig{Bucket: "files"}); err != nil {
		h.t.Fatal(err)
	}
}

func TestJSPullApplyRoundTrip(t *testing.T) {
	h := newJSHarness(t)
	h.seed()
	out := h.pull()
	if !strings.Contains(out, "skipped 1 object store bucket(s): files") || strings.Contains(out, "warning") {
		t.Fatalf("pull said:\n%s", out)
	}
	orders := h.file(jsStreamsDir, "ORDERS")
	if orders["max_age"] != "1h" || orders[jsKeyOrg] != orgA || orders[jsKeyHash] == nil || orders["max_msgs"] != nil {
		t.Fatalf("ORDERS.yaml = %v", orders)
	}
	if twin := h.file(jsKVDir, "twin"); twin["history"] != 10 || twin["bucket"] != "twin" {
		t.Fatalf("twin.yaml = %v", twin)
	}
	if _, err := os.Stat(filepath.Join(h.dir, jsStreamsDir, "KV_twin.yaml")); err == nil {
		t.Fatal("a bucket's stream was pulled as a stream too")
	}

	if out, err := h.apply(jsApplyOptions{}); err != nil || !strings.Contains(out, "created 0  updated 0  unchanged 2  failed 0") {
		t.Fatalf("apply of a fresh pull: %v\n%s", err, out)
	}

	// An edit to each, plus a new stream.
	orders["max_age"] = "2h"
	h.writeFile(jsStreamsDir, "ORDERS", orders)
	twin := h.file(jsKVDir, "twin")
	twin["history"] = 5
	h.writeFile(jsKVDir, "twin", twin)
	h.writeFile(jsStreamsDir, "NEW", pb.Record{"name": "NEW", "subjects": []any{"new.>"}})

	out, err := h.apply(jsApplyOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"would create  streams/NEW.yaml", "would update  streams/ORDERS.yaml (max_age)", "would update  kv/twin.yaml (history)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("dry run lacks %q:\n%s", want, out)
		}
	}
	if h.server("ORDERS")["max_age"] != int64(time.Hour) {
		t.Fatal("dry run changed the server")
	}

	if out, err := h.apply(jsApplyOptions{}); err != nil || !strings.Contains(out, "created 1  updated 2  unchanged 0  failed 0") {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	if got := h.server("ORDERS")["max_age"]; got != int64(2*time.Hour) {
		t.Fatalf("ORDERS max_age = %v", got)
	}
	if got := h.server("KV_twin")["max_msgs_per_subject"]; got != int64(5) {
		t.Fatalf("twin history = %v", got)
	}
	if h.file(jsStreamsDir, "NEW")[jsKeyHash] == nil {
		t.Fatal("create did not record config_hash")
	}
	// Each write recorded the new hash, so the workspace is now in step.
	if out, err := h.apply(jsApplyOptions{}); err != nil || !strings.Contains(out, "unchanged 3") {
		t.Fatalf("second apply: %v\n%s", err, out)
	}
}

func TestJSApplyRefusesStaleAndDeleted(t *testing.T) {
	h := newJSHarness(t)
	h.seed()
	h.pull()

	// Someone else changes ORDERS; our file, pulled before that, edits it too.
	cur := h.server("ORDERS")
	cur["max_age"] = int64(3 * time.Hour)
	if _, err := h.api.updateStream(h.ctx, "ORDERS", cur); err != nil {
		t.Fatal(err)
	}
	orders := h.file(jsStreamsDir, "ORDERS")
	orders["subjects"] = []any{"orders.>", "returns.>"}
	h.writeFile(jsStreamsDir, "ORDERS", orders)

	out, err := h.apply(jsApplyOptions{})
	if err == nil || !strings.Contains(err.Error(), "changed on the server since this file was pulled") {
		t.Fatalf("stale file was not refused: %v\n%s", err, out)
	}
	if h.server("ORDERS")["max_age"] != int64(3*time.Hour) {
		t.Fatal("a refused apply wrote anyway")
	}
	if _, err := h.apply(jsApplyOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	if h.server("ORDERS")["max_age"] != int64(time.Hour) {
		t.Fatal("--force did not apply the file")
	}

	// A pulled stream deleted on the server is not quietly recreated.
	if err := h.js.DeleteStream(h.ctx, "ORDERS"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.apply(jsApplyOptions{}); err == nil || !strings.Contains(err.Error(), "deleted on the server") {
		t.Fatalf("deleted stream was not refused: %v", err)
	}
	if out, err := h.apply(jsApplyOptions{Force: true}); err != nil || !strings.Contains(out, "created 1") {
		t.Fatalf("--force did not recreate: %v\n%s", err, out)
	}
}

func TestJSApplyRefusesBucketsItWouldReset(t *testing.T) {
	h := newJSHarness(t)
	h.seed()
	cur := h.server("KV_twin")
	cur["max_msgs"] = int64(1000)
	if _, err := h.api.updateStream(h.ctx, "KV_twin", cur); err != nil {
		t.Fatal(err)
	}
	if out := h.pull(); !strings.Contains(out, "warning  kv/twin") || !strings.Contains(out, "max_msgs") {
		t.Fatalf("pull did not warn:\n%s", out)
	}
	twin := h.file(jsKVDir, "twin")
	twin["history"] = 3
	h.writeFile(jsKVDir, "twin", twin)
	if _, err := h.apply(jsApplyOptions{}); err == nil || !strings.Contains(err.Error(), "max_msgs") {
		t.Fatalf("an update that resets max_msgs was not refused: %v", err)
	}
}

func TestJSApplyChecksOrganizationAndReportsServerRefusals(t *testing.T) {
	h := newJSHarness(t)
	h.seed()
	h.pull()

	orders := h.file(jsStreamsDir, "ORDERS")
	orders[jsKeyOrg] = "orgB00000000000"
	h.writeFile(jsStreamsDir, "ORDERS", orders)
	if _, err := h.apply(jsApplyOptions{}); err == nil || !strings.Contains(err.Error(), "--any-org") {
		t.Fatalf("a file of another organization was not refused: %v", err)
	}

	// The server will not change storage type. That fails the one file; the
	// other edit in the same apply still lands.
	orders[jsKeyOrg] = orgA
	orders["storage"] = "memory"
	h.writeFile(jsStreamsDir, "ORDERS", orders)
	twin := h.file(jsKVDir, "twin")
	twin["history"] = 4
	h.writeFile(jsKVDir, "twin", twin)
	out, err := h.apply(jsApplyOptions{})
	if err == nil || !strings.Contains(out, "failed   streams/ORDERS.yaml: stream configuration update can not change storage type") ||
		!strings.Contains(out, "updated 1") {
		t.Fatalf("apply: %v\n%s", err, out)
	}
}

// TestKVMappingMatchesNATSGo is the drift guard for kvExpectedStream, which
// copies nats.go's private bucket-to-stream mapping: every bucket nats.go
// creates must read back as fully representable, or pull warns on buckets
// nobody touched and apply refuses them.
func TestKVMappingMatchesNATSGo(t *testing.T) {
	h := newJSHarness(t)
	cfgs := []jetstream.KeyValueConfig{
		{Bucket: "plain"},
		{Bucket: "full", Description: "d", History: 64, TTL: 30 * time.Second, MaxBytes: 1 << 20, MaxValueSize: 1024,
			Storage: jetstream.MemoryStorage, Compression: true, Metadata: map[string]string{"owner": "ops"},
			RePublish: &jetstream.RePublish{Source: ">", Destination: "repub.>"}},
		{Bucket: "markers", TTL: time.Hour, LimitMarkerTTL: time.Minute},
		{Bucket: "copy", Mirror: &jetstream.StreamSource{Name: "plain"}},
		{Bucket: "merged", Sources: []*jetstream.StreamSource{{Name: "plain"}, {Name: "full"}}},
	}
	for _, cfg := range cfgs {
		if _, err := h.js.CreateKeyValue(h.ctx, cfg); err != nil {
			t.Fatalf("%s: %v", cfg.Bucket, err)
		}
		raw := h.server(kvStreamPrefix + cfg.Bucket)
		if unrep, err := kvUnrepresentable(raw); err != nil || len(unrep) != 0 {
			t.Errorf("%s: unrepresentable %v (%v)", cfg.Bucket, unrep, err)
		}
		// And the file pull writes reproduces the bucket: updating from it
		// sends nothing that changes the stream.
		back := kvConfigFromStream(raw)
		if _, err := h.js.UpdateKeyValue(h.ctx, back); err != nil {
			t.Fatalf("%s: update from its own file: %v", cfg.Bucket, err)
		}
		before, _ := jsConfigHash(raw)
		after, _ := jsConfigHash(h.server(kvStreamPrefix + cfg.Bucket))
		if before != after {
			t.Errorf("%s: updating from the pulled config changed the stream", cfg.Bucket)
		}
	}
}

package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/a6n-ai/inscribe"
	"github.com/a6n-ai/inscribe/internal/config"
	"github.com/a6n-ai/inscribe/internal/recall"
)

const defaultServeAddr = "127.0.0.1:7331"

func serveCmd(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := flags.String("addr", defaultServeAddr, "loopback address to listen on")
	if err := flags.Parse(args); err != nil {
		return err
	}
	host, port, err := loopbackPort(*addr)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "serving on http://%s\n", *addr)
	srv := &http.Server{Handler: newServeHandler(config.Home(), host, port), ReadHeaderTimeout: 10 * time.Second}
	return srv.Serve(ln)
}

// loopbackPort checks that addr is exactly 127.0.0.1 or ::1 with a port, and returns
// the host and port. Hostnames and other spellings are refused so the Host allowlist
// matches what the listener binds.
func loopbackPort(addr string) (string, int, error) {
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, fmt.Errorf("--addr %q: %w", addr, err)
	}
	if host == "localhost" {
		return "", 0, errors.New("--addr: use 127.0.0.1 instead of localhost")
	}
	if host != "127.0.0.1" && host != "::1" {
		return "", 0, fmt.Errorf("--addr %q: host must be 127.0.0.1 or ::1", addr)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("--addr %q: invalid port", addr)
	}
	return host, port, nil
}

func newServeHandler(home, host string, port int) http.Handler {
	p := strconv.Itoa(port)
	hosts := map[string]bool{"127.0.0.1:" + p: true, "localhost:" + p: true}
	if host == "::1" {
		hosts["[::1]:"+p] = true
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { serveIndex(w, home) })
	mux.HandleFunc("POST /share", func(w http.ResponseWriter, r *http.Request) { serveShare(w, r, home) })
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'")
		if !hosts[r.Host] {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

type weightRow struct {
	Name  string
	Value float64
}

type indexPage struct {
	Weights              []weightRow
	Seen, Hits, Misses   int
	LabelRule, StepRule  string
	Analytics            bool
	Next, Button         string
	ShareHost, ShareNote string
}

func serveIndex(w http.ResponseWriter, home string) {
	cfg, _, err := config.Load(home, "")
	if err != nil {
		http.Error(w, "config.toml does not load; run inscribe doctor", http.StatusInternalServerError)
		return
	}
	h, err := inscribe.Open(inscribe.Options{Home: home, Caller: "inscribe"})
	if err != nil {
		http.Error(w, "the score store does not open; run inscribe doctor", http.StatusInternalServerError)
		return
	}
	defer func() { _ = h.Close() }()
	wt, err := h.ScoreWeights()
	if err != nil {
		http.Error(w, "the score model does not load", http.StatusInternalServerError)
		return
	}
	page := indexPage{
		Weights: []weightRow{{"text (pinned)", wt.Text}, {"key", wt.Key}, {"body", wt.Body}, {"type", wt.Type},
			{"scope", wt.Scope}, {"agent", wt.Agent}, {"command", wt.Command}, {"trajectory", wt.Trajectory}},
		Seen: wt.Seen, Hits: wt.Hits, Misses: wt.Misses,
		LabelRule: recall.LabelRule, StepRule: recall.StepRule,
		Analytics: cfg.Share.Analytics,
		Next:      "1", Button: "Turn sharing on",
	}
	switch {
	case cfg.Share.URL == "":
		page.ShareHost = "empty"
	case shareURLOK(cfg.Share.URL):
		u, _ := url.Parse(cfg.Share.URL)
		page.ShareHost = u.Hostname()
	default:
		page.ShareHost = "not usable"
	}
	if cfg.Share.Analytics {
		page.Next, page.Button = "0", "Turn sharing off"
		if !shareURLOK(cfg.Share.URL) {
			page.ShareNote = "Sharing is on, but the share URL is not an absolute http or https URL. Nothing is uploaded."
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := indexTmpl.Execute(w, page); err != nil {
		http.Error(w, "the page could not be rendered", http.StatusInternalServerError)
	}
}

func serveShare(w http.ResponseWriter, r *http.Request, home string) {
	if o := r.Header.Get("Origin"); o != "" && o != "http://"+r.Host {
		http.Error(w, "cross-origin change refused", http.StatusForbidden)
		return
	}
	if sf := r.Header.Get("Sec-Fetch-Site"); sf != "" && sf != "same-origin" && sf != "none" {
		http.Error(w, "cross-site change refused", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "the form could not be read", http.StatusBadRequest)
		return
	}
	v := r.PostFormValue("analytics")
	if v != "0" && v != "1" {
		http.Error(w, `analytics must be "0" or "1"`, http.StatusBadRequest)
		return
	}
	if err := setShareAnalytics(home, v == "1"); err != nil {
		var perr toml.ParseError
		if errors.As(err, &perr) {
			http.Error(w, "config.toml does not parse; fix it by hand", http.StatusBadRequest)
			return
		}
		if errors.Is(err, errShareNotTable) {
			http.Error(w, "config.toml has a share key that is not a table; fix it by hand", http.StatusBadRequest)
			return
		}
		http.Error(w, "the config change failed; run inscribe doctor", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

var errShareNotTable = errors.New("share is not a table")

// setShareAnalytics sets share.analytics in the global config and leaves every other
// key as it was. Comments in an existing file are not kept.
func setShareAnalytics(home string, on bool) error {
	raw, err := os.ReadFile(filepath.Join(home, "config.toml")) //nolint:gosec // G304: the config path is under the user's INSCRIBE_HOME
	if errors.Is(err, fs.ErrNotExist) {
		return writeConfigFile(home, fmt.Appendf(nil, "[share]\nanalytics = %t\n", on))
	}
	if err != nil {
		return err
	}
	doc := map[string]any{}
	if _, err := toml.Decode(string(raw), &doc); err != nil {
		return err
	}
	share := map[string]any{}
	if cur, ok := doc["share"]; ok {
		if share, ok = cur.(map[string]any); !ok {
			return errShareNotTable
		}
	}
	share["analytics"] = on
	doc["share"] = share
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(doc); err != nil {
		return err
	}
	return writeConfigFile(home, buf.Bytes())
}

// writeConfigFile replaces config.toml through a temp file in the same directory, so a
// crash never leaves a half-written config.
func writeConfigFile(home string, data []byte) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	path := filepath.Join(home, "config.toml")
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".config.toml.*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	// Some platforms cannot fsync a directory; the rename already happened.
	if d, err := os.Open(dir); err == nil { //nolint:gosec // G304: dir is the parent of the file this function just wrote
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

var indexTmpl = template.Must(template.New("index").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Inscribe score</title>
<style>
body { font-family: system-ui, sans-serif; max-width: 40rem; margin: 2rem auto; padding: 0 1rem; }
table { border-collapse: collapse; }
th, td { padding: 0.2rem 1rem 0.2rem 0; text-align: left; }
</style>
</head>
<body>
<h1>Inscribe score model</h1>
<h2>Weights</h2>
<table>
{{range .Weights}}<tr><th>{{.Name}}</th><td>{{printf "%.3f" .Value}}</td></tr>
{{end}}</table>
<p>seen {{.Seen}}, hits {{.Hits}}, misses {{.Misses}}</p>
<p>label_rule {{.LabelRule}}, step_rule {{.StepRule}}</p>
<h2>Share</h2>
<p>analytics: {{if .Analytics}}on{{else}}off{{end}}</p>
<p>share host: {{.ShareHost}}</p>
{{if .ShareNote}}<p>{{.ShareNote}}</p>{{end}}
<form method="post" action="/share">
<input type="hidden" name="analytics" value="{{.Next}}">
<button type="submit">{{.Button}}</button>
</form>
</body>
</html>
`))

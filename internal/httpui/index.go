package httpui

import (
	"html/template"
	"strings"
)

type indexData struct {
	OwnedSites []string
	Created    string
	Error      string
}

var indexTemplate = template.Must(template.New("index").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>go-zeronet</title>
  <style>
    :root { --bg:#efe7d8; --panel:#fffdf8; --line:#1d1d1d22; --text:#171512; --muted:#6a6259; --accent:#0f6c5c; --danger:#9f2f2f; }
    * { box-sizing: border-box; }
    body { margin:0; font-family: Georgia,"Times New Roman",serif; color:var(--text); background:radial-gradient(circle at top,#faf6ef 0%,#efe7d8 56%,#e6dcc8 100%); }
    main { max-width: 1080px; margin: 0 auto; padding: 40px 20px 72px; }
    h1 { margin:0 0 8px; font-size:48px; }
    p { color:var(--muted); font-size:18px; line-height:1.7; }
    .grid { display:grid; grid-template-columns: repeat(auto-fit, minmax(280px, 1fr)); gap:20px; margin-top:28px; }
    .card { background:rgba(255,253,248,.92); border:1px solid var(--line); border-radius:20px; padding:22px; box-shadow:0 20px 40px rgba(43,31,14,.07); }
    h2 { margin:0 0 16px; font-size:20px; }
    label { display:block; margin:0 0 14px; font-size:14px; color:var(--muted); }
    input, textarea { width:100%; margin-top:6px; padding:12px 14px; border:1px solid var(--line); border-radius:14px; font:inherit; background:#fff; }
    textarea { min-height:110px; resize:vertical; }
    button { border:0; border-radius:999px; padding:12px 18px; background:var(--accent); color:#fff; font:inherit; cursor:pointer; }
    ul { margin:0; padding-left:18px; }
    li { margin:8px 0; }
    a { color:var(--accent); text-decoration:none; }
    .flash { margin-top:20px; padding:14px 16px; border-radius:16px; }
    .ok { background:#e8f4f1; color:#16463d; }
    .err { background:#fdeeee; color:var(--danger); }
    code { background:#f3eee4; border-radius:8px; padding:2px 6px; }
  </style>
</head>
<body>
  <main>
    <h1>go-zeronet</h1>
    <p>Connect to Python ZeroNet peers, open old sites, and now create or clone your own site from the browser.</p>
    {{if .Created}}<div class="flash ok">Site ready: <a href="/{{.Created}}/">/{{.Created}}/</a></div>{{end}}
    {{if .Error}}<div class="flash err">{{.Error}}</div>{{end}}
    <div class="grid">
      <section class="card">
        <h2>Create Site</h2>
        <form method="post" action="/ZeroNet-Internal/site/new">
          <label>Title<input name="title" value="My ZeroNet Site"></label>
          <label>Description<textarea name="description" placeholder="Describe your site"></textarea></label>
          <button type="submit">Create</button>
        </form>
      </section>
      <section class="card">
        <h2>Clone Site</h2>
        <form method="post" action="/ZeroNet-Internal/site/clone">
          <label>Source Address<input name="source" placeholder="1Example..."></label>
          <button type="submit">Clone</button>
        </form>
      </section>
      <section class="card">
        <h2>Owned Sites</h2>
        {{if .OwnedSites}}
        <ul>
          {{range .OwnedSites}}<li><a href="/{{.}}/">{{.}}</a></li>{{end}}
        </ul>
        {{else}}
        <p>No local sites yet. Create one from this page.</p>
        {{end}}
      </section>
    </div>
  </main>
</body>
</html>`))

func renderIndex(data indexData) string {
	var builder strings.Builder
	_ = indexTemplate.Execute(&builder, data)
	return builder.String()
}

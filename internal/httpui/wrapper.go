package httpui

import (
	"html/template"
	"strings"
)

type wrapperData struct {
	Title        string
	SiteAddress  string
	InnerPath    string
	IFrameURL    string
	WrapperKey   string
	WrapperNonce string
	AjaxKey      string
}

var wrapperTemplate = template.Must(template.New("wrapper").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{.Title}}</title>
  <style>
    :root { color-scheme: light; --bg:#f5f1e8; --panel:#fffdf8; --line:#1d1d1d22; --text:#171512; --muted:#6a6259; --accent:#0f6c5c; }
    * { box-sizing:border-box; }
    html, body { margin:0; height:100%; background:linear-gradient(180deg,#f8f4ec 0%,#efe7d8 100%); color:var(--text); font-family:Georgia,"Times New Roman",serif; }
    .shell { display:grid; grid-template-rows:auto 1fr; height:100%; }
    .topbar { display:flex; align-items:center; justify-content:space-between; gap:16px; padding:10px 14px; border-bottom:1px solid var(--line); background:rgba(255,253,248,.92); backdrop-filter: blur(8px); }
    .title { font-size:14px; letter-spacing:.04em; text-transform:uppercase; color:var(--muted); }
    .badge { font-size:12px; padding:4px 8px; border:1px solid var(--line); border-radius:999px; background:#fff; }
    iframe { width:100%; height:100%; border:0; background:white; }
    .toast-wrap { position:fixed; top:58px; right:16px; display:flex; flex-direction:column; gap:8px; z-index:1000; }
    .toast { min-width:220px; max-width:360px; padding:10px 12px; border-radius:12px; background:#111; color:#fff; box-shadow:0 18px 40px rgba(0,0,0,.18); font-size:13px; }
  </style>
</head>
<body>
  <div class="shell">
    <div class="topbar">
      <div class="title">{{.Title}}</div>
      <div class="badge">{{.SiteAddress}}</div>
    </div>
    <iframe id="inner-iframe" sandbox="allow-forms allow-scripts allow-same-origin allow-popups allow-modals allow-downloads allow-pointer-lock allow-presentation" allow="fullscreen; unload" src="{{.IFrameURL}}"></iframe>
  </div>
  <div class="toast-wrap" id="toast-wrap"></div>
  <script>
    window.wrapper_nonce = "{{.WrapperNonce}}";
    window.wrapper_key = "{{.WrapperKey}}";
    window.ajax_key = "{{.AjaxKey}}";
    window.file_inner_path = "{{.InnerPath}}";
    window.address = "{{.SiteAddress}}";

    const iframe = document.getElementById("inner-iframe");
    const callbacks = new Map();
    let nextID = 1;
    let innerReady = false;
    let socket = null;

    function toast(text) {
      const wrap = document.getElementById("toast-wrap");
      const node = document.createElement("div");
      node.className = "toast";
      node.innerHTML = text;
      wrap.appendChild(node);
      setTimeout(() => node.remove(), 5000);
    }

    function sendInner(message) {
      message.wrapper_nonce = window.wrapper_nonce;
      iframe.contentWindow.postMessage(message, "*");
    }

    function wsCmd(cmd, params, cb, forcedID) {
      const id = forcedID || nextID++;
      if (cb) callbacks.set(id, cb);
      socket.send(JSON.stringify({cmd: cmd, params: params || [], id: id}));
      return id;
    }

    function connect() {
      const proto = location.protocol === "https:" ? "wss://" : "ws://";
      socket = new WebSocket(proto + location.host + "/ZeroNet-Internal/Websocket?wrapper_key=" + encodeURIComponent(window.wrapper_key));
      socket.onopen = () => {
        if (innerReady) sendInner({cmd: "wrapperOpenedWebsocket"});
      };
      socket.onclose = () => {
        sendInner({cmd: "wrapperClosedWebsocket"});
        setTimeout(connect, 1500);
      };
      socket.onmessage = (event) => {
        const message = JSON.parse(event.data);
        if (message.cmd === "response" && callbacks.has(message.to)) {
          const cb = callbacks.get(message.to);
          callbacks.delete(message.to);
          cb(message.result);
          return;
        }
        sendInner(message);
      };
    }

    function handleWrapperCommand(message) {
      const cmd = message.cmd;
      if (cmd === "innerReady") {
        innerReady = true;
        if (socket && socket.readyState === 1) sendInner({cmd: "wrapperOpenedWebsocket"});
        return;
      }
      if (cmd === "wrapperNotification") {
        const params = Array.isArray(message.params) ? message.params : [message.params];
        toast(params.slice(1).join(" ") || String(params[0] || ""));
        return;
      }
      if (cmd === "wrapperConfirm") {
        const params = Array.isArray(message.params) ? message.params : [message.params];
        sendInner({cmd: "response", to: message.id, result: window.confirm(params[0] || "")});
        return;
      }
      if (cmd === "wrapperPrompt") {
        const params = Array.isArray(message.params) ? message.params : [message.params];
        sendInner({cmd: "response", to: message.id, result: window.prompt(params[0] || "", params[1] || "")});
        return;
      }
      if (cmd === "wrapperSetTitle") {
        document.title = String(message.params || "{{.Title}}");
        return;
      }
      if (cmd === "wrapperReload") {
        iframe.contentWindow.location.reload();
        return;
      }
      if (cmd === "wrapperGetState") {
        sendInner({cmd: "response", to: message.id, result: window.history.state});
        return;
      }
      if (cmd === "wrapperGetAjaxKey") {
        sendInner({cmd: "response", to: message.id, result: window.ajax_key});
        return;
      }
      if (cmd === "wrapperPushState") {
        const params = Array.isArray(message.params) ? message.params : [];
        history.pushState(params[0], params[1] || "", params[2] || location.href);
        return;
      }
      if (cmd === "wrapperReplaceState") {
        const params = Array.isArray(message.params) ? message.params : [];
        history.replaceState(params[0], params[1] || "", params[2] || location.href);
        return;
      }
      if (cmd === "wrapperOpenWindow") {
        const params = Array.isArray(message.params) ? message.params : [message.params];
        window.open(params[0], "_blank", params[2] || "");
        return;
      }
      if (cmd === "wrapperGetLocalStorage") {
        const value = localStorage.getItem("go-zeronet:" + window.address);
        sendInner({cmd: "response", to: message.id, result: value ? JSON.parse(value) : null});
        return;
      }
      if (cmd === "wrapperSetLocalStorage") {
        localStorage.setItem("go-zeronet:" + window.address, JSON.stringify(message.params || null));
        return;
      }
      if (cmd === "wrapperRequestFullscreen") {
        iframe.requestFullscreen?.();
        return;
      }
      if (socket && socket.readyState === 1) {
        wsCmd(cmd, message.params, null, message.id);
      }
    }

    window.addEventListener("message", (event) => {
      if (event.source !== iframe.contentWindow) return;
      const message = event.data || {};
      if (!message.cmd) return;
      if (message.wrapper_nonce && message.wrapper_nonce !== window.wrapper_nonce) return;
      handleWrapperCommand(message);
    });

    connect();
  </script>
</body>
</html>`))

func renderWrapper(data wrapperData) string {
	var builder strings.Builder
	_ = wrapperTemplate.Execute(&builder, data)
	return strings.ReplaceAll(builder.String(), "&amp;", "&")
}

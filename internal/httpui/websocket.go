package httpui

import (
	"fmt"
	"net/http"
	"strings"
)

type wsMessage struct {
	Cmd    string `json:"cmd"`
	ID     int64  `json:"id,omitempty"`
	To     int64  `json:"to,omitempty"`
	Params any    `json:"params,omitempty"`
	Result any    `json:"result,omitempty"`
}

func (s *Server) handleWebsocket(w http.ResponseWriter, r *http.Request) {
	sess := s.session(r.URL.Query().Get("wrapper_key"))
	if sess == nil {
		http.Error(w, "invalid wrapper_key", http.StatusForbidden)
		return
	}

	wsConn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer wsConn.Close()

	for {
		var message wsMessage
		if err := wsConn.ReadJSON(&message); err != nil {
			return
		}
		if message.Cmd == "response" {
			continue
		}
		result := s.handleWSCommand(sess, message)
		if err := wsConn.WriteJSON(wsMessage{
			Cmd:    "response",
			To:     message.ID,
			Result: result,
		}); err != nil {
			return
		}
	}
}

func (s *Server) handleWSCommand(sess *session, message wsMessage) any {
	switch message.Cmd {
	case "ping":
		return "pong"
	case "serverInfo":
		return map[string]any{
			"version": version,
			"rev":     1,
			"plugins": []string{},
			"debug":   false,
			"offline": false,
			"ui_addr": s.addr,
		}
	case "siteInfo":
		fileStatus := stringParam(message.Params, 0, "file_status")
		return s.manager.SiteInfo(sess.SiteAddress, fileStatus)
	case "serverGetWrapperNonce":
		sess.WrapperNonce = randomToken(12)
		return sess.WrapperNonce
	case "fileGet":
		innerPath := stringParam(message.Params, 0, "inner_path")
		if innerPath == "" {
			return map[string]any{"error": "missing inner_path"}
		}
		format := stringParam(message.Params, 2, "format")
		if format == "" {
			format = "text"
		}
		value, err := s.manager.ReadSiteFile(sess.SiteAddress, cleanInnerPath(innerPath), format)
		if err != nil {
			return nil
		}
		return value
	case "fileNeed":
		innerPath := stringParam(message.Params, 0, "inner_path")
		if err := s.manager.NeedFile(sess.SiteAddress, cleanInnerPath(innerPath)); err != nil {
			return map[string]any{"error": err.Error()}
		}
		return "ok"
	case "fileRules":
		innerPath := stringParam(message.Params, 0, "inner_path")
		return s.manager.FileRules(sess.SiteAddress, cleanInnerPath(innerPath))
	case "siteBadFiles":
		return []string{}
	case "channelJoin":
		return "ok"
	case "fileList", "dirList", "dbQuery", "certSelect", "certSet", "certAdd", "certList":
		return map[string]any{"error": fmt.Sprintf("%s not supported yet", message.Cmd)}
	default:
		return map[string]any{"error": "Unknown command: " + message.Cmd}
	}
}

func stringParam(raw any, index int, key string) string {
	switch val := raw.(type) {
	case []any:
		if index >= 0 && index < len(val) {
			switch item := val[index].(type) {
			case string:
				return item
			case []byte:
				return string(item)
			}
		}
	case map[string]any:
		if item, ok := val[key]; ok {
			switch value := item.(type) {
			case string:
				return value
			case []byte:
				return string(value)
			}
		}
	}
	return ""
}

func cleanInnerPath(innerPath string) string {
	innerPath = strings.TrimPrefix(innerPath, "/")
	if innerPath == "" {
		return "index.html"
	}
	return innerPath
}

package site

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"
)

var relativePathPattern = regexp.MustCompile(`^[^\x00-\x1F"*:<>?\\|]+$`)

// ContentFile 描述 content.json 中的单个文件条目。
type ContentFile struct {
	SHA512 string `json:"sha512"`
	Size   int64  `json:"size"`
}

// ContentJSON 是第一阶段需要用到的 content.json 子集。
type ContentJSON struct {
	Address       string                 `json:"address"`
	InnerPath     string                 `json:"inner_path"`
	Modified      int64                  `json:"modified"`
	Title         string                 `json:"title"`
	Files         map[string]ContentFile `json:"files"`
	FilesOptional map[string]ContentFile `json:"files_optional"`
	Includes      map[string]any         `json:"includes"`
	UserContents  map[string]any         `json:"user_contents"`
}

// ParseContentJSON 解析并做第一阶段最小合法性校验。
func ParseContentJSON(siteAddress, innerPath string, raw []byte) (*ContentJSON, error) {
	var content ContentJSON
	if err := json.Unmarshal(raw, &content); err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", innerPath, err)
	}
	if content.Address != "" && content.Address != siteAddress {
		return nil, fmt.Errorf("content.json 地址不匹配: got=%s want=%s", content.Address, siteAddress)
	}
	if content.InnerPath != "" && content.InnerPath != innerPath {
		return nil, fmt.Errorf("content.json inner_path 不匹配: got=%s want=%s", content.InnerPath, innerPath)
	}
	if content.Modified > time.Now().Unix()+24*60*60 {
		return nil, fmt.Errorf("content.json modified 时间过于超前: %d", content.Modified)
	}
	for relativePath := range content.Files {
		if !isValidRelativePath(relativePath) {
			return nil, fmt.Errorf("非法文件路径: %s", relativePath)
		}
	}
	for relativePath := range content.FilesOptional {
		if !isValidRelativePath(relativePath) {
			return nil, fmt.Errorf("非法 optional 文件路径: %s", relativePath)
		}
	}
	return &content, nil
}

func isValidRelativePath(relativePath string) bool {
	if relativePath == "" {
		return false
	}
	relativePath = strings.ReplaceAll(relativePath, "\\", "/")
	if strings.Contains(relativePath, "../") {
		return false
	}
	if strings.HasPrefix(relativePath, "/") || strings.HasSuffix(relativePath, ".") || strings.HasSuffix(relativePath, " ") {
		return false
	}
	if len(relativePath) > 255 {
		return false
	}
	if !relativePathPattern.MatchString(relativePath) {
		return false
	}
	cleaned := path.Clean(relativePath)
	return cleaned == relativePath
}

package app

import (
	"bookget/config"
	"bookget/pkg/chttp"
	"bookget/pkg/downloader"
	"bookget/pkg/progressbar"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
)

var templateHTMLAttribute = regexp.MustCompile(`(?is)(?:src|data-src|data-original|href|content)\s*=\s*["']([^"']+)["']`)

// downloadTemplateResource handles one generated template URL. Direct media
// is saved as-is; HTML and IIIF JSON are treated as containers and their media
// URLs are downloaded into numbered sibling files.
func (i *ImageDownloader) downloadTemplateResource(rawURL, filePath string, globalBar *progressbar.ProgressBar, totalDownloaded *int64) error {
	if FileExist(filePath) {
		return nil
	}

	body, contentType, err := i.fetchTemplateResource(rawURL)
	if err != nil {
		return err
	}
	if isTemplateBinary(contentType, rawURL) {
		return i.saveTemplateFile(templateOutputPath(filePath, rawURL, contentType), body, globalBar, totalDownloaded)
	}

	if isIIIFInfoDocument(body, rawURL) {
		return i.downloadTemplateIIIFInfo(rawURL, filePath, globalBar, totalDownloaded)
	}

	mediaURLs, err := i.extractTemplateMedia(rawURL, body, contentType)
	if err != nil {
		return err
	}
	if len(mediaURLs) == 0 {
		return fmt.Errorf("模板地址没有找到可下载的图片或 IIIF 内容: %s", rawURL)
	}

	for index, mediaURL := range mediaURLs {
		target := templateMediaPath(filePath, mediaURL, index, len(mediaURLs))
		if err := i.downloadTemplateResource(mediaURL, target, globalBar, totalDownloaded); err != nil {
			return fmt.Errorf("下载模板资源 %s: %w", mediaURL, err)
		}
	}
	return nil
}

func (i *ImageDownloader) fetchTemplateResource(rawURL string) ([]byte, string, error) {
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", config.Conf.UserAgent)
	if cookies, readErr := chttp.ReadCookiesFromFile(config.Conf.CookieFile); readErr == nil && cookies != "" {
		req.Header.Set("Cookie", cookies)
	}
	if headers, readErr := chttp.ReadHeadersFromFile(config.Conf.HeaderFile); readErr == nil {
		for key, value := range headers {
			req.Header.Set(key, value)
		}
	}

	resp, err := i.client.Do(req.WithContext(i.ctx))
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return nil, "", fmt.Errorf("服务器返回错误状态码: %s", resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	contentType := strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	return body, contentType, nil
}

func (i *ImageDownloader) extractTemplateMedia(rawURL string, body []byte, contentType string) ([]string, error) {
	base, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if isTemplateJSON(contentType, rawURL) {
		return extractIIIFMedia(body, base)
	}
	if strings.Contains(contentType, "html") || strings.Contains(strings.ToLower(rawURL), ".html") {
		return i.extractHTMLMedia(body, base)
	}
	return nil, fmt.Errorf("不支持的模板响应类型 %q: %s", contentType, rawURL)
}

func (i *ImageDownloader) extractHTMLMedia(body []byte, base *url.URL) ([]string, error) {
	var mediaURLs []string
	var manifestURLs []string
	seen := make(map[string]bool)
	for _, match := range templateHTMLAttribute.FindAllSubmatch(body, -1) {
		candidate := strings.TrimSpace(string(match[1]))
		if candidate == "" || strings.HasPrefix(candidate, "data:") || strings.HasPrefix(candidate, "#") {
			continue
		}
		resolved := resolveTemplateURL(base, candidate)
		if resolved == "" || seen[resolved] {
			continue
		}
		seen[resolved] = true
		if isTemplateManifestURL(resolved) {
			manifestURLs = append(manifestURLs, resolved)
		} else if isTemplateMediaURL(resolved) {
			mediaURLs = append(mediaURLs, resolved)
		}
	}

	for _, manifestURL := range manifestURLs {
		manifestBody, contentType, err := i.fetchTemplateResource(manifestURL)
		if err != nil {
			return nil, err
		}
		manifestMedia, err := extractIIIFMedia(manifestBody, mustParseURL(manifestURL))
		if err != nil {
			return nil, err
		}
		if len(manifestMedia) == 0 && !isTemplateJSON(contentType, manifestURL) {
			return nil, fmt.Errorf("网页中的 manifest 不是有效的 IIIF JSON: %s", manifestURL)
		}
		mediaURLs = appendUniqueURLs(mediaURLs, manifestMedia...)
	}
	return mediaURLs, nil
}

func (i *ImageDownloader) downloadTemplateIIIFInfo(rawURL, filePath string, globalBar *progressbar.ProgressBar, totalDownloaded *int64) error {
	iiifDownloader := downloader.NewIIIFDownloader(&config.Conf)
	outputPath := templateOutputPath(filePath, rawURL, "image/jpeg")
	if FileExist(outputPath) {
		return nil
	}
	args := []string{"-H", "Referer:" + rawURL}
	if err := iiifDownloader.Dezoomify(i.ctx, rawURL, outputPath, args); err != nil {
		return err
	}
	atomic.AddInt64(totalDownloaded, 1)
	if globalBar != nil {
		globalBar.Add(1)
	}
	return nil
}

func (i *ImageDownloader) saveTemplateFile(filePath string, body []byte, globalBar *progressbar.ProgressBar, totalDownloaded *int64) error {
	if FileExist(filePath) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(filePath, body, 0644); err != nil {
		return err
	}
	atomic.AddInt64(totalDownloaded, 1)
	if globalBar != nil {
		globalBar.Add(1)
	}
	return nil
}

func extractIIIFMedia(body []byte, base *url.URL) ([]string, error) {
	var document interface{}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("解析 IIIF JSON 失败: %w", err)
	}

	var mediaURLs []string
	seen := make(map[string]bool)
	var walk func(interface{}, bool)
	walk = func(value interface{}, inService bool) {
		switch node := value.(type) {
		case []interface{}:
			for _, child := range node {
				walk(child, inService)
			}
		case map[string]interface{}:
			for key, child := range node {
				if key == "service" {
					walk(child, true)
					continue
				}
				if (key == "id" || key == "@id") && inService {
					if raw, ok := child.(string); ok {
						if resolved := normalizeIIIFServiceURL(base, raw); resolved != "" && !seen[resolved] {
							seen[resolved] = true
							mediaURLs = append(mediaURLs, resolved)
						}
					}
				}
				if (key == "id" || key == "@id") && !inService {
					if raw, ok := child.(string); ok {
						if resolved := resolveTemplateURL(base, raw); isTemplateMediaURL(resolved) && !seen[resolved] {
							seen[resolved] = true
							mediaURLs = append(mediaURLs, resolved)
						}
					}
				}
				walk(child, false)
			}
		}
	}
	walk(document, false)
	return mediaURLs, nil
}

func isIIIFInfoDocument(body []byte, rawURL string) bool {
	if !strings.HasSuffix(strings.ToLower(strings.Split(rawURL, "?")[0]), "info.json") {
		return false
	}
	var document map[string]interface{}
	if json.Unmarshal(body, &document) != nil {
		return false
	}
	_, hasWidth := document["width"]
	_, hasHeight := document["height"]
	_, hasID := document["id"]
	if !hasID {
		_, hasID = document["@id"]
	}
	return hasWidth && hasHeight && hasID
}

func isTemplateBinary(contentType, rawURL string) bool {
	if strings.HasPrefix(contentType, "image/") || contentType == "application/pdf" || contentType == "application/octet-stream" {
		return true
	}
	ext := strings.ToLower(filepath.Ext(strings.Split(rawURL, "?")[0]))
	return ext == ".jpg" || ext == ".jpeg" || ext == ".png" || ext == ".gif" || ext == ".webp" || ext == ".tif" || ext == ".tiff" || ext == ".pdf"
}

func isTemplateJSON(contentType, rawURL string) bool {
	return strings.Contains(contentType, "json") || strings.HasSuffix(strings.ToLower(strings.Split(rawURL, "?")[0]), ".json")
}

func isTemplateManifestURL(rawURL string) bool {
	lower := strings.ToLower(rawURL)
	return !strings.HasSuffix(strings.Split(lower, "?")[0], "info.json") && (strings.Contains(lower, "manifest") || strings.HasSuffix(strings.Split(lower, "?")[0], ".json"))
}

func isTemplateMediaURL(rawURL string) bool {
	ext := strings.ToLower(filepath.Ext(strings.Split(rawURL, "?")[0]))
	return ext == ".jpg" || ext == ".jpeg" || ext == ".png" || ext == ".gif" || ext == ".webp" || ext == ".tif" || ext == ".tiff" || ext == ".pdf" || strings.HasSuffix(strings.Split(strings.ToLower(rawURL), "?")[0], "info.json")
}

func normalizeIIIFServiceURL(base *url.URL, raw string) string {
	resolved := resolveTemplateURL(base, raw)
	if resolved == "" {
		return ""
	}
	if strings.HasSuffix(strings.ToLower(strings.Split(resolved, "?")[0]), "info.json") || isTemplateMediaURL(resolved) {
		return resolved
	}
	return strings.TrimRight(resolved, "/") + "/info.json"
}

func resolveTemplateURL(base *url.URL, raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(strings.ReplaceAll(raw, "\\/", "/")))
	if err != nil || parsed.IsAbs() && parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ""
	}
	if base != nil {
		parsed = base.ResolveReference(parsed)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ""
	}
	return parsed.String()
}

func mustParseURL(rawURL string) *url.URL {
	parsed, _ := url.Parse(rawURL)
	return parsed
}

func appendUniqueURLs(dst []string, values ...string) []string {
	seen := make(map[string]bool, len(dst)+len(values))
	for _, value := range dst {
		seen[value] = true
	}
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			dst = append(dst, value)
		}
	}
	return dst
}

func templateOutputPath(filePath, rawURL, contentType string) string {
	ext := strings.ToLower(filepath.Ext(filePath))
	if ext != ".html" && ext != ".htm" && ext != ".json" && ext != "" {
		return filePath
	}
	actualExt := extensionFromContentType(contentType)
	if actualExt == "" {
		actualExt = strings.ToLower(filepath.Ext(strings.Split(rawURL, "?")[0]))
		if actualExt == ".json" || actualExt == ".html" || actualExt == ".htm" {
			actualExt = ""
		}
	}
	if actualExt == "" {
		actualExt = config.Conf.FileExt
	}
	if !strings.HasPrefix(actualExt, ".") {
		actualExt = "." + actualExt
	}
	return strings.TrimSuffix(filePath, filepath.Ext(filePath)) + actualExt
}

func templateMediaPath(filePath, mediaURL string, index, total int) string {
	if total == 1 {
		return filePath
	}
	ext := strings.ToLower(filepath.Ext(strings.Split(mediaURL, "?")[0]))
	if ext == ".json" || ext == ".html" || ext == ".htm" || ext == "" {
		ext = filepath.Ext(filePath)
	}
	if ext == "" || ext == ".json" || ext == ".html" || ext == ".htm" {
		ext = config.Conf.FileExt
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	stem := strings.TrimSuffix(filePath, filepath.Ext(filePath))
	return fmt.Sprintf("%s-%02d%s", stem, index+1, ext)
}

func extensionFromContentType(contentType string) string {
	if contentType == "" {
		return ""
	}
	switch contentType {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/tiff":
		return ".tif"
	case "application/pdf":
		return ".pdf"
	default:
		return ""
	}
}

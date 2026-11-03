package app

import (
	"bookget/config"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// Shidian uses URLs exported automatically or manually by the site's reader.
// The API requires browser-generated signatures; its SDK decrypts picUrl using a ptoken;
// replaying a Cookie alone is insufficient. Never treat previewPages as a book.
type Shidian struct {
	client        *http.Client
	fetchManifest func(string, string) (shidianManifest, error)
}

type shidianPage struct {
	PageNum int    `json:"pageNum"`
	PageID  string `json:"pageId"`
	URL     string `json:"url"`
}

type shidianManifest struct {
	Format     string        `json:"format"`
	BookID     string        `json:"bookId"`
	BookName   string        `json:"bookName"`
	TotalPage  int           `json:"totalPage"`
	Version    int           `json:"version,omitempty"`
	SourceURL  string        `json:"sourceUrl,omitempty"`
	ExportedAt string        `json:"exportedAt,omitempty"`
	Pages      []shidianPage `json:"pages"`
}

var shidianBookPath = regexp.MustCompile(`^/(?:zh|en|zh-Hans|zh-Hant|zh-CN|zh-TW)/book/([A-Za-z0-9_-]+)(?:/|$)|^/book/([A-Za-z0-9_-]+)(?:/|$)`)

func NewShidian() *Shidian {
	timeout := config.Conf.Timeout
	if timeout <= 0 {
		timeout = 300 * time.Second
	} else if timeout < time.Second {
		// The existing configuration stores bare numeric seconds as a Duration.
		timeout *= time.Second
	}
	return &Shidian{client: &http.Client{Timeout: timeout}, fetchManifest: fetchShidianManifest}
}

func (r *Shidian) GetRouterInit(rawURL string) (map[string]interface{}, error) {
	msg, err := r.Run(rawURL)
	return map[string]interface{}{"url": rawURL, "msg": msg}, err
}

func (r *Shidian) Run(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil ||
		(u.Host != "www.shidianguji.com" && u.Host != "shidianguji.com") {
		return "", errors.New("不是有效的识典古籍书籍 URL")
	}
	matches := shidianBookPath.FindStringSubmatch(u.Path)
	if len(matches) == 0 {
		return "", errors.New("识典古籍 URL 应为 /book/<书籍ID> 或 /book/<书籍ID>/chapter/<章节ID>")
	}
	bookID := matches[1] + matches[2]
	manifest, err := r.loadManifest(rawURL, bookID)
	if err != nil {
		return "", err
	}
	pages, err := validateShidianManifest(manifest, bookID)
	if err != nil {
		return "", err
	}
	savePath := filepath.Join(config.Conf.Directory, shidianDirectoryName(manifest.BookName, bookID))
	if err := os.MkdirAll(savePath, 0755); err != nil {
		return "", err
	}
	log.Printf("识典古籍 %s，共 %d 页，保存目录: %s", manifest.BookName, manifest.TotalPage, savePath)
	failed, selected := 0, 0
	var firstErr error
	for i, page := range pages {
		if !config.PageRange(i, manifest.TotalPage) {
			continue
		}
		selected++
		base := filepath.Join(savePath, fmt.Sprintf("%04d", page.PageNum))
		if validShidianImage(base+".png") || validShidianImage(base+".jpg") {
			continue
		}
		log.Printf("Get %d/%d scan", page.PageNum, manifest.TotalPage)
		if err := r.downloadPage(page.URL, rawURL, base); err != nil {
			failed++
			if firstErr == nil {
				firstErr = err
			}
			log.Printf("识典古籍第 %d 页下载失败: %v", page.PageNum, err)
		}
		if config.Conf.Sleep > 0 {
			time.Sleep(time.Duration(config.Conf.Sleep) * time.Second)
		}
	}
	if selected == 0 {
		return "", errors.New("指定页码范围不包含任何扫描页")
	}
	if failed > 0 {
		return "", fmt.Errorf("识典古籍 %d/%d 页下载失败，可重新运行续传: %w", failed, selected, firstErr)
	}
	return savePath, nil
}

func (r *Shidian) loadManifest(rawURL, bookID string) (shidianManifest, error) {
	var manifest shidianManifest
	filename := config.Conf.ShidianPages
	if filename != "" {
		if stat, err := os.Stat(filename); err == nil && stat.IsDir() {
			filename = filepath.Join(filename, bookID+".json")
		}
		data, err := os.ReadFile(filename)
		if err != nil {
			return manifest, fmt.Errorf("读取识典古籍清单: %w", err)
		}
		if err := json.Unmarshal(data, &manifest); err != nil {
			return manifest, fmt.Errorf("解析识典古籍清单: %w", err)
		}
		return manifest, nil
	}
	log.Printf("识典古籍：正在自动获取 %s 的完整扫描页清单", bookID)
	manifest, err := r.fetchManifest(rawURL, bookID)
	if err != nil {
		return manifest, err
	}
	if _, err := validateShidianManifest(manifest, bookID); err != nil {
		return manifest, err
	}
	// Always refresh automatically: cached CDN signatures expire. Keep the last
	// complete manifest for manual reuse, and never replace it with partial data.
	dir := filepath.Join(config.Conf.Directory, ".shidian-pages")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return manifest, err
	}
	f, err := os.CreateTemp(dir, ".manifest-*.json")
	if err != nil {
		return manifest, err
	}
	defer os.Remove(f.Name())
	encodeErr := json.NewEncoder(f).Encode(manifest)
	closeErr := f.Close()
	if encodeErr != nil {
		return manifest, encodeErr
	}
	if closeErr != nil {
		return manifest, closeErr
	}
	filename = filepath.Join(dir, bookID+".json")
	if err := os.Rename(f.Name(), filename); err != nil {
		return manifest, err
	}
	log.Printf("识典古籍：已取得 %d 页，清单保存至 %s", manifest.TotalPage, filename)
	return manifest, nil
}

func validateShidianManifest(m shidianManifest, bookID string) ([]shidianPage, error) {
	if m.Format != "bookget-shidian-v1" {
		return nil, errors.New("不是 bookget 识典古籍清单，请使用 scripts/shidian-export.js 导出")
	}
	if m.BookID != bookID {
		return nil, fmt.Errorf("图像清单书籍 ID %q 与 URL 中的 %q 不符", m.BookID, bookID)
	}
	if m.TotalPage <= 0 || len(m.Pages) != m.TotalPage {
		return nil, fmt.Errorf("图像清单不完整: 应有 %d 页，实际 %d 页；请重新导出", m.TotalPage, len(m.Pages))
	}
	pages := make([]shidianPage, m.TotalPage)
	for _, p := range m.Pages {
		if p.PageNum < 1 || p.PageNum > m.TotalPage || pages[p.PageNum-1].PageNum != 0 {
			return nil, fmt.Errorf("图像清单页码重复或越界: %d", p.PageNum)
		}
		u, err := url.Parse(p.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
			return nil, fmt.Errorf("第 %d 页不是有效图像 URL；接口密文须先由网页解码", p.PageNum)
		}
		pages[p.PageNum-1] = p
	}
	return pages, nil
}

func shidianDirectoryName(title, bookID string) string {
	var name []rune
	for _, c := range strings.TrimSpace(title) {
		if unicode.IsLetter(c) || unicode.IsDigit(c) || c == '-' || c == '_' {
			name = append(name, c)
		} else {
			name = append(name, '_')
		}
		if len(name) == 60 {
			break
		}
	}
	if len(name) == 0 {
		return bookID
	}
	return string(name) + "-" + bookID
}

func validShidianImage(filename string) bool {
	_, err := shidianImageFormat(filename)
	return err == nil
}

func shidianImageFormat(filename string) (string, error) {
	f, err := os.Open(filename)
	if err != nil {
		return "", err
	}
	defer f.Close()
	_, format, err := image.Decode(f)
	if err != nil {
		return "", err
	}
	switch format {
	case "png":
		return ".png", nil
	case "jpeg":
		return ".jpg", nil
	default:
		return "", fmt.Errorf("不支持的图像格式 %s，请重新导出 PNG 清单", format)
	}
}

func (r *Shidian) downloadPage(rawURL, referer, base string) error {
	attempts := config.Conf.Retries
	if attempts < 1 {
		attempts = 1
	}
	var err error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
		err = r.downloadPageOnce(rawURL, referer, base)
		if err == nil {
			return nil
		}
	}
	return err
}

func (r *Shidian) downloadPageOnce(rawURL, referer, base string) error {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return errors.New("无法构造图像请求")
	}
	req.Header.Set("User-Agent", config.Conf.UserAgent)
	req.Header.Set("Referer", referer)
	// Signed CDN URLs need no login credentials. Do not forward a site's -H/-C
	// credentials to a CDN or another host named in a local manifest.
	resp, err := r.client.Do(req)
	if err != nil {
		// url.Error contains the signed URL; keep it out of diagnostic logs.
		var uerr *url.Error
		if errors.As(err, &uerr) {
			return uerr.Err
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("图像服务器返回 HTTP %d；若签名过期，请重新运行自动模式，或在浏览器重新导出手工清单", resp.StatusCode)
	}
	f, err := os.CreateTemp(filepath.Dir(base), ".shidian-*.part")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer os.Remove(temp)
	_, copyErr := io.Copy(f, resp.Body)
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	ext, err := shidianImageFormat(temp)
	if err != nil {
		return fmt.Errorf("响应不是完整的 PNG/JPEG 扫描图: %w", err)
	}
	// Preserve the downloaded bytes without resizing or re-encoding. Only a
	// complete decoded image can replace the final file or be skipped on resume.
	return os.Rename(temp, base+ext)
}

package app

import (
	"bookget/config"
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// Luoyang's PDF.js viewer reads an ordinary, complete PDF per volume.
// Download those files unchanged rather than rasterizing the displayed pages.
type Luoyang struct {
	dt     *DownloadTask
	client *http.Client
}

func NewLuoyang() *Luoyang {
	timeout := config.Conf.Timeout
	if timeout <= 0 {
		timeout = 300 * time.Second
	} else if timeout < time.Second {
		// Legacy configuration stores a bare number of seconds as a Duration.
		timeout *= time.Second
	}
	jar, _ := cookiejar.New(nil)
	return &Luoyang{dt: new(DownloadTask), client: &http.Client{Timeout: timeout, Jar: jar}}
}

func (p *Luoyang) GetRouterInit(rawURL string) (map[string]interface{}, error) {
	msg, err := p.Run(rawURL)
	return map[string]interface{}{"url": rawURL, "msg": msg}, err
}

func (p *Luoyang) Run(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errors.New("无效的洛阳图书馆 URL")
	}
	if config.Conf.Seq != "" || config.Conf.SeqStart > 0 {
		return "", errors.New("洛阳图书馆提供整册 PDF，不支持 -p 按页下载；请用 -v 选择册数")
	}
	p.dt = &DownloadTask{Url: rawURL, UrlParsed: u, BookId: p.getBookId(rawURL)}
	var title string
	var volumes []string
	switch strings.ToLower(path.Base(u.Path)) {
	case "productshow.php":
		if p.dt.BookId == "" {
			return "", errors.New("洛阳图书馆详情页缺少有效的 id")
		}
		resp, err := p.get(rawURL)
		if err != nil {
			return "", fmt.Errorf("读取洛阳图书馆详情页: %w", err)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if err != nil {
			return "", err
		}
		title, volumes, err = parseLuoyangDetail(resp.Request.URL, string(body))
		if err != nil {
			return "", err
		}
	case "viewer.php":
		pdfURL, err := luoyangViewerPDF(u)
		if err != nil {
			return "", err
		}
		title = u.Query().Get("name")
		id := sha256.Sum256([]byte(pdfURL))
		p.dt.BookId = fmt.Sprintf("pdf-%x", id[:8])
		volumes = []string{pdfURL}
	default:
		return "", errors.New("洛阳图书馆支持 productshow.php?id=... 详情页或 viewer.php?pdf=... 阅读页")
	}

	p.dt.Title = title
	p.dt.SavePath = filepath.Join(config.Conf.Directory, luoyangDirectoryName(title, p.dt.BookId))
	if err := os.MkdirAll(p.dt.SavePath, 0755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(p.dt.SavePath, "urls.txt"), []byte(strings.Join(volumes, "\n")+"\n"), 0644); err != nil {
		return "", err
	}
	log.Printf("洛阳图书馆 %s，共 %d 册，保存目录: %s", title, len(volumes), p.dt.SavePath)
	var failures []error
	selected := 0
	for i, pdfURL := range volumes {
		if !config.VolumeRange(i) {
			continue
		}
		selected++
		dest := filepath.Join(p.dt.SavePath, fmt.Sprintf("%04d.pdf", i+1))
		if validLuoyangPDF(dest) {
			log.Printf("跳过已完成的第 %d 册", i+1)
			continue
		}
		log.Printf("下载第 %d/%d 册 PDF", i+1, len(volumes))
		var downloadErr error
		attempts := max(1, config.Conf.Retries+1)
		for attempt := 0; attempt < attempts; attempt++ {
			downloadErr = p.downloadPDF(pdfURL, dest)
			if downloadErr == nil {
				break
			}
			if attempt+1 < attempts {
				log.Printf("第 %d 册下载失败，重试 %d/%d: %v", i+1, attempt+1, attempts-1, downloadErr)
				time.Sleep(time.Second)
			}
		}
		if downloadErr != nil {
			failures = append(failures, fmt.Errorf("第 %d 册: %w", i+1, downloadErr))
		}
		if i+1 < len(volumes) && config.Conf.Sleep > 0 {
			time.Sleep(time.Duration(config.Conf.Sleep) * time.Second)
		}
	}
	if selected == 0 {
		return "", errors.New("指定册数范围不包含任何 PDF")
	}
	if len(failures) > 0 {
		return "", fmt.Errorf("洛阳图书馆 %d/%d 册下载失败，可重新运行补下载: %w", len(failures), selected, errors.Join(failures...))
	}
	return p.dt.SavePath, nil
}

func (p *Luoyang) getBookId(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	id := u.Query().Get("id")
	if !regexp.MustCompile(`^\d+$`).MatchString(id) {
		return ""
	}
	return id
}

var (
	luoyangLinks     = regexp.MustCompile(`(?is)<a\b[^>]*\bhref\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
	luoyangTitle     = regexp.MustCompile(`(?is)<span\b[^>]*\bclass\s*=\s*["']title["'][^>]*>(.*?)</span\s*>`)
	luoyangHTMLTitle = regexp.MustCompile(`(?is)<title\b[^>]*>(.*?)</title\s*>`)
	luoyangTags      = regexp.MustCompile(`<[^>]*>`)
)

func parseLuoyangDetail(base *url.URL, body string) (string, []string, error) {
	var title string
	if m := luoyangTitle.FindStringSubmatch(body); len(m) > 1 {
		title = m[1]
	} else if m := luoyangHTMLTitle.FindStringSubmatch(body); len(m) > 1 {
		title = strings.Split(m[1], " - ")[0]
	}
	title = strings.TrimSpace(html.UnescapeString(luoyangTags.ReplaceAllString(title, "")))
	var volumes []string
	seen := make(map[string]bool)
	for _, match := range luoyangLinks.FindAllStringSubmatch(body, -1) {
		href := html.UnescapeString(match[1] + match[2] + match[3])
		viewer, err := base.Parse(href)
		if err != nil || !strings.EqualFold(path.Base(viewer.Path), "viewer.php") {
			continue
		}
		if viewer.Scheme != base.Scheme || viewer.Host != base.Host {
			return "", nil, errors.New("洛阳图书馆阅读链接不在当前站点")
		}
		pdfURL, err := luoyangViewerPDF(viewer)
		if err != nil {
			return "", nil, err
		}
		if !seen[pdfURL] {
			volumes = append(volumes, pdfURL)
			seen[pdfURL] = true
		}
	}
	if len(volumes) == 0 {
		return "", nil, errors.New("洛阳图书馆详情页未找到 PDF 阅读链接，请确认网页可正常阅读")
	}
	return title, volumes, nil
}

func luoyangViewerPDF(viewer *url.URL) (string, error) {
	pdfPath := viewer.Query().Get("pdf")
	pdfURL, err := viewer.Parse(pdfPath)
	if err != nil || pdfPath == "" || pdfURL.User != nil ||
		pdfURL.Scheme != viewer.Scheme || pdfURL.Host != viewer.Host || !strings.EqualFold(path.Ext(pdfURL.Path), ".pdf") {
		return "", errors.New("洛阳图书馆阅读页缺少有效的本站 PDF 路径")
	}
	pdfURL.Fragment = ""
	return pdfURL.String(), nil
}

func luoyangDirectoryName(title, id string) string {
	var name []rune
	for _, c := range title {
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
		return "洛阳古籍-" + id
	}
	return string(name) + "-" + id
}

func (p *Luoyang) get(rawURL string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Referer", p.dt.Url)
	for key, value := range BuildRequestHeader() {
		req.Header.Set(key, value)
	}
	// A copied browser Range/conditional header must not turn a whole-volume
	// download into a partial file or a bodyless 304 response.
	for _, key := range []string{"Range", "If-Range", "If-None-Match", "If-Modified-Since"} {
		req.Header.Del(key)
	}
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, req.URL.Path)
	}
	return resp, nil
}

// Stream large PDFs to a temporary file. Only promote it after validating the
// transfer length and PDF header/trailer; interrupted transfers cannot masquerade
// as completed downloads on the next run.
func (p *Luoyang) downloadPDF(rawURL, dest string) error {
	resp, err := p.get(rawURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var signature [5]byte
	if _, err := io.ReadFull(resp.Body, signature[:]); err != nil {
		return err
	}
	if string(signature[:]) != "%PDF-" {
		return errors.New("服务器返回的不是 PDF，可能是登录页或错误页")
	}
	f, err := os.CreateTemp(filepath.Dir(dest), ".luoyang-*.part")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if resp.ContentLength > 0 {
		log.Printf("PDF 大小: %.1f MiB", float64(resp.ContentLength)/(1<<20))
	}
	n, err := io.Copy(f, io.MultiReader(bytes.NewReader(signature[:]), resp.Body))
	if err != nil {
		return err
	}
	if resp.ContentLength >= 0 && n != resp.ContentLength {
		return fmt.Errorf("PDF 不完整: 收到 %d 字节，预期 %d", n, resp.ContentLength)
	}
	if err := f.Close(); err != nil {
		return err
	}
	if !validLuoyangPDF(f.Name()) {
		return errors.New("PDF 缺少完整的文件头或结束标记")
	}
	return os.Rename(f.Name(), dest)
}

func validLuoyangPDF(filename string) bool {
	f, err := os.Open(filename)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 10 {
		return false
	}
	var signature [5]byte
	if _, err := io.ReadFull(f, signature[:]); err != nil || string(signature[:]) != "%PDF-" {
		return false
	}
	if _, err := f.Seek(max(0, info.Size()-1024), io.SeekStart); err != nil {
		return false
	}
	tail, err := io.ReadAll(f)
	return err == nil && bytes.HasSuffix(bytes.TrimSpace(tail), []byte("%%EOF"))
}

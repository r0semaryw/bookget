package app

import (
	"bookget/config"
	"bookget/model/sdlib"
	"bookget/pkg/chttp"
	"bookget/pkg/downloader"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
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
)

type Sdlib struct {
	dm     *downloader.DownloadManager
	ctx    context.Context
	cancel context.CancelFunc
	client *http.Client

	bufBuilder strings.Builder
	bufString  string
	bufBody    []byte
	canvases   []string
	urlsFile   string

	rawUrl    string
	parsedUrl *url.URL
	savePath  string
	bookId    string
}

func NewSdlib() *Sdlib {
	ctx, cancel := context.WithCancel(context.Background())
	dm := downloader.NewDownloadManager(ctx, cancel, config.Conf.MaxConcurrent)

	// 创建自定义 Transport 忽略 SSL 验证
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
	}
	jar, _ := cookiejar.New(nil)
	return &Sdlib{
		// 初始化字段
		dm:     dm,
		client: &http.Client{Timeout: config.Conf.Timeout * time.Second, Jar: jar, Transport: tr},
		ctx:    ctx,
		cancel: cancel,
	}
}

func (r *Sdlib) GetRouterInit(rawUrl string) (map[string]interface{}, error) {
	r.rawUrl = rawUrl
	var err error
	r.parsedUrl, err = url.Parse(rawUrl)
	if err != nil {
		return nil, err
	}
	err = r.Run()
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"type": "",
		"url":  rawUrl,
	}, nil
}

func (r *Sdlib) getBookId(rawUrl string) (bookId string) {
	if r.parsedUrl == nil {
		return ""
	}
	if id := r.parsedUrl.Query().Get("resId"); id != "" {
		return id
	}
	// The .cn reader is a hash route, so the book id is in URL.Fragment,
	// not in URL.Query (for example #/reader/150113020240030?treeId=...).
	fragment := r.parsedUrl.Fragment
	if m := regexp.MustCompile(`(?i)(?:^|/)reader/([A-Za-z0-9_-]+)(?:$|[/?])`).FindStringSubmatch(fragment); len(m) > 1 {
		return m[1]
	}
	return ""
}

func (r *Sdlib) Run() (err error) {
	r.bookId = r.getBookId(r.rawUrl)
	if r.bookId == "" {
		return fmt.Errorf("山东省古籍平台 URL 中未找到书籍 ID")
	}
	if r.isCNReader() {
		return r.runCNReader()
	}
	r.savePath = config.Conf.Directory
	r.urlsFile = path.Join(r.savePath, "urls.txt")

	r.canvases, err = r.getCanvases(r.rawUrl)
	if err != nil || len(r.canvases) == 0 {
		return err
	}

	err = os.WriteFile(r.urlsFile, []byte(r.bufBuilder.String()), os.ModePerm)
	if err != nil {
		return err
	}

	if err := r.do(r.canvases); err != nil {
		return err
	}

	return nil
}

func (r *Sdlib) isCNReader() bool {
	if r.parsedUrl == nil {
		return false
	}
	host := strings.ToLower(r.parsedUrl.Hostname())
	return host == "guji.sdlib.cn" || host == "www.guji.sdlib.cn"
}

type sdlibCNBookResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		Name string `json:"name"`
	} `json:"data"`
}

type sdlibCNPageResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		PageNo        int    `json:"pageNo"`
		FileID        string `json:"fileId"`
		FileExtension string `json:"fileExtension"`
	} `json:"data"`
}

// runCNReader downloads the original page files exposed by the current
// guji.sdlib.cn reader.  The reader's hash route is only a UI route; the
// stable API is getContentFileTotal -> getReadPage -> getEncryptedPdf.
func (r *Sdlib) runCNReader() error {
	base := r.apiBaseURL()
	bookBody, err := r.getBody(base + "/ancientbooks/front/getResBookById/" + url.PathEscape(r.bookId))
	if err != nil {
		return fmt.Errorf("山东古籍书目信息: %w", err)
	}
	var book sdlibCNBookResponse
	if err := json.Unmarshal(bookBody, &book); err != nil {
		return fmt.Errorf("山东古籍书目信息解析: %w", err)
	}
	if book.Code != 0 && book.Code != http.StatusOK {
		return fmt.Errorf("山东古籍书目信息: %s (code %d)", book.Msg, book.Code)
	}

	totalBody, err := r.getBody(base + "/ancientbooks/front/getContentFileTotal/3/" + url.PathEscape(r.bookId))
	if err != nil {
		return fmt.Errorf("山东古籍页数: %w", err)
	}
	var totalResp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data int    `json:"data"`
	}
	if err := json.Unmarshal(totalBody, &totalResp); err != nil {
		return fmt.Errorf("山东古籍页数解析: %w", err)
	}
	if totalResp.Data <= 0 {
		return fmt.Errorf("山东古籍没有可下载页面: %s", totalResp.Msg)
	}

	r.savePath = config.Conf.Directory
	if err := os.MkdirAll(r.savePath, 0755); err != nil {
		return err
	}
	var urls strings.Builder
	title := strings.TrimSpace(book.Data.Name)
	if title != "" {
		log.Printf(" %s, %d pages\n", title, totalResp.Data)
	} else {
		log.Printf(" %d pages\n", totalResp.Data)
	}
	for page := 1; page <= totalResp.Data; page++ {
		// PageRange uses zero-based indexes and an exclusive end, matching the
		// other Bookget adapters.
		if !config.PageRange(page-1, totalResp.Data) {
			continue
		}
		pageURL := fmt.Sprintf("%s/ancientbooks/front/getReadPage/3/%s/%d?withText=false", base, url.PathEscape(r.bookId), page)
		pageBody, err := r.getBody(pageURL)
		if err != nil {
			return fmt.Errorf("山东古籍第 %d 页信息: %w", page, err)
		}
		var pageResp sdlibCNPageResponse
		if err := json.Unmarshal(pageBody, &pageResp); err != nil {
			return fmt.Errorf("山东古籍第 %d 页信息解析: %w", page, err)
		}
		if pageResp.Code != 0 && pageResp.Code != http.StatusOK {
			return fmt.Errorf("山东古籍第 %d 页: %s (code %d)", page, pageResp.Msg, pageResp.Code)
		}
		if pageResp.Data.FileID == "" {
			return fmt.Errorf("山东古籍第 %d 页没有 fileId", page)
		}
		pdfURL := base + "/ancientbooks/front/getEncryptedPdf/" + url.PathEscape(pageResp.Data.FileID)
		urls.WriteString(pdfURL)
		urls.WriteByte('\n')
		filename := fmt.Sprintf("%04d.pdf", page)
		dest := filepath.Join(r.savePath, filename)
		if FileExist(dest) {
			continue
		}
		body, err := r.getBody(pdfURL)
		if err != nil {
			return fmt.Errorf("山东古籍第 %d 页文件: %w", page, err)
		}
		if len(body) < 5 || string(body[:5]) != "%PDF-" {
			return fmt.Errorf("山东古籍第 %d 页返回的不是 PDF", page)
		}
		if err := writeDownloadFile(dest, body); err != nil {
			return fmt.Errorf("保存山东古籍第 %d 页: %w", page, err)
		}
		log.Printf("Get %d/%d page\n", page, totalResp.Data)
	}
	if err := os.WriteFile(filepath.Join(r.savePath, "urls.txt"), []byte(urls.String()), 0644); err != nil {
		return err
	}
	return nil
}

func (r *Sdlib) apiBaseURL() string {
	scheme := "https"
	if r.parsedUrl != nil && r.parsedUrl.Scheme != "" {
		scheme = r.parsedUrl.Scheme
	}
	return scheme + "://" + r.parsedUrl.Host + "/dev-api"
}

func writeDownloadFile(dest string, body []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".bookget-*.part")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, dest)
}

func (r *Sdlib) do(canvases []string) (err error) {
	sizeVol := len(canvases)
	if sizeVol <= 0 {
		return err
	}

	counter := 0
	headers := BuildRequestHeader()
	for i, imgUrl := range canvases {
		i++
		sortId := fmt.Sprintf("%04d", i)
		fileName := sortId + filepath.Ext(imgUrl)

		if imgUrl == "" || !config.PageRange(i, sizeVol) {
			continue
		}
		//跳过存在的文件
		if FileExist(path.Join(r.savePath, fileName)) {
			continue
		}
		// 添加GET下载任务
		r.dm.AddTask(
			imgUrl,
			"GET",
			headers,
			nil,
			r.savePath,
			fileName,
			config.Conf.Threads,
		)
		counter++
	}
	fmt.Println()
	r.dm.SetBar(counter)
	r.dm.Start()
	return nil
}

func (r *Sdlib) getVolumes(rawUrl string) (volumes []string, err error) {
	//TODO implement me
	panic("implement me")
}

func (r *Sdlib) getCanvases(rawUrl string) (canvases []string, err error) {
	apiUrl := fmt.Sprintf("https://%s/dev-api/ancientbooks/front/getFileContentPage/3/", r.parsedUrl.Host)
	r.bufBody, err = r.getBody(apiUrl)
	if err != nil {
		return nil, err
	}
	resp := sdlib.Response{}
	if err = json.Unmarshal(r.bufBody, &resp); err != nil {
		return nil, err
	}
	r.canvases = make([]string, 0, len(resp.Data))
	for _, d := range resp.Data {
		r.bufBuilder.WriteString(d.Url)
		r.bufBuilder.WriteString("\n")
		r.canvases = append(r.canvases, d.Url)
	}
	return r.canvases, err
}

func (r *Sdlib) getBody(rawUrl string) ([]byte, error) {
	req, err := http.NewRequest("GET", rawUrl, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", config.Conf.UserAgent)
	if r.rawUrl != "" {
		req.Header.Set("Referer", r.rawUrl)
	}
	cookies, _ := chttp.ReadCookiesFromFile(config.Conf.CookieFile)
	if cookies != "" {
		req.Header.Set("Cookie", cookies)
	}
	headers, err := chttp.ReadHeadersFromFile(config.Conf.HeaderFile)
	if err == nil {
		for key, value := range headers {
			req.Header.Set(key, value)
		}
	}
	resp, err := r.client.Do(req.WithContext(r.ctx))
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("close body err=%v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		err = fmt.Errorf("服务器返回错误状态码: %d", resp.StatusCode)
		return nil, err
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return body, nil
}

func (r *Sdlib) postBody(rawUrl string, postData interface{}) ([]byte, error) {
	//TODO implement me
	panic("implement me")
}

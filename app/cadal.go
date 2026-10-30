package app

import (
	"bookget/config"
	"bookget/pkg/gohttp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Cadan is the reader used by cadal.edu.cn.  CADAL does not expose a stable
// image URL in the reader page: the page first obtains a signed image prefix
// from /reader/book/info and then appends a base64 encoded page name.
type Cadan struct {
	dt  *DownloadTask
	jar *cookiejar.Jar
}

func NewCadan() *Cadan {
	return &Cadan{dt: new(DownloadTask)}
}

func (r *Cadan) GetRouterInit(sURL string) (map[string]interface{}, error) {
	msg, err := r.Run(sURL)
	return map[string]interface{}{"url": sURL, "msg": msg}, err
}

func (r *Cadan) Run(sURL string) (string, error) {
	parsed, err := url.Parse(sURL)
	if err != nil {
		return "", err
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("invalid CADAL URL: %s", sURL)
	}
	ssno := parsed.Query().Get("ssno")
	if ssno == "" {
		return "", errors.New("CADAL URL does not contain ssno")
	}
	r.dt = &DownloadTask{Url: sURL, UrlParsed: parsed, BookId: ssno}
	r.jar, err = cookiejar.New(nil)
	if err != nil {
		return "", err
	}
	return r.download()
}

type cadalAddrResponse struct {
	Success bool              `json:"success"`
	URL     string            `json:"url"`
	Params  map[string]string `json:"params"`
}

// cadalBookInfo is the response returned by the JSONP endpoint selected by
// the CADAL address service.  The service currently returns strings for all
// fields, so keeping them as strings also handles older deployments.
type cadalBookInfo struct {
	Msg            string `json:"msg"`
	ImgPath        string `json:"imgpath"`
	Title          string `json:"title"`
	OldReadPage    string `json:"oldReadPage"`
	Pages          string `json:"pages"`
	ImgAddr        string `json:"imgaddr"`
	AllowPages     string `json:"allowpages"`
	SSNO           string `json:"ssno"`
	AllowDirBorrow string `json:"allowDirBorrowTime"`
	IfBorrow       string `json:"ifBorrow"`
	ReadPermission string `json:"read_permission"`
	AllowBorrow    string `json:"allowBorrowTime"`
	DataSources    string `json:"dataSources"`
	Username       string `json:"username"`
	BookType       string `json:"bookType"`
	BookID         string `json:"bookid"`
	LibraryCode    string `json:"librarycode"`
}

type cadalImageInfo struct {
	Success  bool `json:"success"`
	PathInfo struct {
		EndPage   int    `json:"endPage"`
		StartPage int    `json:"startPage"`
		JPGURL    string `json:"jpgUrl"`
		PDGPath   string `json:"pdgPath"`
		SMarkA    string `json:"sMarkA"`
	} `json:"pathInfo"`
	BookID      string `json:"bookid"`
	LibraryCode string `json:"librarycode"`
}

func (r *Cadan) download() (string, error) {
	log.Printf("Get %s\n", r.dt.Url)

	// The initial GET establishes the CADAL reader session and lets a cookie
	// file supplied with -C participate in the same cookie jar as Set-Cookie.
	if _, err := r.request(httpMethodGet, r.dt.Url, nil); err != nil {
		return "", fmt.Errorf("open CADAL reader: %w", err)
	}

	book, err := r.getBookInfo()
	if err != nil {
		return "", err
	}
	imageInfo, err := r.getImageInfo(book)
	if err != nil {
		return "", err
	}

	pageCount := parsePositiveInt(book.Pages)
	if imageInfo.PathInfo.EndPage > 0 && (pageCount == 0 || imageInfo.PathInfo.EndPage < pageCount) {
		pageCount = imageInfo.PathInfo.EndPage
	}
	allowed := parsePositiveInt(book.AllowPages)
	if allowed > 0 && (pageCount == 0 || allowed < pageCount) {
		pageCount = allowed
	}
	if pageCount <= 0 {
		return "", errors.New("CADAL returned no readable pages")
	}
	if imageInfo.PathInfo.JPGURL == "" {
		return "", errors.New("CADAL returned no image path")
	}

	if book.Title != "" {
		log.Printf(" %s, %d pages\n", book.Title, pageCount)
	} else {
		log.Printf(" %d pages\n", pageCount)
	}
	r.dt.SavePath = config.Conf.Directory
	if err := os.MkdirAll(r.dt.SavePath, 0755); err != nil {
		return "", err
	}

	bookType := book.BookType
	if bookType == "" {
		bookType = r.dt.UrlParsed.Query().Get("bookType")
	}
	if bookType == "" {
		bookType = "2"
	}
	bookID := imageInfo.BookID
	if bookID == "" {
		bookID = book.BookID
	}
	libraryCode := imageInfo.LibraryCode
	if libraryCode == "" {
		libraryCode = book.LibraryCode
	}
	jpgPath := imageInfo.PathInfo.JPGURL
	// The reader builds the final prefix as jpgUrl + dataSources + "/".
	// Omitting this segment produces a valid-looking URL that CADAL answers
	// with 404 (for example, .../<signed-prefix>/1/<base64-page>).
	if dataSources := strings.Trim(book.DataSources, "/"); dataSources != "" {
		jpgPath = strings.TrimRight(jpgPath, "/") + "/" + dataSources + "/"
	}
	return "", r.downloadPages(jpgPath, bookType, bookID, libraryCode, pageCount)
}

func (r *Cadan) getBookInfo() (cadalBookInfo, error) {
	ssno := r.dt.BookId
	addr, err := r.postForm(r.readerURL("reader/cadal/api/addr"), url.Values{
		"ssno":    {ssno},
		"apiType": {"bookInfo"},
	})
	if err != nil {
		return cadalBookInfo{}, fmt.Errorf("CADAL bookInfo address: %w", err)
	}
	var addrResp cadalAddrResponse
	if err := decodeCADALJSON(addr, &addrResp); err != nil {
		return cadalBookInfo{}, fmt.Errorf("decode CADAL bookInfo address: %w", err)
	}
	if addrResp.URL == "" {
		return cadalBookInfo{}, errors.New("CADAL bookInfo address is empty")
	}
	endpoint := normalizeCADALURLForBase(addrResp.URL, r.dt.UrlParsed)
	params := url.Values{}
	for key, value := range addrResp.Params {
		params.Set(key, value)
	}
	// CADAL returns JSONP from this endpoint.  The callback is harmless on
	// deployments that return plain JSON and makes the request match the
	// browser reader.
	endpointURL, err := url.Parse(endpoint)
	if err != nil {
		return cadalBookInfo{}, fmt.Errorf("CADAL bookInfo address URL: %w", err)
	}
	query := endpointURL.Query()
	query.Set("callback", "bookgetCADAL")
	endpointURL.RawQuery = query.Encode()
	body, err := r.postForm(endpointURL.String(), params)
	if err != nil {
		return cadalBookInfo{}, fmt.Errorf("CADAL bookInfo: %w", err)
	}
	var info cadalBookInfo
	if err := decodeCADALJSON(body, &info); err != nil {
		return cadalBookInfo{}, fmt.Errorf("decode CADAL bookInfo: %w", err)
	}
	if info.SSNO == "" {
		info.SSNO = ssno
	}
	return info, nil
}

func (r *Cadan) getImageInfo(book cadalBookInfo) (cadalImageInfo, error) {
	bookType := book.BookType
	if bookType == "" {
		bookType = r.dt.UrlParsed.Query().Get("bookType")
	}
	values := url.Values{}
	values.Set("msg", book.Msg)
	values.Set("imgpath", book.ImgPath)
	values.Set("title", book.Title)
	values.Set("oldReadPage", book.OldReadPage)
	values.Set("pages", book.Pages)
	values.Set("imgaddr", book.ImgAddr)
	values.Set("allowpages", book.AllowPages)
	values.Set("ssno", book.SSNO)
	values.Set("allowDirBorrowTime", book.AllowDirBorrow)
	values.Set("ifBorrow", book.IfBorrow)
	values.Set("read_permission", book.ReadPermission)
	values.Set("allowBorrowTime", book.AllowBorrow)
	values.Set("dataSources", book.DataSources)
	values.Set("username", book.Username)
	values.Set("bookType", bookType)
	values.Set("ipinside", r.dt.UrlParsed.Query().Get("ipinside"))
	body, err := r.postForm(r.readerURL("reader/book/info"), values)
	if err != nil {
		return cadalImageInfo{}, fmt.Errorf("CADAL image info: %w", err)
	}
	var info cadalImageInfo
	if err := decodeCADALJSON(body, &info); err != nil {
		return cadalImageInfo{}, fmt.Errorf("decode CADAL image info: %w", err)
	}
	return info, nil
}

func (r *Cadan) downloadPages(jpgPath, bookType, bookID, libraryCode string, pageCount int) error {
	ctx := context.Background()
	failed := 0
	attempted := 0
	for page := 1; page <= pageCount; page++ {
		if !config.PageRange(page-1, pageCount) {
			continue
		}
		dest := filepath.Join(r.dt.SavePath, fmt.Sprintf("%04d.jpg", page))
		if FileExist(dest) {
			continue
		}
		attempted++
		imageURL := cadalPageImageURL(r.dt.UrlParsed, jpgPath, page, bookType, bookID, libraryCode)
		log.Printf("Get %d/%d  %s\n", page, pageCount, imageURL)
		opts := gohttp.Options{
			DestFile:    dest,
			Overwrite:   false,
			Concurrency: 1,
			CookieFile:  config.Conf.CookieFile,
			HeaderFile:  config.Conf.HeaderFile,
			CookieJar:   r.jar,
			Headers: map[string]interface{}{
				"User-Agent": config.Conf.UserAgent,
				"Referer":    r.dt.Url,
				"Origin":     r.dt.UrlParsed.Scheme + "://" + r.dt.UrlParsed.Host,
			},
		}
		resp, err := gohttp.FastGet(ctx, imageURL, opts)
		if err != nil {
			log.Printf("CADAL page %d failed: %v\n", page, err)
			failed++
			continue
		}
		if resp == nil || resp.GetStatusCode() < 200 || resp.GetStatusCode() >= 300 {
			failed++
			if resp == nil {
				log.Printf("CADAL page %d failed: empty response\n", page)
			} else {
				log.Printf("CADAL page %d failed: HTTP %d\n", page, resp.GetStatusCode())
			}
			continue
		}
		contentType := strings.ToLower(resp.GetHeaderLine("Content-Type"))
		if contentType != "" && !strings.HasPrefix(contentType, "image/") && contentType != "application/octet-stream" {
			failed++
			_ = os.Remove(dest)
			log.Printf("CADAL page %d failed: unexpected content type %q\n", page, contentType)
		}
	}
	if failed > 0 {
		return fmt.Errorf("CADAL page download failed: %d/%d pages", failed, attempted)
	}
	return nil
}

func (r *Cadan) readerURL(resource string) string {
	return r.dt.UrlParsed.Scheme + "://" + r.dt.UrlParsed.Host + "/cx/reader/" + strings.TrimLeft(resource, "/")
}

func (r *Cadan) request(method, rawURL string, body []byte) ([]byte, error) {
	opts := gohttp.Options{
		CookieFile: config.Conf.CookieFile,
		HeaderFile: config.Conf.HeaderFile,
		CookieJar:  r.jar,
		Headers: map[string]interface{}{
			"User-Agent": config.Conf.UserAgent,
			"Referer":    r.dt.Url,
		},
		Body: body,
	}
	if method == httpMethodPost {
		opts.Headers["Content-Type"] = "application/x-www-form-urlencoded; charset=UTF-8"
		opts.Headers["X-Requested-With"] = "XMLHttpRequest"
	}
	var resp *gohttp.Response
	var err error
	if method == httpMethodPost {
		resp, err = gohttp.NewClient(context.Background(), opts).Post(rawURL)
	} else {
		resp, err = gohttp.NewClient(context.Background(), opts).Get(rawURL)
	}
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.GetStatusCode() < 200 || resp.GetStatusCode() >= 300 {
		if resp == nil {
			return nil, errors.New("empty response")
		}
		return nil, fmt.Errorf("HTTP %d", resp.GetStatusCode())
	}
	bs, err := resp.GetBody()
	return []byte(bs), err
}

func (r *Cadan) postForm(rawURL string, values url.Values) ([]byte, error) {
	return r.request(httpMethodPost, rawURL, []byte(values.Encode()))
}

const (
	httpMethodGet  = "GET"
	httpMethodPost = "POST"
)

func normalizeCADALURL(raw string) string {
	return normalizeCADALURLForBase(raw, nil)
}

func normalizeCADALURLForBase(raw string, base *url.URL) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if parsed, err := url.Parse(raw); err == nil {
		if parsed.IsAbs() {
			return parsed.String()
		}
		if strings.HasPrefix(raw, "//") {
			scheme := "https"
			if base != nil && base.Scheme != "" {
				scheme = base.Scheme
			}
			parsed.Scheme = scheme
			return parsed.String()
		}
		if base != nil {
			return base.ResolveReference(parsed).String()
		}
	}
	if strings.HasPrefix(raw, "//") {
		return "https:" + raw
	}
	if strings.HasPrefix(raw, "/") {
		return "https://cadal.edu.cn" + raw
	}
	return raw
}

// decodeCADALJSON accepts both plain JSON and the JSONP wrapper emitted by
// the legacy CADAL endpoints.
func decodeCADALJSON(body []byte, target interface{}) error {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return errors.New("empty response")
	}
	if strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[") {
		return json.Unmarshal([]byte(text), target)
	}
	start := strings.IndexAny(text, "{[")
	if start < 0 {
		return errors.New("JSON payload not found")
	}
	end := strings.LastIndexAny(text, "}]")
	if end < start {
		return errors.New("JSON payload is incomplete")
	}
	return json.Unmarshal([]byte(text[start:end+1]), target)
}

func parsePositiveInt(value string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(value))
	if n < 0 {
		return 0
	}
	return n
}

func cadalPageImageURL(pageBase *url.URL, jpgPath string, page int, bookType, bookID, libraryCode string) string {
	base := strings.TrimSpace(jpgPath)
	if strings.HasPrefix(base, "//") {
		base = pageBase.Scheme + ":" + base
	} else if strings.HasPrefix(base, "/") {
		base = pageBase.Scheme + "://" + pageBase.Host + base
	} else if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		base = pageBase.Scheme + "://" + pageBase.Host + "/cx/reader/" + strings.TrimLeft(base, "/")
	}
	base = strings.TrimRight(base, "/") + "/"
	pageName := fmt.Sprintf("%08d.jpg", page)
	encoded := base64.StdEncoding.EncodeToString([]byte(pageName))
	query := url.Values{}
	query.Set("zoom", "0")
	query.Set("librarycode", bookTypeOrUndefined(libraryCode))
	query.Set("bookid", bookTypeOrUndefined(bookID))
	query.Set("bookType", bookTypeOrUndefined(bookType))
	query.Set("_p", pageBase.Scheme+":")
	query.Set("ssno", pageBase.Query().Get("ssno"))
	query.Set("page", strconv.Itoa(page))
	return base + encoded + "?" + query.Encode()
}

func bookTypeOrUndefined(value string) string {
	if strings.TrimSpace(value) == "" {
		return "undefined"
	}
	return value
}

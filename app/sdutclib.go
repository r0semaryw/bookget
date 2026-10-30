package app

import (
	"bookget/config"
	"bookget/pkg/chttp"
	"bookget/pkg/gohttp"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const (
	sduTclibAESKey = "MSQLHKJ1007SDong"
	sduTclibAESIV  = "SHANDONMSQ1007lh"
)

// SduTclib downloads image pages from the Shandong University special
// collections library (tclib.sdu.edu.cn).
type SduTclib struct {
	dt  *DownloadTask
	jar *cookiejar.Jar
}

func NewSduTclib() *SduTclib {
	return &SduTclib{dt: new(DownloadTask)}
}

type sduTclibDetailResponse struct {
	Status int              `json:"status"`
	Data   []sduTclibDetail `json:"data"`
}

type sduTclibDetail struct {
	Attr  string `json:"attr"`
	Value string `json:"value"`
}

type sduTclibViewResponse struct {
	Code         int    `json:"code"`
	Status       int    `json:"status"`
	ErrorMessage string `json:"errorMessage"`
	Data         struct {
		Title      string   `json:"title"`
		ServerBase string   `json:"serverBase"`
		Images     []string `json:"images"`
	} `json:"data"`
}

func (r *SduTclib) GetRouterInit(sURL string) (map[string]interface{}, error) {
	msg, err := r.Run(sURL)
	return map[string]interface{}{"url": sURL, "msg": msg}, err
}

func (r *SduTclib) Run(sURL string) (string, error) {
	parsed, err := url.Parse(sURL)
	if err != nil {
		return "", err
	}
	bookID := parsed.Query().Get("id")
	if bookID == "" {
		return "", errors.New("山东大学图书馆 URL 不包含 id")
	}
	r.dt = &DownloadTask{Url: sURL, UrlParsed: parsed, BookId: bookID}
	r.jar, err = cookiejar.New(nil)
	if err != nil {
		return "", err
	}

	if _, err := r.get(sURL); err != nil {
		return "", fmt.Errorf("打开山东大学图书页面: %w", err)
	}
	userID := sduTclibCookieValue("userId")
	if userID == "" {
		userID = sduTclibCookieValue("userid")
	}
	if userID == "" {
		return "", errors.New("山东大学图书馆需要登录 Cookie，未找到 userId")
	}

	detail, err := r.getDetail(bookID)
	if err != nil {
		return "", err
	}
	view, err := r.getView(bookID, userID)
	if err != nil {
		return "", err
	}
	if view.Data.ServerBase == "" || len(view.Data.Images) == 0 {
		message := view.ErrorMessage
		if message == "" {
			message = "返回数据中没有图像地址"
		}
		return "", fmt.Errorf("山东大学图书馆无法获取图像: %s", message)
	}

	title := view.Data.Title
	if title == "" {
		title = sduTclibDetailValue(detail.Data, "題名卷數", "题名卷数", "題名", "题名")
	}
	if title != "" {
		log.Printf(" %s, %d pages\n", title, len(view.Data.Images))
	} else {
		log.Printf(" %d pages\n", len(view.Data.Images))
	}
	r.dt.SavePath = config.Conf.Directory
	if err := os.MkdirAll(r.dt.SavePath, 0755); err != nil {
		return "", err
	}

	serverBase, err := r.resolveURL(view.Data.ServerBase)
	if err != nil {
		return "", fmt.Errorf("解析山东大学图像地址: %w", err)
	}
	return "", r.downloadPages(serverBase, view.Data.Images)
}

func (r *SduTclib) getDetail(bookID string) (sduTclibDetailResponse, error) {
	query := url.Values{}
	encrypted, err := encryptSduTclibParam(bookID)
	if err != nil {
		return sduTclibDetailResponse{}, err
	}
	query.Set("bookId", encrypted)
	body, err := r.get(r.apiURL("portal/book/detail") + "?" + query.Encode())
	if err != nil {
		return sduTclibDetailResponse{}, fmt.Errorf("获取山东大学书目信息: %w", err)
	}
	var detail sduTclibDetailResponse
	if err := json.Unmarshal(body, &detail); err != nil {
		return sduTclibDetailResponse{}, fmt.Errorf("解析山东大学书目信息: %w", err)
	}
	if detail.Status != 1 {
		return sduTclibDetailResponse{}, errors.New("山东大学书目信息接口返回失败")
	}
	return detail, nil
}

func (r *SduTclib) getView(bookID, userID string) (sduTclibViewResponse, error) {
	encryptedUser, err := encryptSduTclibParam(userID)
	if err != nil {
		return sduTclibViewResponse{}, err
	}
	encryptedBook, err := encryptSduTclibParam(bookID)
	if err != nil {
		return sduTclibViewResponse{}, err
	}
	query := url.Values{}
	query.Set("userId", encryptedUser)
	query.Set("id", encryptedBook)
	body, err := r.get(r.apiURL("portal/book/view") + "?" + query.Encode())
	if err != nil {
		return sduTclibViewResponse{}, fmt.Errorf("获取山东大学图像信息: %w", err)
	}
	var view sduTclibViewResponse
	if err := json.Unmarshal(body, &view); err != nil {
		return sduTclibViewResponse{}, fmt.Errorf("解析山东大学图像信息: %w", err)
	}
	if view.Status == 0 && view.Data.ServerBase == "" {
		message := view.ErrorMessage
		if message == "" {
			message = "用户无权访问"
		}
		return sduTclibViewResponse{}, errors.New("山东大学图书馆图像接口失败: " + message)
	}
	return view, nil
}

func (r *SduTclib) downloadPages(serverBase string, images []string) error {
	failed := 0
	for i, image := range images {
		if !config.PageRange(i, len(images)) || strings.TrimSpace(image) == "" {
			continue
		}
		dest := filepath.Join(r.dt.SavePath, fmt.Sprintf("%04d.jpg", i+1))
		if FileExist(dest) {
			continue
		}
		imageURL := strings.TrimRight(serverBase, "/") + "/thumbnails/" + url.PathEscape(image) + "/large.jpg"
		log.Printf("Get %d/%d  %s\n", i+1, len(images), imageURL)
		resp, err := gohttp.FastGet(context.Background(), imageURL, gohttp.Options{
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
		})
		if err != nil || resp == nil || resp.GetStatusCode() < 200 || resp.GetStatusCode() >= 300 {
			failed++
			if err != nil {
				log.Printf("山东大学第 %d 页下载失败: %v\n", i+1, err)
			} else if resp == nil {
				log.Printf("山东大学第 %d 页下载失败: 空响应\n", i+1)
			} else {
				log.Printf("山东大学第 %d 页下载失败: HTTP %d\n", i+1, resp.GetStatusCode())
			}
		}
	}
	if failed > 0 {
		return fmt.Errorf("山东大学图像下载失败: %d/%d pages", failed, len(images))
	}
	return nil
}

func (r *SduTclib) get(rawURL string) ([]byte, error) {
	resp, err := gohttp.NewClient(context.Background(), gohttp.Options{
		CookieFile: config.Conf.CookieFile,
		HeaderFile: config.Conf.HeaderFile,
		CookieJar:  r.jar,
		Headers: map[string]interface{}{
			"User-Agent": config.Conf.UserAgent,
			"Referer":    r.dt.Url,
		},
	}).Get(rawURL)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, errors.New("空响应")
	}
	if resp.GetStatusCode() < 200 || resp.GetStatusCode() >= 300 {
		return nil, fmt.Errorf("HTTP %d", resp.GetStatusCode())
	}
	body, err := resp.GetBody()
	return []byte(body), err
}

func (r *SduTclib) apiURL(resource string) string {
	return r.dt.UrlParsed.Scheme + "://" + r.dt.UrlParsed.Host + "/" + strings.TrimLeft(resource, "/")
}

func (r *SduTclib) resolveURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	return r.dt.UrlParsed.ResolveReference(u).String(), nil
}

func sduTclibDetailValue(items []sduTclibDetail, names ...string) string {
	for _, name := range names {
		for _, item := range items {
			if item.Attr == name && strings.TrimSpace(item.Value) != "" {
				return strings.TrimSpace(item.Value)
			}
		}
	}
	return ""
}

func encryptSduTclibParam(value string) (string, error) {
	block, err := aes.NewCipher([]byte(sduTclibAESKey))
	if err != nil {
		return "", err
	}
	data := []byte(value)
	if remainder := len(data) % block.BlockSize(); remainder != 0 {
		data = append(data, make([]byte, block.BlockSize()-remainder)...)
	}
	// CryptoJS ZeroPadding adds no block when the input is already aligned.
	encrypted := make([]byte, len(data))
	cipher.NewCBCEncrypter(block, []byte(sduTclibAESIV)).CryptBlocks(encrypted, data)
	return base64.StdEncoding.EncodeToString(encrypted), nil
}

func sduTclibCookieValue(name string) string {
	if headers, err := chttp.ReadHeadersFromFile(config.Conf.HeaderFile); err == nil {
		for key, value := range headers {
			if !strings.EqualFold(key, "Cookie") {
				continue
			}
			for _, item := range strings.Split(value, ";") {
				parts := strings.SplitN(strings.TrimSpace(item), "=", 2)
				if len(parts) == 2 && parts[0] == name {
					return parts[1]
				}
			}
		}
	}

	data, err := os.ReadFile(config.Conf.CookieFile)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#HttpOnly_") {
			line = strings.TrimPrefix(line, "#HttpOnly_")
		} else if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) >= 7 && fields[5] == name {
			return strings.Trim(fields[6], "\"")
		}
	}
	return ""
}

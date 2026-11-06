package app

import (
	"archive/zip"
	"bookget/config"
	"bookget/model/iiif"
	"bookget/pkg/downloader"
	"bookget/pkg/gohttp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"log"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"
)

// Nationaljp handles both the old DAS endpoint and the current National
// Archives viewer. The latter exposes image content through IIIF manifests
// linked from /img/<id> pages.
type Nationaljp struct {
	dt    *DownloadTask
	extId string
}

func NewNationaljp() *Nationaljp {
	return &Nationaljp{dt: new(DownloadTask)}
}

func (r *Nationaljp) GetRouterInit(sURL string) (map[string]interface{}, error) {
	msg, err := r.Run(sURL)
	return map[string]interface{}{
		"type": "iiif",
		"url":  sURL,
		"msg":  msg,
	}, err
}

func (r *Nationaljp) Run(sURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(sURL))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errors.New("无效的国立公文书馆 URL")
	}
	r.dt = &DownloadTask{Url: sURL, UrlParsed: u}
	r.dt.Jar, _ = cookiejar.New(nil)

	// Keep support for the old DAS URLs, which carry BID in the query string.
	if r.dt.BookId = r.getBookId(sURL); r.dt.BookId != "" {
		r.extId = "jp2"
		return r.downloadLegacy()
	}

	imageIDs, err := r.getModernImageIDs()
	if err != nil {
		return "", err
	}
	return r.downloadModern(imageIDs)
}

func (r *Nationaljp) getBookId(sURL string) string {
	u, err := url.Parse(sURL)
	if err == nil {
		if bid := strings.TrimSpace(u.Query().Get("BID")); bid != "" {
			return bid
		}
	}
	m := regexp.MustCompile(`(?i)(?:^|[?&])BID=([A-Za-z0-9_-]+)`).FindStringSubmatch(sURL)
	if len(m) == 2 {
		return m[1]
	}
	return ""
}

// getModernImageIDs resolves a /img, /file, or /item URL to viewer IDs. A
// catalog page without a digital-content link is an error, rather than an
// empty successful download.
func (r *Nationaljp) getModernImageIDs() ([]string, error) {
	segments := strings.Split(strings.Trim(r.dt.UrlParsed.Path, "/"), "/")
	if len(segments) != 2 {
		return nil, errors.New("国立公文书馆首页没有可下载内容，请提供 /img/<id>、/file/<id> 或 /item/<id> 链接")
	}
	kind, id := strings.ToLower(segments[0]), segments[1]
	if (kind != "img" && kind != "file" && kind != "item") || !regexp.MustCompile(`^[0-9]+$`).MatchString(id) {
		return nil, errors.New("国立公文书馆链接必须是 /img/<id>、/file/<id> 或 /item/<id>")
	}
	if kind == "img" {
		return []string{id}, nil
	}

	body, err := r.getBody(r.dt.Url)
	if err != nil {
		return nil, fmt.Errorf("读取国立公文书馆详情页: %w", err)
	}
	// The links are server-rendered for pages with digital content. Keep the
	// order in the HTML because it is the archive's page order.
	linkRE := regexp.MustCompile(`(?i)(?:https?://[^"'\s>]+)?/img/([0-9]+)\b`)
	seen := make(map[string]struct{})
	var imageIDs []string
	for _, match := range linkRE.FindAllStringSubmatch(string(body), -1) {
		if len(match) != 2 {
			continue
		}
		if _, ok := seen[match[1]]; ok {
			continue
		}
		seen[match[1]] = struct{}{}
		imageIDs = append(imageIDs, match[1])
	}
	if len(imageIDs) == 0 {
		return nil, fmt.Errorf("国立公文书馆 %s/%s 页面没有可下载的数字图像", kind, id)
	}
	return imageIDs, nil
}

func (r *Nationaljp) endpoint(p string) string {
	return strings.TrimRight(r.dt.UrlParsed.Scheme+"://"+r.dt.UrlParsed.Host, "/") + "/" + strings.TrimLeft(p, "/")
}

func (r *Nationaljp) getBody(rawURL string) ([]byte, error) {
	client := gohttp.NewClient(context.Background(), gohttp.Options{
		CookieFile: config.Conf.CookieFile,
		CookieJar:  r.dt.Jar,
		Headers: map[string]interface{}{
			"User-Agent": config.Conf.UserAgent,
			"Referer":    r.dt.Url,
		},
	})
	resp, err := client.Get(rawURL)
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.GetStatusCode() != 200 {
		status := 0
		if resp != nil {
			status = resp.GetStatusCode()
		}
		return nil, fmt.Errorf("服务器返回 HTTP %d", status)
	}
	bs, err := resp.GetBody()
	if err != nil {
		return nil, err
	}
	if len(bs) == 0 {
		return nil, errors.New("服务器返回空响应")
	}
	return bs, nil
}

func (r *Nationaljp) downloadModern(imageIDs []string) (string, error) {
	if len(imageIDs) == 0 {
		return "", errors.New("没有找到可下载的国立公文书馆图像")
	}
	r.dt.SavePath = config.Conf.Directory
	var failures []error
	selectedVolumes := 0
	for i, imageID := range imageIDs {
		if !config.VolumeRange(i) {
			continue
		}
		selectedVolumes++
		images, err := r.getManifestImages(imageID)
		if err != nil {
			failures = append(failures, fmt.Errorf("图像 %s: %w", imageID, err))
			continue
		}

		savePath := r.dt.SavePath
		if len(imageIDs) > 1 {
			savePath = CreateDirectory(fmt.Sprintf("%04d", i+1))
		}
		if err := os.MkdirAll(savePath, 0755); err != nil {
			failures = append(failures, fmt.Errorf("创建图像目录: %w", err))
			continue
		}

		log.Printf(" %d/%d volume, %d pages\n", i+1, len(imageIDs), len(images))
		for page, serviceID := range images {
			if !config.PageRange(page, len(images)) {
				continue
			}
			dest := path.Join(savePath, fmt.Sprintf("%04d%s", page+1, config.Conf.FileExt))
			if validNationaljpImage(dest) {
				continue
			}
			if err := r.downloadModernImage(serviceID, dest); err != nil {
				failures = append(failures, fmt.Errorf("图像 %s 第 %d 页: %w", imageID, page+1, err))
			}
		}
	}
	if selectedVolumes == 0 {
		return "", errors.New("指定册数范围不包含任何国立公文书馆图像")
	}
	if len(failures) > 0 {
		return "", fmt.Errorf("国立公文书馆下载失败: %w", errors.Join(failures...))
	}
	return r.dt.SavePath, nil
}

func (r *Nationaljp) getManifestImages(imageID string) ([]string, error) {
	manifestURL := r.endpoint("/api/iiif/" + imageID + "/manifest.json")
	bs, err := r.getBody(manifestURL)
	if err != nil {
		return nil, err
	}
	var manifest iiif.ManifestResponse
	if err := json.Unmarshal(bs, &manifest); err != nil {
		return nil, fmt.Errorf("解析 IIIF 清单: %w", err)
	}
	var images []string
	for _, sequence := range manifest.Sequences {
		for _, canvas := range sequence.Canvases {
			for _, image := range canvas.Images {
				serviceID := strings.TrimSpace(image.Resource.Service.Id)
				if serviceID == "" {
					serviceID = strings.TrimSpace(image.Resource.Id)
					serviceID = strings.TrimSuffix(serviceID, "/full/max/0/native.jpg")
				}
				if serviceID != "" {
					images = append(images, serviceID)
				}
			}
		}
	}
	if len(images) == 0 {
		return nil, errors.New("IIIF 清单没有图像画布")
	}
	return images, nil
}

func (r *Nationaljp) downloadModernImage(serviceID, dest string) error {
	var err error
	if config.Conf.UseDzi {
		infoURL := strings.TrimRight(serviceID, "/") + "/info.json"
		args := []string{"-H", "User-Agent:" + config.Conf.UserAgent, "-H", "Referer:" + r.dt.Url}
		err = downloader.NewIIIFDownloader(&config.Conf).Dezoomify(context.Background(), infoURL, dest, args)
	} else {
		imageURL := strings.TrimRight(serviceID, "/") + "/" + strings.TrimLeft(config.Conf.Format, "/")
		resp, requestErr := gohttp.FastGet(context.Background(), imageURL, gohttp.Options{
			DestFile: dest,
			// Invalid or interrupted files are removed/replaced after the
			// validation above; do not let FastGet treat them as complete.
			Overwrite:   true,
			Concurrency: 1,
			CookieFile:  config.Conf.CookieFile,
			HeaderFile:  config.Conf.HeaderFile,
			CookieJar:   r.dt.Jar,
			Headers: map[string]interface{}{
				"User-Agent": config.Conf.UserAgent,
				"Referer":    r.dt.Url,
			},
		})
		if requestErr != nil {
			err = requestErr
		} else if resp == nil || resp.GetStatusCode() != 200 {
			status := 0
			if resp != nil {
				status = resp.GetStatusCode()
			}
			err = fmt.Errorf("服务器返回 HTTP %d", status)
		}
	}
	if err != nil {
		_ = os.Remove(dest)
		_ = os.Remove(dest + ".downloading")
		return err
	}
	if !validNationaljpImage(dest) {
		_ = os.Remove(dest)
		return errors.New("下载结果不是有效图像")
	}
	return nil
}

func validNationaljpImage(filename string) bool {
	file, err := os.Open(filename)
	if err != nil {
		return false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() == 0 {
		return false
	}
	_, _, err = image.DecodeConfig(file)
	return err == nil
}

func (r *Nationaljp) downloadLegacy() (string, error) {
	log.Printf("Get %s\n", r.dt.Url)
	volumes, err := r.getVolumes()
	if err != nil {
		return "", fmt.Errorf("读取国立公文书馆旧版卷清单: %w", err)
	}
	if len(volumes) == 0 {
		return "", errors.New("国立公文书馆旧版页面没有找到可下载卷")
	}
	r.dt.SavePath = config.Conf.Directory
	selected := 0
	var failures []error
	for i, volumeID := range volumes {
		if !config.VolumeRange(i) {
			continue
		}
		selected++
		dest := path.Join(r.dt.SavePath, fmt.Sprintf("%04d.zip", i+1))
		if validNationaljpZip(dest) {
			continue
		}
		log.Printf(" %d/%d volume, %s\n", i+1, len(volumes), r.extId)
		if _, err := r.do(i+1, volumeID, dest); err != nil {
			failures = append(failures, fmt.Errorf("第 %d 卷: %w", i+1, err))
		}
	}
	if selected == 0 {
		return "", errors.New("指定册数范围不包含任何国立公文书馆卷")
	}
	if len(failures) > 0 {
		return "", fmt.Errorf("国立公文书馆旧版下载失败: %w", errors.Join(failures...))
	}
	return r.dt.SavePath, nil
}

func (r *Nationaljp) do(index int, id, dest string) (string, error) {
	apiURL := r.endpoint("/acv/auto_conversion/download")
	data := fmt.Sprintf("DL_TYPE=%s&id_%d=%s", r.extId, index, id)
	resp, err := gohttp.Post(context.Background(), apiURL, gohttp.Options{
		DestFile:    dest,
		Overwrite:   false,
		Concurrency: 1,
		CookieFile:  config.Conf.CookieFile,
		HeaderFile:  config.Conf.HeaderFile,
		CookieJar:   r.dt.Jar,
		Headers: map[string]interface{}{
			"User-Agent":   config.Conf.UserAgent,
			"Content-Type": "application/x-www-form-urlencoded",
			"Referer":      r.dt.Url,
		},
		Body: []byte(data),
	})
	if err != nil {
		_ = os.Remove(dest)
		return "", err
	}
	if resp == nil || resp.GetStatusCode() != 200 {
		status := 0
		if resp != nil {
			status = resp.GetStatusCode()
		}
		_ = os.Remove(dest)
		return "", fmt.Errorf("服务器返回 HTTP %d", status)
	}
	if !validNationaljpZip(dest) {
		_ = os.Remove(dest)
		return "", errors.New("下载结果不是有效 ZIP 文件")
	}
	return "", nil
}

func validNationaljpZip(filename string) bool {
	archive, err := zip.OpenReader(filename)
	if err != nil {
		return false
	}
	defer archive.Close()
	return len(archive.File) > 0
}

func (r *Nationaljp) getVolumes() ([]string, error) {
	base := strings.TrimRight(r.dt.UrlParsed.Scheme+"://"+r.dt.UrlParsed.Host, "/")
	apiURL := fmt.Sprintf("%s/DAS/meta/listPhoto?LANG=default&BID=%s&ID=&NO=&TYPE=dljpeg&DL_TYPE=jpeg", base, url.QueryEscape(r.dt.BookId))
	bs, err := r.getBody(apiURL)
	if err != nil {
		return nil, err
	}
	// <input type="checkbox" class="check" name="id_2" posi="2" value="M...">
	matches := regexp.MustCompile(`(?is)<input[^>]+posi=["']([0-9]+)["'][^>]+value=["']([A-Za-z0-9_-]+)["']`).FindAllStringSubmatch(string(bs), -1)
	if len(matches) == 0 {
		return nil, nil
	}
	volumes := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(matches) > 1 && match[1] == "0" {
			continue
		}
		if match[2] != "" {
			volumes = append(volumes, match[2])
		}
	}
	return volumes, nil
}

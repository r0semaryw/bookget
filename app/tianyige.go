package app

import (
	"bookget/config"
	"bookget/model/tianyige"
	"bookget/pkg/gohttp"
	"bookget/pkg/util"
	"bytes"
	"context"
	"crypto/aes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/andreburgaud/crypt2go/ecb"
	"github.com/andreburgaud/crypt2go/padding"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"log"
	"math/rand"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// APP_ID & APP_KEY form https://gj.tianyige.com.cn/js/2.f75e590e.chunk.js
const TIANYIGE_ID = "4f65a2a8247f400c8c29474bf707d680"
const TIANYIGE_KEY = "G3HT5CX8FTG5GWGUUJX8B5SWJTXS1KRC"

type Tianyige struct {
	dt           *DownloadTask
	index        int
	localStorage struct {
		authorization  string
		authorizationu string
	}
}

func NewTianyige() *Tianyige {
	return &Tianyige{
		// 初始化字段
		dt:    new(DownloadTask),
		index: 0,
	}
}

func (r *Tianyige) GetRouterInit(sUrl string) (map[string]interface{}, error) {
	msg, err := r.Run(sUrl)
	return map[string]interface{}{
		"url": sUrl,
		"msg": msg,
	}, err
}

func (r *Tianyige) Run(sUrl string) (msg string, err error) {
	r.dt.UrlParsed, err = url.Parse(sUrl)
	r.dt.Url = sUrl
	r.dt.BookId = r.getBookId(r.dt.Url)
	if r.dt.BookId == "" {
		return "requested URL was not found.", err
	}
	r.dt.Jar, _ = cookiejar.New(nil)
	//r.localStorage.authorization, r.localStorage.authorizationu, err = r.getLocalStorage()
	return r.download()
}

func (r *Tianyige) getBookId(sUrl string) (bookId string) {
	m := regexp.MustCompile(`(?i)searchpage/([A-z0-9_-]+)`).FindStringSubmatch(sUrl)
	if m != nil {
		bookId = m[1]
	} else {
		m = regexp.MustCompile(`(?i)catalogid=([A-z0-9_-]+)`).FindStringSubmatch(sUrl)
		if m != nil {
			bookId = m[1]
		}
	}
	return bookId
}

func (r *Tianyige) download() (msg string, err error) {
	respVolume, err := r.getVolumes(r.dt.BookId, r.dt.Jar)
	if err != nil {
		fmt.Println(err)
		return "getVolumes", err
	}
	canvases, err := r.getCanvases(r.dt.BookId, r.dt.Jar)
	if err != nil {
		log.Println(err)
		return
	}
	log.Printf(" %d volumes,  %d pages.\n", len(respVolume), len(canvases))
	parts := make(tianyige.Parts)
	for _, record := range canvases {
		parts[record.FascicleId] = append(parts[record.FascicleId], record)
	}
	var bookmark = config.CatalogVersionInfo + "\r\n"
	sizeVol := len(respVolume)
	for i, vol := range respVolume {
		if !config.VolumeRange(i) {
			continue
		}
		vid := fmt.Sprintf("%04d", i+1)
		r.dt.SavePath = CreateDirectory(vid)
		sizePage := len(parts[vol.FascicleId])
		log.Printf(" %d/%d volume, %d pages \n", i+1, sizeVol, sizePage)
		text, err := r.getCatalogById(vol.CatalogId, vol.FascicleId, r.index)
		if err == nil {
			bookmark += text
		}
		r.do(parts[vol.FascicleId])
	}

	savePath := config.Conf.Directory
	data, _ := io.ReadAll(transform.NewReader(bytes.NewReader([]byte(bookmark)), simplifiedchinese.GBK.NewEncoder()))
	_ = os.WriteFile(path.Join(savePath, "catalog.txt"), []byte(bookmark), os.ModePerm)
	_ = os.WriteFile(path.Join(savePath, "catalog-gbk.txt"), []byte(data), os.ModePerm)
	return msg, err
}

func (r *Tianyige) do(records []tianyige.ImageRecord) (msg string, err error) {
	if records == nil {
		return "", nil
	}
	size := len(records)
	fmt.Println()
	for i, record := range records {
		// Keep the global catalog offset independent of -p, so a later volume
		// still receives the correct page numbers in catalog.txt.
		r.index++
		if !config.PageRange(i, size) {
			continue
		}
		sortId := fmt.Sprintf("%04d", i+1)
		ext := strings.ToLower(config.Conf.FileExt)
		if ext != ".png" && ext != ".jpg" && ext != ".jpeg" {
			ext = ".jpg"
		}
		filename := sortId + ext
		dest := path.Join(r.dt.SavePath, filename)
		if FileExist(dest) {
			continue
		}
		log.Printf("Get %d/%d image %s\n", i+1, size, record.ImageId)
		body, err := r.downloadImage(record.ImageId)
		if err != nil {
			return "", fmt.Errorf("天一阁第 %d 页: %w", i+1, err)
		}
		if err := writeDownloadFile(dest, body); err != nil {
			return "", err
		}
		util.PrintSleepTime(config.Conf.Sleep)
		fmt.Println()
	}
	fmt.Println()
	return "", err
}

// downloadImage follows the current Tianyige reader protocol.  Each logical
// page is stored as 6, 8, 9 or 12 physical image slices.  The browser asks
// for a short-lived server timestamp, signs "slice.fileInfoId.timestamp" with
// the site's Base58 variant, then stitches the returned JPEGs in a grid.
func (r *Tianyige) downloadImage(imageID string) ([]byte, error) {
	files, err := r.queryImageFiles(imageID)
	if err != nil {
		return nil, err
	}
	var source *tianyige.FileInfo
	for i := range files {
		if !strings.Contains(files[i].FileOldname, "_c") {
			source = &files[i]
			break
		}
	}
	if source == nil && len(files) > 0 {
		source = &files[0]
	}
	if source == nil {
		return nil, errors.New("没有原始图像文件")
	}

	// Older deployments returned a complete file path.  Keep this fallback so
	// existing Tianyige URLs remain usable while the current split-file API is
	// handled below.
	if source.FileInfoId == "" {
		if source.FilePath == "" || source.FileName == "" {
			return nil, errors.New("图像文件缺少 fileInfoId 或文件路径")
		}
		uri := fmt.Sprintf("https://%s/fileUpload/%s/%s", r.dt.UrlParsed.Host, source.FilePath, source.FileName)
		return r.getBody(uri, r.dt.Jar)
	}

	serverTime, err := r.getPhysicalFileTime()
	if err != nil {
		return nil, err
	}
	count := tianyigeSliceCount(source.FileInfoId)
	slices := make([][]byte, count)
	workers := config.Conf.Threads
	if workers < 1 {
		workers = 1
	}
	if workers > count {
		workers = count
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				requestID := tianyigeBase58(fmt.Sprintf("%d.%s.%d", i, source.FileInfoId, serverTime))
				uri := fmt.Sprintf("https://%s/g/sw-file/physicalFile/getSplittedFile?requestId=%s", r.dt.UrlParsed.Host, url.QueryEscape(requestID))
				body, err := r.getBody(uri, r.dt.Jar)
				if err != nil {
					errMu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("获取图像分片 %d/%d: %w", i, count, err)
					}
					errMu.Unlock()
					continue
				}
				slices[i-1] = body
			}
		}()
	}
	for i := 1; i <= count; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return stitchTianyigeSlices(slices, count)
}

func (r *Tianyige) queryImageFiles(imageID string) ([]tianyige.FileInfo, error) {
	apiURL := fmt.Sprintf("https://%s/g/sw-anb/api/queryOcrFileByimageId?imageId=%s", r.dt.UrlParsed.Host, url.QueryEscape(imageID))
	body, err := r.getBody(apiURL, r.dt.Jar)
	if err != nil {
		return nil, err
	}
	var response tianyige.ResponseFile
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}
	if response.Code != 0 && response.Code != 200 {
		return nil, fmt.Errorf("图像接口返回 code %d: %s", response.Code, response.Msg)
	}
	return response.Data.File, nil
}

func (r *Tianyige) getPhysicalFileTime() (int64, error) {
	uri := fmt.Sprintf("https://%s/g/sw-file/physicalFile/currentTime", r.dt.UrlParsed.Host)
	body, err := r.getBody(uri, r.dt.Jar)
	if err != nil {
		return 0, err
	}
	var response struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data int64  `json:"data"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return 0, err
	}
	if response.Code != 0 && response.Code != 200 {
		return 0, fmt.Errorf("服务器时间接口返回 code %d: %s", response.Code, response.Msg)
	}
	if response.Data <= 0 {
		return 0, errors.New("服务器时间为空")
	}
	return response.Data, nil
}

const tianyigeBase58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// tianyigeBase58 mirrors the small encoder embedded in the Tianyige reader.
// The input used by the site is ASCII, so byte and UTF-16 code-unit iteration
// are equivalent here.
func tianyigeBase58(input string) string {
	digits := []int{0}
	for i := 0; i < len(input); i++ {
		carry := int(input[i])
		for j := range digits {
			carry += digits[j] << 8
			digits[j] = carry % 58
			carry /= 58
		}
		for carry > 0 {
			digits = append(digits, carry%58)
			carry /= 58
		}
	}
	var out strings.Builder
	for i := 0; i < len(input) && input[i] == 0; i++ {
		out.WriteByte('1')
	}
	for i := len(digits) - 1; i >= 0; i-- {
		out.WriteByte(tianyigeBase58Alphabet[digits[i]])
	}
	return out.String()
}

func tianyigeSliceCount(fileInfoID string) int {
	count := 8
	if len(fileInfoID) >= 2 {
		firstEven := fileInfoID[0]%2 == 0
		secondEven := fileInfoID[1]%2 == 0
		switch {
		case firstEven && secondEven:
			count = 12
		case firstEven && !secondEven:
			count = 9
		case !firstEven && secondEven:
			count = 8
		default:
			count = 6
		}
	}
	return count
}

func stitchTianyigeSlices(raw [][]byte, count int) ([]byte, error) {
	if len(raw) == 0 {
		return nil, errors.New("没有图像分片")
	}
	columns := 4
	if count == 6 || count == 9 {
		columns = 3
	}
	rows := (len(raw) + columns - 1) / columns
	images := make([]image.Image, len(raw))
	colWidths := make([]int, columns)
	rowHeights := make([]int, rows)
	for i, body := range raw {
		img, _, err := image.Decode(bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("解析图像分片 %d: %w", i+1, err)
		}
		images[i] = img
		col := i % columns
		row := i / columns
		if img.Bounds().Dx() > colWidths[col] {
			colWidths[col] = img.Bounds().Dx()
		}
		if img.Bounds().Dy() > rowHeights[row] {
			rowHeights[row] = img.Bounds().Dy()
		}
	}
	width, height := 0, 0
	for _, w := range colWidths {
		width += w
	}
	for _, h := range rowHeights {
		height += h
	}
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	y := 0
	for row := 0; row < rows; row++ {
		x := 0
		for col := 0; col < columns; col++ {
			i := row*columns + col
			if i < len(images) {
				draw.Draw(canvas, image.Rect(x, y, x+images[i].Bounds().Dx(), y+images[i].Bounds().Dy()), images[i], images[i].Bounds().Min, draw.Src)
			}
			x += colWidths[col]
		}
		y += rowHeights[row]
	}
	var out bytes.Buffer
	if strings.EqualFold(config.Conf.FileExt, ".png") {
		if err := png.Encode(&out, canvas); err != nil {
			return nil, err
		}
	} else {
		quality := config.Conf.Quality
		if quality < 1 || quality > 100 {
			quality = 90
		}
		if err := jpeg.Encode(&out, canvas, &jpeg.Options{Quality: quality}); err != nil {
			return nil, err
		}
	}
	return out.Bytes(), nil
}

func (r *Tianyige) getVolumes(catalogId string, jar *cookiejar.Jar) (volumes []tianyige.Volume, err error) {
	apiUrl := fmt.Sprintf("https://%s/g/sw-anb/api/getFasciclesByCataId?catalogId=%s", r.dt.UrlParsed.Host, catalogId)
	bs, err := r.getBody(apiUrl, jar)
	if bs == nil || err != nil {
		return nil, err
	}
	resObj := new(tianyige.ResponseVolume)
	if err = json.Unmarshal(bs, resObj); err != nil {
		return nil, err
	}
	volumes = make([]tianyige.Volume, len(resObj.Data))
	copy(volumes, resObj.Data)
	return volumes, err
}

func (r *Tianyige) getCanvases(bookId string, jar *cookiejar.Jar) (canvases []tianyige.ImageRecord, err error) {
	for i := 1; i < 100; i++ {
		apiUrl := fmt.Sprintf("https://%s/g/sw-anb/api/queryImageByCatalog?catalogId=%s", r.dt.UrlParsed.Host, bookId)
		d := fmt.Sprintf(`{"param":{"pageNum":%d,"pageSize":999}}`, i)
		bs, err := r.postBody(apiUrl, []byte(d), jar)
		if bs == nil || err != nil {
			break
		}
		var resObj tianyige.ResponsePage
		if err = json.Unmarshal(bs, &resObj); resObj.Code != 200 {
			break
		}
		if resObj.Data.Total == len(canvases) {
			break
		}
		records := make([]tianyige.ImageRecord, len(resObj.Data.Records))
		copy(records, resObj.Data.Records)
		canvases = append(canvases, records...)
	}
	return canvases, err
}

func (r *Tianyige) getImageById(imageId string) (imgUrl, ocrUrl string, err error) {
	apiUrl := fmt.Sprintf("https://%s/g/sw-anb/api/queryOcrFileByimageId?imageId=%s", r.dt.UrlParsed.Host, imageId)
	var bs []byte
	for i := 0; i < 3; i++ {
		bs, err = r.getBody(apiUrl, r.dt.Jar)
		if bs != nil {
			break
		}
	}
	if err != nil {
		return
	}
	var resObj tianyige.ResponseFile
	if err = json.Unmarshal(bs, &resObj); err != nil {
		fmt.Println(err)
		return
	}

	for _, ossFile := range resObj.Data.File {
		if strings.Contains(ossFile.FileOldname, "_c") {
			ocrUrl = fmt.Sprintf("https://%s/fileUpload/%s/%s", r.dt.UrlParsed.Host, ossFile.FilePath, ossFile.FileName)
		} else {
			imgUrl = fmt.Sprintf("https://%s/fileUpload/%s/%s", r.dt.UrlParsed.Host, ossFile.FilePath, ossFile.FileName)
		}
	}
	return
}

func (r *Tianyige) getCatalogById(catalogId, fascicleId string, indexStart int) (string, error) {
	apiUrl := fmt.Sprintf("https://%s/g/sw-anb/api/getDirectorys?catalogId=%s&fascicleId=%s&directoryName=", r.dt.UrlParsed.Host, catalogId, fascicleId)
	bs, err := r.getBody(apiUrl, r.dt.Jar)
	if err != nil {
		return "", err
	}
	var resp tianyige.Catalog
	if err = json.Unmarshal(bs, &resp); err != nil {
		fmt.Println(err)
		return "", err
	}
	var bookmark string
	for _, record := range resp.Data.Records {
		m := regexp.MustCompile(`(\d+).jpg`).FindStringSubmatch(record.PageId)
		if m != nil {
			page, _ := strconv.Atoi(m[1])
			page = indexStart + page
			if os.PathSeparator == '\\' {
				bookmark += fmt.Sprintf("%s......%d\r\n", record.Name, page)
			} else {
				bookmark += fmt.Sprintf("%s......%d\n", record.Name, page)
			}
		}
	}
	return bookmark, err
}

func (r *Tianyige) getBody(sUrl string, jar *cookiejar.Jar) ([]byte, error) {
	ctx := context.Background()
	token := r.getToken()
	cli := gohttp.NewClient(ctx, gohttp.Options{
		CookieFile: config.Conf.CookieFile,
		CookieJar:  jar,
		Headers: map[string]interface{}{
			"User-Agent":     config.Conf.UserAgent,
			"Referer":        r.dt.Url,
			"Origin":         "https://" + r.dt.UrlParsed.Host,
			"Content-Type":   "application/json;charset=UTF-8",
			"token":          token,
			"appId":          TIANYIGE_ID,
			"authorization":  r.localStorage.authorization,
			"authorizationu": r.localStorage.authorizationu,
		},
	})
	resp, err := cli.Get(sUrl)
	if err != nil {
		return nil, err
	}
	bs, _ := resp.GetBody()
	if bs == nil || resp.GetStatusCode() != 200 {
		msg := fmt.Sprintf("Please try again later.[%d %s]\n", resp.GetStatusCode(), resp.GetReasonPhrase())
		fmt.Println(msg)
		return nil, errors.New(msg)
	}
	return bs, err
}

func (r *Tianyige) postBody(sUrl string, d []byte, jar *cookiejar.Jar) ([]byte, error) {
	token := r.getToken()
	ctx := context.Background()
	cli := gohttp.NewClient(ctx, gohttp.Options{
		CookieFile: config.Conf.CookieFile,
		CookieJar:  jar,
		Headers: map[string]interface{}{
			"User-Agent":     config.Conf.UserAgent,
			"Referer":        r.dt.Url,
			"Origin":         "https://" + r.dt.UrlParsed.Host,
			"Content-Type":   "application/json;charset=UTF-8",
			"token":          token,
			"appId":          TIANYIGE_ID,
			"authorization":  r.localStorage.authorization,
			"authorizationu": r.localStorage.authorizationu,
		},
		Body: d,
	})
	resp, err := cli.Post(sUrl)
	if err != nil {
		return nil, err
	}
	bs, _ := resp.GetBody()
	if bs == nil || resp.GetStatusCode() != 200 {
		msg := fmt.Sprintf("Please try again later.[%d %s]\n", resp.GetStatusCode(), resp.GetReasonPhrase())
		fmt.Println(msg)
		return nil, errors.New(msg)
	}
	return bs, err
}

func (r *Tianyige) encrypt(pt, key []byte) string {
	block, err := aes.NewCipher(key)
	if err != nil {
		panic(err.Error())
	}
	mode := ecb.NewECBEncrypter(block)
	padder := padding.NewPkcs7Padding(mode.BlockSize())
	pt, err = padder.Pad(pt) // padd last block of plaintext if block size less than block cipher size
	if err != nil {
		panic(err.Error())
	}
	ct := make([]byte, len(pt))
	mode.CryptBlocks(ct, pt)
	return base64.StdEncoding.EncodeToString(ct)
}

func (r *Tianyige) getToken() string {
	rd := rand.New(rand.NewSource(time.Now().UnixNano()))
	//pt := []byte(strconv.Itoa(r.Intn(900000)+100000) + strconv.FormatInt(time.Now().UnixMilli(), 10))
	pt := []byte(fmt.Sprintf("%.6d%d", rd.Int31()%10000, time.Now().UnixMilli()))
	// Key size for AES is either: 16 bytes (128 bits), 24 bytes (192 bits) or 32 bytes (256 bits)
	key := []byte(TIANYIGE_KEY)
	return r.encrypt(pt, key)
}

// // 假设 LocalStorage 中已经有 'authorization' 和 'authorizationu' 这两个键
// const authorization = localStorage.getItem('authorization');
// const authorizationu = localStorage.getItem('authorizationu');
//func (r *Tianyige) getLocalStorage() (string, string, error) {
//	bs, err := os.ReadFile(config.Conf.LocalStorage)
//	if bs == nil || err != nil {
//		return "", "", err
//	}
//
//	// 分割输入字符串为多个部分，以换行符为分隔符
//	lines := strings.Split(string(bs), "\n")
//
//	authTokens := make(map[string]string)
//	for _, line := range lines {
//		// 去除行首和行尾的空格
//		line = strings.TrimSpace(line)
//
//		// 如果行是空的，则跳过
//		if line == "" {
//			continue
//		}
//
//		// 分割键和值，以冒号为分隔符，并去除键和值两侧的空格
//		parts := strings.SplitN(line, ":", 2)
//		if len(parts) != 2 {
//			return "", "", fmt.Errorf("invalid line format: %s", line)
//		}
//		key := strings.TrimSpace(parts[0])
//		value := strings.Trim(strings.Trim(strings.Trim(parts[1], "\""), " "), "'")
//
//		// 将键值对存储到 map 中
//		authTokens[key] = value
//	}
//
//	// 从 map 中提取 authorization 和 authorizationu 的值
//	authorization, ok1 := authTokens["authorization"]
//	authorizationu, ok2 := authTokens["authorizationu"]
//
//	// 检查是否成功提取到所有需要的值
//	if !ok1 || !ok2 {
//		return "", "", fmt.Errorf("missing required token")
//	}
//	return authorization, authorizationu, nil
//}

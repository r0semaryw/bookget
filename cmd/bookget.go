package main

import (
	"bookget/app"
	"bookget/config"
	"bookget/pkg/queue"
	"bookget/pkg/version"
	"bookget/router"
	"bufio"
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
)

var (
	versionChecker = version.NewChecker(
		config.Version,
		"deweizhu", // GitHub仓库所有者
		"bookget",  // GitHub仓库名称
	)
)

func main() {
	ctx := context.Background()

	// 初始化配置
	if !initializeConfig(ctx) {
		return
	}

	// 根据运行模式执行相应操作
	executeByRunMode(ctx)
}

// initializeConfig 处理配置初始化
func initializeConfig(ctx context.Context) bool {
	if !config.Init(ctx) {
		return false
	}
	return true
}

// executeByRunMode 根据运行模式执行相应操作
func executeByRunMode(ctx context.Context) {
	switch determineRunMode() {
	case RunModeSingleURL:
		if !executeSingleURL(ctx, config.Conf.DUrl) {
			return
		}
	case RunModeBatchURLs:
		executeBatchURLs()
	case RunModeInteractive:
		if !runInteractiveMode(ctx) {
			return
		}
	case RunModeInteractiveImage:
		runInteractiveModeImage(ctx)
	}

	log.Println("Download complete.")
}

type RunMode int

const (
	RunModeSingleURL RunMode = iota
	RunModeBatchURLs
	RunModeInteractive
	RunModeInteractiveImage
)

// determineRunMode 确定运行模式
func determineRunMode() RunMode {
	if config.Conf.DUrl != "" {
		return RunModeSingleURL
	}
	if hasValidURLsFile() {
		return RunModeBatchURLs
	}
	if config.Conf.DownloaderMode == 1 {
		return RunModeInteractiveImage
	}
	return RunModeInteractive
}

// hasValidURLsFile 检查是否有有效的URLs文件
func hasValidURLsFile() bool {
	f, err := os.Stat(config.Conf.UrlsFile)
	return err == nil && f.Size() > 0
}

// executeSingleURL 处理单个URL模式
func executeSingleURL(ctx context.Context, rawUrl string) bool {
	if err := processURL(ctx, rawUrl); err != nil {
		log.Println(err)
		return false
	}
	return true
}

// executeBatchURLs 处理批量URLs模式
func executeBatchURLs() {
	allUrls, err := loadAndFilterURLs(config.Conf.UrlsFile)
	if err != nil {
		log.Println(err)
		return
	}

	concurrency := config.Conf.MaxConcurrent
	if concurrency < 1 {
		concurrency = 1
	}
	q := queue.NewConcurrentQueue(concurrency)
	// Each URL gets its own output directory. The router implementations use a
	// global directory setting, so processURLSet serializes the full router call
	// while retaining the queue's task lifecycle.
	if config.Conf.DownloaderMode == 1 {
		processURLsDownloaderMode(q, allUrls)
	} else {
		processURLsManual(q, allUrls)
	}
	q.Wait()
}

// runInteractiveMode 运行交互模式
func runInteractiveMode(ctx context.Context) bool {
	//cleanupCookieFile()
	success := true
	for {
		rawUrl, err := readURLFromInput()
		if err != nil {
			break
		}

		if err = processURL(ctx, rawUrl); err != nil {
			log.Println(err)
			success = false
		}
	}
	return success
}

// runInteractiveModeImage 运行交互模式：图片下载
func runInteractiveModeImage(ctx context.Context) {
	//cleanupCookieFile()
	app.NewImageDownloader().Run("")
}

// loadAndFilterURLs 加载并过滤URLs
func loadAndFilterURLs(filename string) ([]string, error) {
	content, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("无法读取URL文件: %w", err)
	}

	lines := strings.Split(string(content), "\n")
	var urls []string
	for index, line := range lines {
		sUrl := strings.TrimSpace(line)
		if index == 0 {
			// UTF-8 BOMs are common when the list was saved by Windows tools.
			sUrl = strings.TrimPrefix(sUrl, "\ufeff")
		}
		if isValidURL(sUrl) {
			urls = append(urls, sUrl)
		}
	}

	if len(urls) == 0 {
		return nil, fmt.Errorf("URL文件中没有有效的URL")
	}

	return urls, nil
}

// isValidURL 验证URL是否有效
func isValidURL(rawURL string) bool {
	u, err := neturl.ParseRequestURI(rawURL)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// processURLsDownloaderMode 自动检测模式处理URLs
func processURLsDownloaderMode(q *queue.ConcurrentQueue, allUrls []string) {
	for _, v := range allUrls {
		rawURL := v // 创建局部变量供闭包使用
		q.Go(func() {
			processURLSet("bookget", rawURL)
		})
	}
}

// processURLsManual 手动模式处理URLs
func processURLsManual(q *queue.ConcurrentQueue, allUrls []string) {
	for _, v := range allUrls {
		u, err := neturl.Parse(v)
		if err != nil {
			log.Printf("URL解析失败: %s, 错误: %v\n", v, err)
			continue
		}

		rawURL := v      // 创建局部变量供闭包使用
		siteID := u.Host // 不要在闭包中捕获循环变量 u
		q.Go(func() {
			processURLSet(siteID, rawURL)
		})
	}
}

// processURLSet 处理一组URLs
func processURLSet(siteID string, rawUrl string) {
	// Most site routers keep per-book state on the router object and write to
	// config.Conf.Directory.  Keep both pieces isolated for each batch item.
	batchDirectoryMu.Lock()
	defer batchDirectoryMu.Unlock()

	oldDirectory := config.Conf.Directory
	config.Conf.Directory = batchOutputDirectory(oldDirectory, rawUrl)
	defer func() { config.Conf.Directory = oldDirectory }()
	if err := os.MkdirAll(config.Conf.Directory, 0755); err != nil {
		log.Printf("创建批量输出目录失败: %s (%v)\n", rawUrl, err)
		return
	}

	result, err := router.FactoryRouter(siteID, rawUrl)
	if err != nil {
		log.Printf("下载失败: %s (%v)\n", rawUrl, err)
		return
	}
	// 使用result
	_ = result
}

var batchDirectoryMu sync.Mutex

// batchOutputDirectory returns a stable, filesystem-safe directory for a URL.
// The short hash keeps URLs that share a path (for example different query
// parameters) from colliding, while the readable prefix makes the directory
// useful when browsing downloaded books.
func batchOutputDirectory(baseDirectory, rawURL string) string {
	u, err := neturl.Parse(rawURL)
	label := "url"
	if err == nil {
		label = u.Host + u.EscapedPath()
		if label == "" {
			label = u.Host
		}
	}
	label = sanitizeBatchDirectoryName(label)
	if label == "" {
		label = "url"
	}
	labelRunes := []rune(label)
	if len(labelRunes) > 72 {
		label = string(labelRunes[:72])
	}
	hash := sha256.Sum256([]byte(rawURL))
	return filepath.Join(baseDirectory, fmt.Sprintf("%s-%x", label, hash[:8]))
}

// directOutputDirectory returns a readable, stable directory for a single URL
// or interactive download. Prefer an identifier commonly used by book sites;
// use the path when no such identifier exists, and always add a URL hash so
// distinct query strings cannot collide.
func directOutputDirectory(baseDirectory, rawURL string) string {
	u, err := neturl.Parse(rawURL)
	label := "url"
	if err == nil {
		label = u.Host
		for _, key := range []string{"title", "ssno", "fid", "bid", "identifier", "id", "code"} {
			if value := strings.TrimSpace(u.Query().Get(key)); value != "" {
				label += "-" + key + "-" + value
				break
			}
		}
		if label == u.Host {
			label += u.EscapedPath()
		}
	}
	label = sanitizeBatchDirectoryName(label)
	if label == "" {
		label = "url"
	}
	labelRunes := []rune(label)
	if len(labelRunes) > 72 {
		label = string(labelRunes[:72])
	}
	hash := sha256.Sum256([]byte(rawURL))
	return filepath.Join(baseDirectory, fmt.Sprintf("%s-%x", label, hash[:8]))
}

func sanitizeBatchDirectoryName(name string) string {
	var b strings.Builder
	lastWasSeparator := false
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
			lastWasSeparator = false
			continue
		}
		if !lastWasSeparator {
			b.WriteByte('-')
			lastWasSeparator = true
		}
	}
	return strings.Trim(b.String(), "-._")
}

// readURLFromInput 从用户输入读取URL
func readURLFromInput() (string, error) {
	reader := bufio.NewReader(os.Stdin)
	fmt.Println()
	fmt.Println("Enter an URL:")
	fmt.Print("-> ")
	input, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("读取输入失败: %w", err)
	}
	return strings.TrimSpace(input), nil
}

// processURL 处理单个URL
func processURL(ctx context.Context, rawUrl string) error {
	rawURL := strings.TrimSpace(rawUrl)
	if !isValidURL(rawURL) {
		return fmt.Errorf("无效的URL: %s", rawUrl)
	}

	u, err := neturl.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("URL解析失败: %w", err)
	}

	// Keep single URL and interactive downloads separate as well. Routers write
	// through the global config directory, so scope it to this URL and restore
	// it after the router finishes.
	oldDirectory := config.Conf.Directory
	config.Conf.Directory = directOutputDirectory(oldDirectory, rawURL)
	defer func() { config.Conf.Directory = oldDirectory }()
	if err := os.MkdirAll(config.Conf.Directory, 0755); err != nil {
		return fmt.Errorf("创建下载目录失败: %w", err)
	}
	log.Printf("保存目录: %s\n", config.Conf.Directory)

	result, err := router.FactoryRouter(u.Host, rawURL)
	if err != nil {
		log.Println(err)
		return err
	}
	// 使用result
	_ = result

	return nil
}

// cleanupCookieFile 清理cookie文件
func cleanupCookieFile() {
	if err := os.Remove(config.Conf.CookieFile); err != nil && !os.IsNotExist(err) {
		log.Printf("清理cookie文件失败: %v\n", err)
	}
}

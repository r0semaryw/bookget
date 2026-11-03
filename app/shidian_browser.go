package app

import (
	"bookget/config"
	"bookget/scripts"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/chromedp/cdproto/network"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// Remember the working mode across fresh adapters in -I and interactive mode.
// Each export owns its browser process; no tab/profile is borrowed from a user.
var shidianPreferHeaded atomic.Bool

type shidianAPIError struct {
	Code  int
	Stage string
}

func (e *shidianAPIError) Error() string {
	return fmt.Sprintf("识典%s接口错误 %d", e.Stage, e.Code)
}

func fetchShidianManifest(rawURL, bookID string) (shidianManifest, error) {
	browser, err := findShidianBrowser(config.Conf.ShidianBrowser)
	if err != nil {
		return shidianManifest{}, err
	}
	headed := config.Conf.ShidianHeaded || shidianPreferHeaded.Load()
	desktop := runtime.GOOS != "linux" || os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
	return exportShidianWithFallback(headed, desktop, func(visible bool) (shidianManifest, error) {
		return exportShidianInBrowser(browser, rawURL, bookID, visible)
	})
}

func exportShidianWithFallback(headed, desktop bool, export func(bool) (shidianManifest, error)) (shidianManifest, error) {
	manifest, err := export(headed)
	var apiErr *shidianAPIError
	if headed || !errors.As(err, &apiErr) || apiErr.Code != 31000 {
		return manifest, err
	}
	if !desktop {
		return manifest, fmt.Errorf("%w；无界面浏览器被拒绝，当前没有桌面环境；请在桌面运行 --shidian-headed，或用 --shidian-pages 指定清单", err)
	}
	log.Print("识典古籍：无界面浏览器收到 31000，切换为可见浏览器重试")
	manifest, err = export(true)
	if err == nil {
		shidianPreferHeaded.Store(true)
	}
	return manifest, err
}

func findShidianBrowser(explicit string) (string, error) {
	if explicit != "" {
		path, err := exec.LookPath(explicit)
		if err != nil {
			return "", fmt.Errorf("无法执行 --shidian-browser 指定的浏览器: %w", err)
		}
		return path, nil
	}
	candidates := []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "microsoft-edge", "microsoft-edge-stable", "chrome", "msedge"}
	switch runtime.GOOS {
	case "darwin":
		candidates = append(candidates, "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge", "/Applications/Chromium.app/Contents/MacOS/Chromium")
	case "windows":
		for _, root := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LOCALAPPDATA")} {
			if root != "" {
				candidates = append(candidates, filepath.Join(root, "Google", "Chrome", "Application", "chrome.exe"), filepath.Join(root, "Microsoft", "Edge", "Application", "msedge.exe"))
			}
		}
	}
	for _, name := range candidates {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", errors.New("识典自动清单需要本机安装 Chrome、Chromium 或 Edge；可用 --shidian-browser 指定路径，或用 --shidian-pages 指定已有清单（无需浏览器）")
}

func shidianResponseError(rawURL string, body []byte) error {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Hostname() != "www.shidianguji.com" && u.Hostname() != "shidianguji.com") {
		return nil
	}
	stage := shidianAPIStage(u.Path)
	if stage == "" {
		return nil // Optional bookmarks/reviews may require login for public books.
	}
	var result struct {
		ErrorCode int `json:"errorCode"`
	}
	if json.Unmarshal(body, &result) != nil || result.ErrorCode == 0 {
		return nil
	}
	return &shidianAPIError{Code: result.ErrorCode, Stage: stage}
}

func shidianAPIStage(path string) string {
	switch path {
	case "/api/ancientlib/read/reader-book/get/", "/api/ancientlib/read/book/get/":
		return "书籍加载"
	case "/api/ancientlib/read/book/pages/v3/":
		return "扫描页"
	}
	return ""
}

var shidianProgress = regexp.MustCompile(`^bookget: 已取得 [0-9]+/[0-9]+ 页图像地址$`)

func exportShidianInBrowser(browser, rawURL, bookID string, headed bool) (shidianManifest, error) {
	var manifest shidianManifest
	timeout := config.Conf.ShidianTimeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	deadlineCtx, deadlineCancel := context.WithTimeout(context.Background(), timeout)
	defer deadlineCancel()
	// Use the browser's ordinary security settings and the site's own SDK.
	// Neither Cookie injection nor emulating the signature algorithm is needed.
	opts := []chromedp.ExecAllocatorOption{
		chromedp.ExecPath(browser), chromedp.NoFirstRun, chromedp.NoDefaultBrowserCheck,
		chromedp.Flag("headless", !headed), chromedp.Flag("lang", "zh-CN"),
	}
	if config.Conf.ShidianBrowserProfile != "" {
		profile, err := filepath.Abs(config.Conf.ShidianBrowserProfile)
		if err != nil {
			return manifest, err
		}
		opts = append(opts, chromedp.UserDataDir(profile))
	}
	allocCtx, allocCancel := chromedp.NewExecAllocator(deadlineCtx, opts...)
	defer allocCancel()
	browserCtx, browserCancel := chromedp.NewContext(allocCtx)
	defer browserCancel()
	ctx, stop := context.WithCancelCause(browserCtx)
	defer stop(nil)

	// Read API bodies only once loading has finished. CDP callbacks cannot block
	// on another CDP command, so the body read runs outside the event handler.
	pending := make(map[network.RequestID]string)
	chromedp.ListenTarget(ctx, func(event interface{}) {
		switch e := event.(type) {
		case *network.EventResponseReceived:
			u, err := url.Parse(e.Response.URL)
			if err == nil && (u.Hostname() == "www.shidianguji.com" || u.Hostname() == "shidianguji.com") && shidianAPIStage(u.Path) != "" {
				pending[e.RequestID] = e.Response.URL
			}
		case *network.EventLoadingFailed:
			delete(pending, e.RequestID)
		case *network.EventLoadingFinished:
			if requestURL, ok := pending[e.RequestID]; ok {
				delete(pending, e.RequestID)
				go func(id network.RequestID, requestURL string) {
					var body []byte
					err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
						var err error
						body, err = network.GetResponseBody(id).Do(ctx)
						return err
					}))
					if err == nil {
						if apiErr := shidianResponseError(requestURL, body); apiErr != nil {
							stop(apiErr)
						}
					}
				}(e.RequestID, requestURL)
			}
		case *cdpruntime.EventConsoleAPICalled:
			for _, arg := range e.Args {
				var message string
				if json.Unmarshal(arg.Value, &message) == nil && shidianProgress.MatchString(message) {
					log.Print("识典古籍：", strings.TrimPrefix(message, "bookget: "))
				}
			}
		}
	})

	var result struct {
		Manifest shidianManifest `json:"manifest"`
		Error    *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	options, _ := json.Marshal(map[string]interface{}{"bookId": bookID, "download": false, "timeout": timeout.Milliseconds()})
	source := strings.TrimSuffix(strings.TrimSpace(strings.Replace(scripts.ShidianExport, "/* BOOKGET_OPTIONS */", string(options), 1)), ";")
	source += ".then(manifest => ({manifest}), error => ({error: {code: Number(error.code) || 0, message: String(error.message)}}))"
	pollOptions := []chromedp.PollOption{chromedp.WithPollingArgs(bookID), chromedp.WithPollingInterval(200 * time.Millisecond), chromedp.WithPollingTimeout(0)}
	err := chromedp.Run(ctx,
		network.Enable(),
		chromedp.Navigate(rawURL),
		chromedp.PollFunction(`id => location.pathname.includes('/book/' + id + '/chapter/')`, nil, pollOptions...),
		// /book/ID routes through the SPA; reload to bind SSR bookInfo to the reader.
		chromedp.Reload(),
		chromedp.PollFunction(`id => Object.values(window._ROUTER_DATA?.loaderData || {}).some(x => x?.bookInfo?.bookId === id) && !!window.byted_psdk?.decrypt && !!window.byted_psdk?.getPtokenStatus`, nil, pollOptions...),
		chromedp.Evaluate(source, &result, func(p *cdpruntime.EvaluateParams) *cdpruntime.EvaluateParams { return p.WithAwaitPromise(true) }),
	)
	if cause := context.Cause(ctx); cause != nil {
		var apiErr *shidianAPIError
		if errors.As(cause, &apiErr) {
			return manifest, apiErr
		}
		if errors.Is(cause, context.DeadlineExceeded) {
			return manifest, errors.New("识典自动清单等待超时；可增加 --shidian-timeout，或使用 --shidian-headed --shidian-browser-profile <独立目录> 在网页完成登录/验证后重试；也可提供 --shidian-pages 清单")
		}
	}
	if err != nil {
		return manifest, fmt.Errorf("识典浏览器加载失败（检查浏览器路径、桌面环境或独立 profile 是否被占用；也可用 --shidian-pages）: %w", err)
	}
	if result.Error != nil {
		if result.Error.Code != 0 {
			return manifest, &shidianAPIError{Code: result.Error.Code, Stage: "扫描页"}
		}
		return manifest, fmt.Errorf("识典清单导出失败: %s", result.Error.Message)
	}
	if _, err := validateShidianManifest(result.Manifest, bookID); err != nil {
		return manifest, err
	}
	// Allow Chromium to flush a deliberately selected persistent login profile.
	if err := chromedp.Cancel(browserCtx); err != nil {
		return manifest, fmt.Errorf("关闭识典浏览器: %w", err)
	}
	return result.Manifest, nil
}

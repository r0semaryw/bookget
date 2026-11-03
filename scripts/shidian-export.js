// 在识典古籍的 /book/<ID> 阅读页打开开发者工具 Console，运行本文件。
// 使用网页已有的请求签名和图像地址解码器，只导出清单，不下载扫描图。
// 清单包含有时效的 CDN 地址，不包含 Cookie、账号信息或 ptoken。
(async (options = {}) => {
  const timeout = options.timeout || 120000;
  const controller = new AbortController();
  let timer;
  try {
    return await Promise.race([
      (async () => {
        if (!/(^|\.)shidianguji\.com$/.test(location.hostname)) {
          throw new Error("请先打开识典古籍阅读页，再运行此脚本");
        }
        const route = Object.values(window._ROUTER_DATA?.loaderData || {})
          .find((item) => item?.bookInfo?.bookId);
        // SPA 切书后 SSR 数据可能仍指向旧书，必须核对当前地址。
        const currentID = location.pathname.match(/\/book\/([A-Za-z0-9_-]+)(?:\/|$)/)?.[1];
        const book = route?.bookInfo;
        if (!book || book.bookId !== currentID || (options.bookId && book.bookId !== options.bookId)) {
          throw new Error("没有当前书籍的数据，请刷新阅读页后重试");
        }
        if (!Number.isSafeInteger(book.totalPage) || book.totalPage < 1) {
          throw new Error("该书没有可导出的扫描页");
        }
        const sdk = window.byted_psdk;
        if (!sdk?.getPtokenStatus || !sdk?.decrypt) {
          throw new Error("网页图像组件尚未就绪，请等待阅读器加载后重试");
        }
        const pages = new Map();
        const pageSize = 40; // 与阅读器的单次请求大小一致
        for (let pageIndex = 1; (pageIndex - 1) * pageSize < book.totalPage; pageIndex++) {
          const ptoken = await sdk.getPtokenStatus();
          controller.signal.throwIfAborted();
          if (!ptoken) throw new Error("网页会话未就绪，请刷新页面后重试");
          const response = await fetch("/api/ancientlib/read/book/pages/v3/", {
            signal: controller.signal,
            method: "POST",
            credentials: "same-origin",
            headers: { "Content-Type": "application/json", "client-type": "web" },
            body: JSON.stringify({
              bookId: book.bookId,
              version: book.version,
              needCheckVersion: true,
              pageIndex,
              pageSize,
              startPageNum: 1,
              endPageNum: book.totalPage + 1, // API 结束页为开区间
              ptoken,
              isWebpSupported: false, // 请求 PNG；不指定缩放或降低画质参数
            }),
          });
          if (!response.ok) throw new Error(`图像接口返回 HTTP ${response.status}`);
          const result = await response.json();
          if (result.errorCode !== 0) {
            const error = new Error(`图像接口错误 ${result.errorCode}，请在网页确认能正常阅读后重试`);
            error.code = result.errorCode;
            throw error;
          }
          const batch = result.data?.pages;
          if (!Array.isArray(batch) || batch.length === 0) {
            throw new Error(`第 ${pageIndex} 批没有扫描页，清单未导出`);
          }
          for (const page of batch) {
            const decoded = await sdk.decrypt(page.picUrl || "", ptoken, page.urlEncryptType);
            controller.signal.throwIfAborted();
            const url = decoded.decrypt ? decoded.data : page.picUrl;
            if (!/^https:\/\//.test(url || "")) {
              throw new Error(`第 ${page.pageNum} 页图像地址未成功解码`);
            }
            if (!Number.isSafeInteger(page.pageNum) || page.pageNum < 1 || page.pageNum > book.totalPage || pages.has(page.pageNum)) {
              throw new Error(`图像页码重复或越界: ${page.pageNum}`);
            }
            pages.set(page.pageNum, { pageNum: page.pageNum, pageId: page.pageId, url });
          }
          console.info(`bookget: 已取得 ${pages.size}/${book.totalPage} 页图像地址`);
          if (pages.size < book.totalPage) await new Promise((resolve) => setTimeout(resolve, 500));
        }
        if (pages.size !== book.totalPage) throw new Error(`扫描页不完整: ${pages.size}/${book.totalPage}`);
        controller.signal.throwIfAborted();
        const manifest = {
          format: "bookget-shidian-v1",
          bookId: book.bookId,
          bookName: book.bookName,
          totalPage: book.totalPage,
          version: book.version,
          sourceUrl: `${location.origin}/book/${book.bookId}`,
          exportedAt: new Date().toISOString(),
          pages: [...pages.values()].sort((a, b) => a.pageNum - b.pageNum),
        };
        if (options.download === false) return manifest;
        const blob = new Blob([JSON.stringify(manifest, null, 2)], { type: "application/json" });
        const blobURL = URL.createObjectURL(blob);
        const link = document.createElement("a");
        link.href = blobURL;
        link.download = `${book.bookId}.json`;
        link.click();
        setTimeout(() => URL.revokeObjectURL(blobURL), 10000);
        console.info(`bookget: 已导出 ${link.download}，可用 --shidian-pages 指定此文件`);
        return { bookId: book.bookId, bookName: book.bookName, pages: pages.size };
      })(),
      new Promise((_, reject) => {
        timer = setTimeout(() => {
          controller.abort();
          reject(new Error("识典清单生成超时"));
        }, timeout);
      }),
    ]);
  } finally {
    clearTimeout(timer);
  }
})(/* BOOKGET_OPTIONS */);

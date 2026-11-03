# 簡介

bookget 数字古籍图书下载工具，已支持约 50+ 个数字图书馆。 

### 使用説明
1. 打开 [https://github.com/deweizhu/bookget/releases](https://github.com/deweizhu/bookget/releases/latest) 下载最新版。
1. [必读]使用手册wiki https://github.com/deweizhu/bookget/wiki 
1. 此项目代码仅供学习研究使用，欢迎有能力的朋友git clone 代码二次开发维护您自己的版本。

#### 源碼編譯
从源码构建，仅对计算机程序员参考。普通用户可直接跳过阅读。   
阅读 [golang 官方文档](https://golang.google.cn/doc/install) ，给您的电脑安装 golang 开发环境。
```shell
git clone https://github.com/deweizhu/bookget.git
cd bookget

# 本地开发时可直接运行
make linux-amd64    # 编译Linux版本
make windows-amd64  # 编译Windows版本
make release        # 编译所有平台
```

- For Win可用环境：windows 10 x64 / windows 11 （自2024/01/12日起，引入bookget-gui只适用于Win10/11 x64系统）。

### 模板批量下载（-m）

使用 `bookget -m 1` 进入模板批量下载模式。模板支持旧格式 `[PAGE]`、`[VOL]`、`[AB]`，也支持带格式宽度的新格式，例如 `{{page:04}}`、`{{vol:02}}`、`{{volume}}` 和 `{{ab}}`。每个生成地址会根据响应类型自动处理：图片或 PDF 直接保存，网页提取其中的图片或 IIIF manifest，IIIF manifest 或 `info.json` 自动提取并下载图像。

模板没有 `.jpg`、`.pdf`、`.html` 或 `.json` 扩展名时，下载器会使用默认扩展名作为临时文件名，并根据实际媒体类型保存最终文件。

### CADAL 阅读器

CADAL 阅读器页面可直接使用普通 URL 模式下载，程序会从阅读器接口获取图像路径并按页保存为 JPG。需要使用已登录 CADAL 会话导出的 Cookie：

推荐使用浏览器开发者工具复制 Cookie 请求头：

1. 登录 CADAL 并打开图书页面，按 `F12` 打开开发者工具。
2. 进入 **Network/网络**，刷新页面，选择 `reader.shtml` 或 `api/addr` 请求。
3. 在 **Headers → Request Headers → Cookie** 中复制完整内容。
4. 保存为 `header.txt`，格式如下：

```text
Cookie: cadalreader=...; JSESSIONID=...; userAuth=...; userid=...
```

使用 `-H` 指定请求头文件：

```shell
bookget -i 'https://cadal.edu.cn/cx/reader/reader/book/reader.shtml?channel=4&code=a1c54a8f1fac9377e3ec403720a5712c&epage=-1&ipinside=0&netuser=0&spage=1&ssno=06707067&userid=901685591&bookType=2' -H header.txt
```

多个 CADAL 页面也可以放入文本文件后使用普通批量模式：

```shell
bookget -I cadal_urls.txt -H header.txt
```

也可以使用浏览器扩展导出 `cookies.txt`，但应选择 Netscape 格式，不能直接使用 JSON 格式。使用 Cookie 文件时改用 `-C cookie.txt`；如果导出文件导致未授权，建议改用上面的 `header.txt` 方法。

`-H` 和 `-C` 指定的文件会作用于本次运行中的所有请求。下载不同网站时不要复用 CADAL 的请求头或 Cookie，应为每个网站分别保存文件并通过参数指定。

使用 `-i` 或直接启动程序后输入 URL 时，下载内容会先保存到 `-O` 指定目录下按书籍标识创建的独立子目录中，不会直接写入 `downloads` 根目录。

### 山东大学图书馆特藏资源

山东大学特藏详情页（例如 `https://tclib.sdu.edu.cn/portal-tsxq.html?id=3638`）的书目信息和目录接口可以公开访问，但阅读图像接口要求登录。程序会自动调用详情页接口、阅读权限接口，并按阅读器返回的页序下载 `large.jpg` 图像。

先在浏览器中登录山东大学图书馆，再从开发者工具 **Network/网络 → Request Headers → Cookie** 复制 Cookie 请求头，保存为单独的 `sdu-header.txt`：

```text
Cookie: userId=...; permissionLevel=...; 其他会话Cookie=...
```

然后使用普通 URL 模式下载：

```shell
bookget -i 'https://tclib.sdu.edu.cn/portal-tsxq.html?id=3638' -H sdu-header.txt
```

也可以将浏览器导出的 Netscape 格式 `cookies.txt` 通过 `-C` 传入：

```shell
bookget -i 'https://tclib.sdu.edu.cn/portal-tsxq.html?id=3638' -C sdu-cookies.txt
```

图像会保存到 `downloads` 下按 URL 生成的独立目录中。若提示“未找到 userId”或“用户无权访问”，说明 Cookie 已过期、缺少 `userId`，或当前账号没有该书的阅读权限；不要把 Cookie 内容提交到公开仓库或聊天记录中。

### 识典古籍扫描图

支持 `https://www.shidianguji.com/book/<书籍ID>` 及其 `/chapter/<章节ID>` 阅读链接，下载该书全部扫描页。章节链接也按整书处理；只下载部分页时使用 `-p 起始页:结束页`，页码为全书扫描页序号。

识典的图像接口需要浏览器动态签名，并由网页 SDK 解码图像地址，单独提供 `-H` / `-C` Cookie 不能代替这一步。因此需要先在浏览器导出图像清单，再交给 bookget 下载：

1. 用浏览器打开目标书籍的阅读页，等待原图加载。若刚从其他书跳转过来，先刷新一次页面。
2. 打开开发者工具的 **Console/控制台**，运行仓库中的 [scripts/shidian-export.js](scripts/shidian-export.js) 全部内容。
3. 脚本沿用网页自身的接口和解码器，每批获取 40 页，确认整书页码齐全后下载 `<书籍ID>.json`。例如本书导出 `NCL11566A.json`，共 236 页；未登录状态下也能导出此书。其他书籍以网页实际阅读权限为准。
4. 使用新编译的 bookget 指定清单路径：

```shell
./bookget-linux -i 'https://www.shidianguji.com/book/NCL11566A/chapter/1lyxbeutoq9ta' \
  --shidian-pages ./NCL11566A.json

# 仅下载全书扫描页的第 1 至 3 页
./bookget-linux -i 'https://www.shidianguji.com/book/NCL11566A' \
  --shidian-pages ./NCL11566A.json -p 1:3

# 交互输入 URL
./bookget-linux --shidian-pages ./NCL11566A.json

# 多本书：每本先导出清单，以 <书籍ID>.json 放到同一个目录
./bookget-linux -I shidian_urls.txt --shidian-pages ./shidian-pages
```

程序在每个 URL 的输出目录中创建 `书名-书籍ID` 子目录，保存为 `0001.png`、`0002.png` 等；根据实际响应也可能保存为 `.jpg`。重新运行相同命令时，已校验完整的文件会跳过，损坏或未完成的页会重新下载。清单与输入 URL 的书籍 ID 不符、缺页、重复页码或响应不是完整图像时会报错。

下载使用阅读器的 `picUrl`，不使用 `thumbUrl` 或仅有少量页的 `previewPages`，不添加缩放/降低画质参数，也不在本地重新编码。得到的是网站提供的扫描图版本，不保证等同于馆藏未压缩的扫描母版；站点可能已裁页或压缩。清单含有时效的图像签名 URL，不含 Cookie 或 `ptoken`，若下载返回 403 等错误，请刷新阅读页重新导出清单后续传。bookget 不会把 `-H` / `-C` 中的站点凭证发送给图像 CDN。

导出器依赖网站当前的 `window._ROUTER_DATA`、`byted_psdk` 和 `/api/ancientlib/read/book/pages/v3/` 接口；若网站改版或要求登录/验证，先在网页正常完成阅读所需步骤，重新导出。直接传入 URL 而没有清单时，程序会提示上述步骤，不会将几张预览图当作整书下载完成。

# 新書推薦

本書即以清代避諱為例，試探如何運用融合數位與傳統的e考據之法，重新定義避諱學2.0該有的研究模式。這將是治清代的文史工作者案頭不可或缺的一本專著。[^1]
-  [清代避諱研究](https://gpi.culture.tw/books/1011300273)
- 作者: 黃一農 / 黄一农
- 出版社: 台灣清華大學出版社
- 副标题: e考據的學術實踐
- 出版年: 2024-4
- ISBN: 9786269724987
[^1]:[https://gpi.culture.tw/books/1011300273](https://gpi.culture.tw/books/1011300273)

### 第三方社區/網站
- 书格 https://www.shuge.org
### Arch Linux AUR 可选包
以下是热心网友维护的 Linux 软件仓库，便于 Arch Linux 用户安装使用。
-  https://aur.archlinux.org/packages/bookget
-  https://aur.archlinux.org/packages/bookget-bin
-  https://aur.archlinux.org/packages/bookget-git
-  [bookget -  Arch Linux 中文维基](https://wiki.archlinuxcn.org/wiki/Bookget)



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



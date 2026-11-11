<p align="center">
  <img src="resources/icon.png" width="128" height="128" alt="开心果">
</p>

<h1 align="center">开心果</h1>

<p align="center">
  <b>无广告、不烧心</b><br>
  红果短剧的极简 macOS 客户端
</p>

<p align="center">
  <a href="https://github.com/kadaliao/kaixinguo/releases/latest"><img src="https://img.shields.io/github/v/release/kadaliao/kaixinguo?label=%E4%B8%8B%E8%BD%BD" alt="最新版本"></a>
  <a href="https://github.com/kadaliao/kaixinguo/actions/workflows/ci.yml"><img src="https://github.com/kadaliao/kaixinguo/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <img src="https://img.shields.io/badge/macOS-12%2B-111?logo=apple" alt="macOS 12+">
  <a href="LICENSE"><img src="https://img.shields.io/github/license/kadaliao/kaixinguo" alt="MIT"></a>
  <a href="https://awesomemygo.dev/apps/kaixinguo"><img src="https://awesomemygo.dev/badge.svg" height="20" alt="Listed on Awesome MyGo"></a>
</p>

![开心果的推荐页](docs/screenshots/discover.webp)

开心果把红果短剧搬到了 Mac 上：没有广告、没有弹窗，打开就是剧。界面由 Go 直接在 GPU 上绘制，不套网页，安装包约 8 MB，窗口即开即用。

## 功能

- **发现**：推荐、排行榜（按类型和榜单）、新剧上线（前后两周的日历）、热搜榜
- **搜索**：⌘F 搜索短剧，同一部剧的各季按顺序排好
- **收藏**：剧集页点「收藏」，或在任意列表里右键卡片，把想看的剧存下来；侧栏「我的 › 收藏」随时找到
- **续播**：记住每集看到第几秒，选集和收藏页卡片标出进度；侧栏「最近播放」可按想看、正在看、已看完筛选，一点就接着看
- **连播**：一集放完自动下一集，一季看完接下一季
- **选集**：长剧每 100 集一段，多季的剧一键切换
- **影院**：播放页双击视频或点「影院」全屏观看；鼠标靠近右侧边缘，选集自动出现，离开自动收起
- **新手教程**：第一次启动用四页卡片介绍搜索、收藏、影院和手势；第一次播放还有播放器专属指南
- **手势**：视频右半边上下滚动调音量，左半边调亮度
- **秒开**：上次的推荐和封面缓存在本地，打开就显示，新内容在后台刷新
- **自动更新**：有新版本时弹窗提醒，附更新说明，一键下载安装并重启；侧栏底部也会提示，菜单「开心果 › 检查更新…」可随时手动检查
- 跟随系统的浅色和深色外观

| 剧集详情 | 收藏 |
| :---: | :---: |
| ![剧集详情：播放、收藏、选季和选集](docs/screenshots/series.webp) | ![收藏页](docs/screenshots/favorites.webp) |
| **热搜** | **排行榜** |
| ![热搜榜](docs/screenshots/hot.webp) | ![排行榜](docs/screenshots/rank.webp) |

## 下载安装

从 [Releases](https://github.com/kadaliao/kaixinguo/releases/latest) 下载 `kai-xin-guo-<版本>.dmg`，打开后把「开心果」拖进「应用程序」。

- 一个安装包同时支持 Apple 芯片和 Intel，需要 macOS 12 或更高版本
- 用 Developer ID 签名并通过了苹果公证，双击即可打开
- 每个安装包旁边附有 `.sha256`，可以用 `shasum -a 256 -c kai-xin-guo-<版本>.dmg.sha256` 校验

## 使用

| 快捷键 | 作用 |
| --- | --- |
| ⌘F | 搜索 |
| ⌘[ | 返回 |
| 空格 | 播放 / 暂停 |
| ← → | 快退 / 快进 5 秒 |
| ↑ ↓ | 上一集 / 下一集 |
| Esc | 退出播放；影院中先收起选集、退出全屏 |
| 双击视频 | 进入 / 退出影院 |

播放时，双击视频或点右上角的「影院」进入全屏；鼠标靠近右侧边缘，选集自动出现，鼠标离开选集区域后自动收起。底部播放栏集中了播放/暂停、快退/快进、上一集/下一集、进度、音量和小窗；放映时底部栏和顶部按钮一起收起，移动鼠标一起出现。鼠标滚轮或触控板在视频右半边上下滑动调音量，左半边调亮度。

观看记录和收藏只存在本机，不会上传：

| 内容 | 位置 |
| --- | --- |
| 观看记录 | `~/Library/Application Support/开心果/history.json` |
| 收藏 | `~/Library/Application Support/开心果/favorites.json` |
| 匿名设备信息、窗口位置 | `~/Library/Application Support/开心果/` |
| 更新设置（跳过的版本、是否自动安装） | `~/Library/Application Support/开心果/updater.json` |
| 新手教程标记 | `~/Library/Application Support/开心果/tutorial` |
| 播放器指南标记 | `~/Library/Application Support/开心果/player-tutorial` |
| 封面和推荐缓存 | `~/Library/Caches/开心果/` |

想重看引导，删掉 `tutorial` 和 `player-tutorial` 两个标记后重启。

卸载时删掉应用和上面两个目录；播放器的网页缓存在 `~/Library/WebKit/com.kadaliao.kaixinguo` 和 `~/Library/Caches/com.kadaliao.kaixinguo`，也可以一并删掉。

## 常见问题

**怎么更新？**
0.4.0 起，开心果启动 10 秒后和之后每天检查一次 GitHub 上的最新版本，有新版本就弹窗提醒，点「安装更新」即可，下载的更新经过签名校验。0.3.0 及更早的版本没有这个功能，需要从 [Releases](https://github.com/kadaliao/kaixinguo/releases/latest) 手动下载一次。

**第一次打开，列表很久才出来？**
第一次启动要注册一个匿名设备，网络慢时可能要等十几秒。之后启动会先显示缓存。

**某些剧提示「暂不支持这种视频格式」？**
目前只支持整段的 MP4，分段（DASH/HLS）的长视频还不能播放。

**有的集数带一把锁？**
那一集需要登录红果账号才能看，开心果不提供登录。

**加载失败或者播放卡住？**
从终端启动并打开调试日志，会打印每个请求，方便提 issue 时附上：

```sh
KX_DEBUG=1 /Applications/开心果.app/Contents/MacOS/开心果
```

## 从源码构建

需要 macOS 12+ 和 Go 1.27.3+（以 [go.mod](go.mod) 为准）。不用 cgo。

```sh
git clone https://github.com/kadaliao/kaixinguo.git
cd kaixinguo
go tool mygo dev     # 开发：改代码自动重启
go test ./...        # 测试：不需要网络和窗口
go tool mygo build   # 打包：build/darwin-arm64/开心果.app 和 .dmg
```

自己打包的应用是临时签名，只适合在本机运行。正式发版的流程见 [docs/releasing.md](docs/releasing.md)。

## 参与开发

欢迎 issue 和 PR。动手之前建议先读：

- [CONTRIBUTING.md](CONTRIBUTING.md)：开发环境、提交前检查、代码约定
- [docs/architecture.md](docs/architecture.md)：代码地图、线程模型、播放和缓存是怎么工作的
- [CHANGELOG.md](CHANGELOG.md)：每个版本改了什么

一句话概括实现：剧集是 CENC（AES-CTR）加密的 HEVC MP4，`internal/cenc` 按需原地解密任意字节区间，`proxy.go` 在 `127.0.0.1` 上边下载边解密，交给 WebView 里的 `<video>` 播放，可以随意拖动进度，不依赖 ffmpeg；封面是 HEIC，`internal/imageio` 通过 purego 调用 macOS 的 ImageIO 解码，不用 cgo。

## 致谢

- [openhongguo](https://github.com/beilunyang/openhongguo)：红果短剧的 Go SDK，开心果的全部数据都来自它
- [MyGo](https://github.com/egoist/mygo)：原生 UI 框架和打包工具

## 许可证

[MIT](LICENSE)

## 免责声明

本项目仅供学习和研究，与红果短剧及其运营方无关。请遵守 openhongguo 的[免责声明](https://github.com/beilunyang/openhongguo#免责声明)，内容版权归原权利人所有。

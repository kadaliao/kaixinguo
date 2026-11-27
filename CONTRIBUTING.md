# 参与开发

谢谢你愿意帮忙。开心果是个小项目，目标是**一直简单、一直快**：一个窗口、几页列表、一个播放器。新增功能之前请先想想它是否值得多出来的那份复杂度。

## 开始之前

- **报告问题**：用 [问题反馈](https://github.com/kadaliao/kaixinguo/issues/new?template=bug_report.yml) 模板，附上版本、macOS 版本和 `KX_DEBUG=1` 的日志，问题会快很多被定位。
- **接口失效**（列表全空、所有剧都播不了）：多半是红果接口变了，先看看 [openhongguo](https://github.com/beilunyang/openhongguo/issues) 有没有人报过。
- **大改动**：先开 issue 聊方案，避免做完了才发现方向不合适。改错别字、修小 bug 直接提 PR 就好。

## 开发环境

- macOS 12 或更高版本；Windows 10、11 也能开发，还在试验，要装的系统组件见 README 的「Windows（试验）」
- Go，版本见 [go.mod](go.mod)
- 第一次可以跑 `go tool mygo doctor` 检查环境

```sh
go tool mygo dev     # 开发版：改代码自动重新编译、重启
go test ./...        # 测试：不需要网络和窗口
go tool mygo build   # 打包 .app 和 .dmg 到 build/
```

先读 [docs/architecture.md](docs/architecture.md)，尤其是「线程模型」一节。

## 提交前检查

```sh
gofmt -l .              # 没有输出才对
go vet ./...
go tool mygo vet .      # MyGo 的检查：元素生命周期、goroutine 捕获
go test ./...
go test -race ./...     # 改了跨 goroutine 的状态时
```

CI 在每个 PR 上跑同样的检查。改了界面的话，再用 `go tool mygo dev` 实际点一遍，浅色和深色外观都看看。

## 代码约定

- **主线程独占状态**：`app` 的字段只在主线程读写。后台工作做完用 `a.update(fn)` 交回结果。不要在 goroutine 里捕获 `*ui.Context` 或 `ui.Element`。
- **视图里没有副作用**：视图会为一个事件重建多次，加载数据要有防重复的标记，参考 `loadMore`。
- **新功能配测试**：用 `ui.NewTester` 写视图测试，从点击到显示走一遍，参考 `favorites_test.go`。测试里用了 `history` 或 `favorites` 的，记得在清理时 `flush`（`testApp` 已经做了）。
- **注释**：用英文写完整的句子，说清楚一个东西**是什么**或**为什么**这样做，而不是复述代码。照着已有代码的口吻写。
- **界面文字**用简体中文，标点用全角。
- **依赖**：尽量不加。不用 cgo，这样任何机器都能交叉编译。
- **本地数据格式**：`history.json`、`favorites.json` 已经在用户机器上了，只能加字段（带 `omitempty`），不能改名或删除。
- MyGo 相关的更多约定（元素生命周期、输入处理、验证方法）在 [.agents/skills/mygo-maintenance/SKILL.md](.agents/skills/mygo-maintenance/SKILL.md)。它是写给 AI 编程助手的，人读也有用。

## 提交信息

用中文，一行概括改了什么，必要时空一行再写原因：

```
收藏：剧集页和列表右键都能收藏，侧栏新增「我的 › 收藏」

退出时等待观看记录和收藏写盘，避免刚点完收藏就退出时丢失。
```

## 更新日志

用户能感知到的改动（新功能、修复、行为变化），请在 [CHANGELOG.md](CHANGELOG.md) 的「未发布」下面加一行。纯重构和测试不用写。

## 更新截图

README 里的截图在 [docs/screenshots/](docs/screenshots/)，界面有明显变化时请一起更新。截图用 MyGo 的自动化接口从真实窗口拍下，保证和应用一致：

1. 用 `go build -tags e2e -o /tmp/kxg .` 构建，再以 `MYGO_AUTOMATION=/tmp/kxg.sock /tmp/kxg` 启动。为了不弄乱自己的观看记录，可以把它放进一个改了 `CFBundleDisplayName` 和 `CFBundleIdentifier` 的 `.app` 副本里运行，数据会存到另一个目录。
2. 通过 socket 发送 `tree`、`act`、`capture` 等请求，导航到要拍的页面并截图（协议见 MyGo 的 `docs/plugins/automation.md`）。窗口保持默认的 1180 × 760。
3. `capture` 拍到的侧栏是透明的（系统的毛玻璃材质不在窗口内容里），用浅灰（`#EDEDF0`）垫底，缩到 1600 像素宽，加圆角，存成 WebP（质量 88 左右）。

播放页的视频画面在系统 WebView 里，`capture` 拍不到，所以 README 没有播放页截图；需要的话可以用 ⌘⇧4 手动截。

## 发版

维护者的事，流程见 [docs/releasing.md](docs/releasing.md)。

## 许可证

提交的代码以 [MIT](LICENSE) 许可证发布。

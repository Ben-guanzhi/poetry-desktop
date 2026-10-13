# 中华诗词 · 桌面客户端（poetry-desktop）

基于 [Fyne](https://fyne.io) 的 Go 桌面应用，离线浏览、搜索、收藏与导出中国古典诗词文选。数据存本地 SQLite，无需联网即可使用（首次下载数据集除外）。

## 功能

- **按数据集浏览**：内置 18 个数据集——全宋诗（约 25.5 万首）、全唐诗（约 5.8 万首）、宋词（约 2.2 万首）、元曲、五代诗词、花间集、南唐二主词、论语、孟子、楚辞、蒙学、幽梦影、水墨唐诗等；经典类另支持从 `tools/crawler` 产出的本地 JSON 导入（如维基文库整理稿）。
- **全文搜索**：SQLite FTS（trigram 分词）+ 触发器自维护索引，短词自动回退 LIKE；带搜索历史。
- **收藏与导出**：收藏页可见上限 1000，导出可达近全量（10000），产出 `collections_export.txt`。
- **导入自愈**：数据集导入按"实际插入数"决定是否重建全文索引——零插入的重复导入不再付全表重建的代价。

## 技术栈与结构

| 层 | 实现 |
|---|---|
| UI | `internal/ui`（Fyne v2：导航意图驱动，浏览/搜索/收藏/导出/设置） |
| 存储 | `internal/store`（`modernc.org/sqlite`，纯 Go 免 CGO，WAL，schema v4） |
| 下载 | `internal/fetcher`（从 GitHub raw 拉取数据集 JSON 并导入） |
| 模型 | `internal/model`（数据集登记表：目录、文件模式、朝代、类型） |
| 采集 | `tools/crawler`（另独立入口，抓 Wikisource 繁简整理稿，产出供本地导入） |

数据源鸣谢：诗词主体来自 [chinese-poetry-simplify](https://github.com/zhangleiDev/chinese-poetry-simplify) 的简体整理数据；经典类采集自中文维基文库。

## 构建与运行

要求 Go 1.27.1+（`go.mod` 的 `go` 指令为准）。

```sh
go build -o poetry-desktop .   # 产物在本目录
./poetry-desktop
```

Linux 上 Fyne 需要 X/GL 开发头文件：

```sh
sudo apt install gcc libgl1-mesa-dev xorg-dev
```

首次进入在设置页"下载全部"或按数据集单独下载；数据落在应用存储目录（`collections_export.txt` 亦在此）。

## 测试与门禁

```sh
gofmt -l .
go vet ./...
go test -count=1 ./...
```

PR 会触发 CI（`.github/workflows/ci.yml`）执行上述四步。

## 说明

- `poetry-desktop.exe`、`tools/crawler.exe` 为本地构建产物，已被 `.gitignore` 排除。
- 存储 schema 当前为 v4（v3→v4 移除了 `authors` 表及零调用方法）。

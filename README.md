# Qobuz-navidrome-plugin
中文专辑多的建议使用网易云插件： https://github.com/sky8282/Netease-navidrome-plugin
## Navidrome 的增强插件，基于 Qobuz 数据源<br>实现自动补全本地音乐库元数据 （ PDF 需 token，国内需科学网络）
✨ 功能特性
* 🖼️ 自动写入专辑封面 cover.jpg
* 👤 自动写入歌手头像 artist.jpg
* 🎼 通过 API 获取与下载歌词（曲目名.lrc）
   * 📖 内置的歌词 API 为网易云，如有需要，请自行修改其他 API  

* 📚 自动补全：
    * 专辑简介（Description）
    * 歌手简介（Biography）
    * 相似歌手（SimilarArtists）

* ⚠️ 需开启硬盘写入权限 rw ( 特别是: 容器 / Nas 版的 navidrome 启动配置里修改 )才能执行以下动作：
    * 歌手头像               cover.jpg
    * 专辑封面               artist.jpg
    * 歌词                  曲目名.lrc
    * 专辑画册               专辑名.pdf（需 🇫🇷 法国区 Token）
    * 增量写入本地音轨元数据   ⚠️ 慎用 ⚠️
    * 专辑元数据             qobuz_metadata.json
    * 专辑曲目写入记录列表     qobuz_processed.txt
* 🎼 🎼 古典乐 作品 写入曲目元数据供定制版 feishin 读取 （ 请从 Releases 下载定制版 feishin ）
* ⚡ 内置缓存（KVStore），减少 API 请求
 
## 🧠 插件在以下时机触发：
* ⚠️ 刮削对象没有被 navidrome 缓存
* ▶️ 播放歌曲（NowPlaying）
* 📊 Scrobble 上报
* 📀 打开专辑页
* 👤 打开歌手页

## 🚀 从 Releases 下载 qobuz.ndp 将文件放入 Navidrome 目录下的 plugins 插件文件夹里，并在官方网页里开启插件：
```text
/plugins/
└── qobuz.ndp
```
## 🛠️ 或者自行编译：
1. 安装依赖
```text
go mod init qobuz-plugin&&go mod tidy
```
2. 编译 wasm 如报警自行安装所需的工具:
```text
tinygo build -opt=2 -scheduler=none -no-debug -o plugin.wasm -target wasip1 -buildmode=c-shared .
```
3. 打包成 ndp:
```text
zip qobuz.ndp plugin.wasm manifest.json
```
## 🛠️ 启用插件示列：
```text
AGENTS = "qobuz,netease,deezer,lastfm,listenbrainz"
PLUGINS_ENABLED = true
PLUGINS_FOLDER = "./plugins"
PLUGINS_AUTORELOAD = true
PLUGINS_LOGLEVEL = "INFO"
PLUGINS_CACHESIZE = "200MB"
```
## 📖 歌手头像 / 专辑封面 / 歌词 / PDF 保存路径格式:
```text
/歌手名文件夹/
└── artist.jpg （歌手头像）
└── 专辑名文件夹
    └── cover.jpg （专辑封面）
    └── 曲目名.lrc （歌词文件）
    └── 专辑名.pdf （Qobuz_PDF）
    └── netease_metadata.json （专辑元数据文件）
    └── netease_processed.txt （写入元数据的曲目列表文件）
    └── 曲目1
    └── 曲目2
```
<img width="1002" height="1420" alt="3" src="https://github.com/user-attachments/assets/7f44ad6c-cb09-458e-946a-1d180d8829a5" />

##

<img width="2324" height="2212" alt="1" src="https://github.com/user-attachments/assets/60a817ee-41c8-4035-8359-54208597c15f" />

## 🛠️ 网页里设置与启用插件：

<img width="1612" height="1830" alt="2" src="https://github.com/user-attachments/assets/380e1aed-d629-40e9-9bd0-1fa8d6038b31" />
<img width="1788" height="1754" alt="2" src="https://github.com/user-attachments/assets/0cd1600d-22c2-455e-84f1-214fcc33644d" />

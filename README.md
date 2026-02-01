# m3u8-downloader

Golang 多线程下载 M3U8 视频工具。只需指定必要的参数，工具即可自动解析、下载、解密并合并视频。

**[2026 增强版特性]**：针对包含周期性广告切片的资源进行了逻辑优化，支持通过 MD5 校验自动剔除广告，并调用 FFmpeg 重新生成时间戳，确保合并后视频播放顺畅。

## 功能介绍

1. **智能解析**：自动解析 M3U8 索引及其嵌套。
2. **多线程下载**：支持自定义线程数，具备失败自动重试机制。
3. **同步解密**：支持 AES-128-CBC 加密片段的同步解密。
4. **暴力去广告**：**[New]** 自动检测并删除 MD5 哈希相同的重复 TS 切片。通过“只要重复全数剔除”逻辑，精准识别并清除视频中间穿插的固定广告。
5. **FFmpeg 强力合并**：**[New]** 弃用原生二进制追加，改用外部 FFmpeg 调用的 `concat` 模式合并。即便剔除了中间的广告切片，也能自动修复 PTS 时间戳，保证画面不卡顿、音画同步。

> 可以下载岛国小电影  
> 可以下载岛国小电影  
> 可以下载岛国小电影  
> 重要的事情说三遍......

## 效果展示
![demo](./demo.gif)

## 参数说明

| 参数 | 说明 | 默认值 |
| :--- | :--- | :--- |
| **-u** | m3u8下载地址 (http(s)://url/xx/xx/index.m3u8) | 必填 |
| **-o** | movieName: 自定义文件名（不带后缀） | movie |
| **-n** | num: 下载线程数 | 24 |
| **-ht** | hostType: 设置 getHost 方式 (v1: Host+路径; v2: 仅 Host) | v1 |
| **-c** | cookie: 自定义请求 Cookie (例如: "key1=v1; key2=v2") | 空 |
| **-r** | autoClear: 合并后是否自动清除 TS 缓存目录，默认保留原始TS切片 | false |
| **-s** | InsecureSkipVerify: 是否允许不安全请求 (1为开启, 0为关闭) | 0 |
| **-sp** | savePath: 文件保存的绝对路径 (默认为程序运行路径) | 当前路径 |

## 环境准备

本工具需要调用系统 `ffmpeg` 命令进行合并，请确保已安装：
- **Linux**: `apt install ffmpeg` 或 `yum install ffmpeg`
- **Mac**: `brew install ffmpeg`
- **Windows**: 下载 `ffmpeg.exe` 并添加到系统环境变量 PATH 中。

## 用法

### 1. 源码方式 (自行编译)
```bash
go build -o m3u8-downloader main.go

# 简洁使用
./m3u8-downloader -u="[http://example.com/index.m3u8](http://example.com/index.m3u8)"

# 完整示例
./m3u8-downloader -u="[http://xxx.com/xxx.m3u8](http://xxx.com/xxx.m3u8)" -o="我的视频" -n=32 -ht=v2

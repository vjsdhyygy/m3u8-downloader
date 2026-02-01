// @author:llychao<lychao_vip@163.com> @edit:vjsdhyygy<vjsdhyygy@163.com>
// @contributor: Junyi<me@junyi.pw>
// @date:2026-02-11
// @功能: 多线程下载 + 广告切片暴力去重 + FFmpeg 外部合并
package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/levigross/grequests"
)

const (
	HEAD_TIMEOUT     = 5 * time.Second
	PROGRESS_WIDTH   = 20
	TS_NAME_TEMPLATE = "%05d.ts"
)

var (
	urlFlag = flag.String("u", "", "m3u8下载地址")
	nFlag   = flag.Int("n", 24, "下载线程数")
	htFlag  = flag.String("ht", "v1", "hostType")
	oFlag   = flag.String("o", "movie", "文件名")
	cFlag   = flag.String("c", "", "cookie")
	rFlag   = flag.Bool("r", true, "自动清除ts文件")
	sFlag   = flag.Int("s", 0, "不安全请求")
	spFlag  = flag.String("sp", "", "保存路径")

	logger *log.Logger
	ro     = &grequests.RequestOptions{
		UserAgent:      "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_13_6) AppleWebKit/537.36",
		RequestTimeout: HEAD_TIMEOUT,
		Headers: map[string]string{
			"Connection": "keep-alive",
			"Accept":     "*/*",
		},
	}
)

type TsInfo struct {
	Name string
	Url  string
}

func init() {
	logger = log.New(os.Stdout, "", log.Ldate|log.Ltime|log.Lshortfile)
}

func main() {
	Run()
}

func Run() {
	fmt.Println("[模式]: 暴力去广告 - 重复切片全删 + FFmpeg 外部合并")
	runtime.GOMAXPROCS(runtime.NumCPU())
	now := time.Now()

	flag.Parse()
	m3u8Url := *urlFlag
	maxGoroutines := *nFlag
	hostType := *htFlag
	movieName := *oFlag
	autoClearFlag := *rFlag
	cookie := *cFlag
	insecure := *sFlag
	savePath := *spFlag

	if cookie != "" { ro.Headers["Cookie"] = cookie }
	if insecure != 0 { ro.InsecureSkipVerify = true }
	if !strings.HasPrefix(m3u8Url, "http") || m3u8Url == "" {
		flag.Usage()
		return
	}

	pwd, _ := os.Getwd()
	if savePath != "" { pwd = savePath }
	download_dir := filepath.Join(pwd, movieName)
	if isExist, _ := pathExists(download_dir); !isExist {
		os.MkdirAll(download_dir, os.ModePerm)
	}

	m3u8Host := getHost(m3u8Url, hostType)
	m3u8Body := getM3u8Body(m3u8Url)
	ts_key := getM3u8Key(m3u8Host, m3u8Body)
	ts_list := getTsList(m3u8Host, m3u8Body)
	fmt.Println("待下载 ts 文件数量:", len(ts_list))

	// 1. 下载
	downloader(ts_list, maxGoroutines, download_dir, ts_key)

	// 2. 暴力剔除重复项（针对广告切片 MD5 相同的特征）
	purgeAllDuplicates(download_dir)

	if ok := checkTsDownDir(download_dir); !ok {
		fmt.Printf("\n[Failed] 目录为空或有效切片不足 \n")
		return
	}

	// 3. 调用外部 FFmpeg 合并（解决广告删除后的 PTS 连续性问题）
	mv := mergeWithFFmpeg(download_dir, movieName, pwd)

	if autoClearFlag && mv != "" {
		os.RemoveAll(download_dir)
	}

	DrawProgressBar("Merging", float32(1), PROGRESS_WIDTH, mv)
	fmt.Printf("\n[Success] 处理完成：%s | 耗时: %6.2fs\n", mv, time.Now().Sub(now).Seconds())
}

func purgeAllDuplicates(downloadDir string) {
	fmt.Printf("\n[校验] 正在分析并剔除广告切片...")
	hashCount := make(map[string]int)
	hashToFileList := make(map[string][]string)

	files, _ := ioutil.ReadDir(downloadDir)
	for _, f := range files {
		if filepath.Ext(f.Name()) != ".ts" { continue }
		path := filepath.Join(downloadDir, f.Name())
		
		file, err := os.Open(path)
		if err != nil { continue }
		h := md5.New()
		io.Copy(h, file)
		file.Close()
		sha := hex.EncodeToString(h.Sum(nil))

		hashCount[sha]++
		hashToFileList[sha] = append(hashToFileList[sha], path)
	}

	delTotal := 0
	for sha, count := range hashCount {
		if count > 1 {
			fmt.Printf("\n[发现广告/重复] Hash: %s 出现 %d 次，正在执行剔除...", sha[:8], count)
			for _, p := range hashToFileList[sha] {
				os.Remove(p)
				delTotal++
			}
		}
	}
	fmt.Printf("\n[清理] 已剔除 %d 个异常文件。\n", delTotal)
}

func mergeWithFFmpeg(downloadDir, movieName, savePath string) string {
	listPath := filepath.Join(downloadDir, "filelist.txt")
	listFile, _ := os.Create(listPath)
	
	files, _ := ioutil.ReadDir(downloadDir)
	count := 0
	for _, f := range files {
		if filepath.Ext(f.Name()) == ".ts" {
			listFile.WriteString(fmt.Sprintf("file '%s'\n", f.Name()))
			count++
		}
	}
	listFile.Close()

	if count == 0 { return "" }

	outputMp4 := filepath.Join(savePath, movieName+".mp4")
	// 外部调用 ffmpeg 保证删除中间切片后视频依然能流畅播放
	cmd := exec.Command("ffmpeg", "-f", "concat", "-safe", "0", "-i", listPath, "-c", "copy", "-fflags", "+genpts", "-y", outputMp4)
	
	var errOut bytes.Buffer
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		fmt.Printf("\n[FFmpeg Error]: %s\n", errOut.String())
		return ""
	}
	return outputMp4
}

// --- 以下保持原下载逻辑不变 ---

func getHost(Url, ht string) (host string) {
	u, _ := url.Parse(Url)
	switch ht {
	case "v1": host = u.Scheme + "://" + u.Host + filepath.Dir(u.EscapedPath())
	case "v2": host = u.Scheme + "://" + u.Host
	}
	return
}

func getM3u8Body(Url string) string {
	r, _ := grequests.Get(Url, ro)
	return r.String()
}

func getM3u8Key(host, html string) (key string) {
	lines := strings.Split(html, "\n")
	for _, line := range lines {
		if strings.Contains(line, "#EXT-X-KEY") && strings.Contains(line, "URI") {
			parts := strings.Split(line, "\"")
			if len(parts) < 2 { continue }
			key_url := parts[1]
			if !strings.HasPrefix(key_url, "http") {
				key_url = fmt.Sprintf("%s/%s", host, strings.TrimPrefix(key_url, "/"))
			}
			res, _ := grequests.Get(key_url, ro)
			if res != nil && res.StatusCode == 200 { return res.String() }
		}
	}
	return ""
}

func getTsList(host, body string) (tsList []TsInfo) {
	lines := strings.Split(body, "\n")
	index := 0
	for _, line := range lines {
		if !strings.HasPrefix(line, "#") && line != "" {
			index++
			tsUrl := line
			if !strings.HasPrefix(line, "http") {
				tsUrl = fmt.Sprintf("%s/%s", host, strings.TrimPrefix(line, "/"))
			}
			tsList = append(tsList, TsInfo{Name: fmt.Sprintf(TS_NAME_TEMPLATE, index), Url: tsUrl})
		}
	}
	return
}

func downloadTsFile(ts TsInfo, download_dir, key string, retries int) {
	curr_path := filepath.Join(download_dir, ts.Name)
	if isExist, _ := pathExists(curr_path); isExist { return }
	res, _ := grequests.Get(ts.Url, ro)
	if res == nil || !res.Ok {
		if retries > 0 { downloadTsFile(ts, download_dir, key, retries-1) }
		return
	}
	data := res.Bytes()
	if key != "" { data, _ = AesDecrypt(data, []byte(key)) }
	for j := 0; j < len(data); j++ {
		if data[j] == 71 {
			data = data[j:]
			break
		}
	}
	ioutil.WriteFile(curr_path, data, 0666)
}

func downloader(tsList []TsInfo, maxGoroutines int, downloadDir string, key string) {
	var wg sync.WaitGroup
	limiter := make(chan struct{}, maxGoroutines)
	tsLen := len(tsList)
	for i, ts := range tsList {
		wg.Add(1)
		limiter <- struct{}{}
		go func(t TsInfo, count int) {
			defer wg.Done()
			defer func() { <-limiter }()
			downloadTsFile(t, downloadDir, key, 5)
			DrawProgressBar("Downloading", float32(count+1)/float32(tsLen), PROGRESS_WIDTH, t.Name)
		}(ts, i)
	}
	wg.Wait()
}

func checkTsDownDir(dir string) bool {
	f, _ := ioutil.ReadDir(dir)
	return len(f) > 0
}

func DrawProgressBar(prefix string, proportion float32, width int, suffix ...string) {
	pos := int(proportion * float32(width))
	s := fmt.Sprintf("[%s] %s%*s %6.2f%% \t%s", prefix, strings.Repeat("■", pos), width-pos, "", proportion*100, strings.Join(suffix, ""))
	fmt.Print("\r" + s)
}

func pathExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil { return true, nil }
	return false, nil
}

func AesDecrypt(crypted, key []byte) ([]byte, error) {
	block, _ := aes.NewCipher(key)
	blockMode := cipher.NewCBCDecrypter(block, key[:block.BlockSize()])
	origData := make([]byte, len(crypted))
	blockMode.CryptBlocks(origData, crypted)
	return PKCS7UnPadding(origData), nil
}

func PKCS7UnPadding(origData []byte) []byte {
	length := len(origData)
	if length == 0 { return nil }
	unpadding := int(origData[length-1])
	if unpadding > length { return origData }
	return origData[:(length - unpadding)]
}

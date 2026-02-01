// @author:llychao<lychao_vip@163.com> @edit:vjsdhyygy<vjsdhyygy@163.com>
// @contributor: Junyi<me@junyi.pw>
// @date:2026-02-11
// @功能:golang m3u8 video Downloader
package main

import (
	"bufio"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/levigross/grequests"
)

const (
	HEAD_TIMEOUT = 5 * time.Second
	PROGRESS_WIDTH = 20
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
		UserAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_13_6) AppleWebKit/537.36",
		RequestTimeout: HEAD_TIMEOUT,
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
	fmt.Println("[模式]: 暴力去广告模式 - 重复切片全数剔除 + FFmpeg 外部合并")
	runtime.GOMAXPROCS(runtime.NumCPU())
	now := time.Now()

	flag.Parse()
	m3u8Url := *urlFlag
	maxGoroutines := *nFlag
	hostType := *htFlag
	movieName := *oFlag
	autoClearFlag := *rFlag
	savePath := *spFlag

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

	// 2. 暴力剔除重复项 (针对广告特征)
	purgeAllDuplicates(download_dir)

	// 3. 调用外部 FFmpeg 合并
	mv := mergeWithFFmpeg(download_dir, movieName, pwd)

	if autoClearFlag && mv != "" {
		os.RemoveAll(download_dir)
	}

	fmt.Printf("\n[Success] 视频处理完成：%s | 耗时: %6.2fs\n", mv, time.Now().Sub(now).Seconds())
}

// 暴力剔除：只要重复，通通删光
func purgeAllDuplicates(downloadDir string) {
	fmt.Printf("\n[校验] 正在扫描广告切片并剔除...")
	hashCount := make(map[string]int)
	hashToFileList := make(map[string][]string)

	files, _ := ioutil.ReadDir(downloadDir)
	for _, f := range files {
		if filepath.Ext(f.Name()) != ".ts" { continue }
		path := filepath.Join(downloadDir, f.Name())
		
		file, _ := os.Open(path)
		h := md5.New()
		io.Copy(h, file)
		file.Close()
		sha := hex.EncodeToString(h.Sum(nil))

		hashCount[sha]++
		hashToFileList[sha] = append(hashToFileList[sha], path)
	}

	delCount := 0
	for sha, count := range hashCount {
		if count > 1 {
			fmt.Printf("\n[命中广告/重复项] 内容哈希 %s 出现 %d 次，全数清理...", sha[:8], count)
			for _, p := range hashToFileList[sha] {
				os.Remove(p)
				delCount++
			}
		}
	}
	fmt.Printf("\n[完成] 共剔除 %d 个异常切片。\n", delCount)
}

// 调用外部 FFmpeg 合并
func mergeWithFFmpeg(downloadDir, movieName, savePath string) string {
	fmt.Println("[FFmpeg] 正在生成合并清单并调用外部 FFmpeg...")
	
	// 1. 生成 filelist.txt
	listPath := filepath.Join(downloadDir, "filelist.txt")
	listFile, _ := os.Create(listPath)
	
	files, _ := ioutil.ReadDir(downloadDir)
	for _, f := range files {
		if filepath.Ext(f.Name()) == ".ts" {
			// FFmpeg concat 格式: file 'path'
			listFile.WriteString(fmt.Sprintf("file '%s'\n", f.Name()))
		}
	}
	listFile.Close()

	outputMp4 := filepath.Join(savePath, movieName+".mp4")
	
	// 2. 外部调用 FFmpeg (增加 -fflags +genpts 以修复删除切片后的时间戳)
	cmd := exec.Command("ffmpeg", "-f", "concat", "-safe", "0", "-i", listPath, "-c", "copy", "-fflags", "+genpts", "-y", outputMp4)
	
	var errOut bytes.Buffer
	cmd.Stderr = &errOut

	err := cmd.Run()
	if err != nil {
		fmt.Printf("\n[错误] FFmpeg 执行失败: %v\n详情: %s\n", err, errOut.String())
		return ""
	}

	return outputMp4
}

// --- 原有辅助函数 (保持不变，确保兼容 go.mod) ---

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

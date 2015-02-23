// imagemagick_server
//   $ go run imagemagick_server.go
//   GET http://127.0.0.1:8888/fill/300/300/example.com/path/to/name
package main

import (
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v2"
)

var cacheDir = flag.String("cachedir", "cache", "File cache dir")
var cacheSize = flag.Int64("cachesize", 1*1024*1024*1024, "Max file cache size")
var bind = flag.String("bind", "8888", "Start server on port or unix domain socket path")
var optsPath = flag.String("opts", "./opts.yml", "functions for imagemagick")

var client http.Client
var opts map[string][]string

// Sort Interface
type byModTimeDesc []os.FileInfo

func (f byModTimeDesc) Len() int           { return len(f) }
func (f byModTimeDesc) Less(i, j int) bool { return f[i].ModTime().Second() > f[j].ModTime().Second() }
func (f byModTimeDesc) Swap(i, j int)      { f[i], f[j] = f[j], f[i] }

func init() {
	runtime.GOMAXPROCS(runtime.NumCPU())
}

func readOpts() {
	buf, err := ioutil.ReadFile(*optsPath)
	if err != nil {
		panic(err)
	}

	opts = make(map[string][]string)
	err = yaml.Unmarshal(buf, &opts)
	if err != nil {
		panic(err)
	}
	fmt.Println("load opts=" + *optsPath)
}

func errorServer(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "404 Not Found", http.StatusNotFound)
}

func checkCache() {
	infos, err := ioutil.ReadDir(*cacheDir)
	if err != nil {
		fmt.Println(err.Error())
		os.Exit(1)
	}
	sort.Sort(byModTimeDesc(infos))

	var sum int64
	for _, info := range infos {
		sum = sum + info.Size()
	}
	for i := 0; *cacheSize < sum; i++ {
		sum -= infos[i].Size()
		fmt.Println("Remove: ", *cacheDir+"/"+infos[i].Name())
		os.Remove(*cacheDir + "/" + infos[i].Name())
	}
}

func parseURL(w http.ResponseWriter, r *http.Request) (fn string, width string, height string, srcPath string, srcFileName string, err error) {
	path := r.URL.RequestURI()
	if path[0] != '/' {
		http.Error(w, "Path should start with /", http.StatusBadRequest)
		return
	}
	parts := strings.SplitN(path[1:], "/", 4)

	fn = parts[0]

	width = parts[1]
	widthNum, err := strconv.Atoi(width)
	if err != nil {
		return
	}
	if widthNum <= 0 || 5000 < widthNum {
		http.Error(w, "Width not specified or invalid", http.StatusBadRequest)
		err = fmt.Errorf("invalid error width")
		return
	}

	height = parts[2]
	heightNum, err := strconv.Atoi(height)
	if err != nil {
		return
	}
	if heightNum <= 0 || 5000 < heightNum {
		http.Error(w, "Height not specified or invalid", http.StatusBadRequest)
		err = fmt.Errorf("invalid error height")
		return
	}

	srcPath = parts[3]
	index := strings.Index(srcPath, "?")
	if index == -1 {
		index = len(srcPath)
	}
	srcFileName = *cacheDir + "/" + url.QueryEscape(srcPath[0:index])

	return
}

func server(w http.ResponseWriter, r *http.Request) {
	path := r.URL.RequestURI()
	if path[0] != '/' {
		http.Error(w, "Path should start with /", http.StatusBadRequest)
		return
	}
	fn, width, height, srcPath, srcFileName, err := parseURL(w, r)
	if err != nil {
		http.Error(w, "Upstream failed Atoi:", http.StatusBadRequest)
		return
	}

	_, err = os.Stat(srcFileName)
	if err == nil {
		// cache hit
	} else {
		// cache miss
		cacheFile, err := os.OpenFile(srcFileName, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0666)
		if err != nil {
			http.Error(w, "Upstream failed Open: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer func() {
			cacheFile.Close()
		}()

		srcReader, err := http.Get("https://" + srcPath)
		if err != nil {
			http.Error(w, "Upstream failed Get: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer srcReader.Body.Close()

		if srcReader.StatusCode != http.StatusOK {
			http.Error(w, "Upstream failed HttpStatus: "+srcReader.Status, srcReader.StatusCode)
			return
		}

		io.Copy(cacheFile, srcReader.Body)
		go checkCache()
	}

	tempfile, err := ioutil.TempFile("", "thumb")
	if err != nil {
		http.Error(w, "Upstream failed Tempfile: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer func() {
		tempfile.Close()
		os.Remove(tempfile.Name())
	}()

	var targetName string
	if fn != "none" {
		cmdRet := []string{"OMP_NUM_THREADS=1", "convert"}
		for _, cmdStr := range opts[fn] {
			cmdStr = strings.Replace(cmdStr, "{{width}}", width, -1)
			cmdStr = strings.Replace(cmdStr, "{{height}}", height, -1)
			cmdRet = append(cmdRet, regexp.MustCompile("\\s+").Split(cmdStr, -1)...)
		}

		cmdRet = append(cmdRet, srcFileName, tempfile.Name())
		err = exec.Command("env", cmdRet...).Run()
		if err != nil {
			http.Error(w, "Upstream failed cmd Run: "+err.Error(), http.StatusBadGateway)
			return
		}
		targetName = tempfile.Name()
	} else {
		targetName = srcFileName
	}

	file, err := os.Open(targetName)
	if err != nil {
		http.Error(w, "Upstream failed open: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer file.Close()

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
	fileinfo, err := file.Stat()
	if err == nil {
		w.Header().Set("Content-Length", fmt.Sprint(fileinfo.Size()))
	}

	io.Copy(w, file)
}

func main() {
	flag.Parse()
	_ = os.Mkdir(*cacheDir, 0777)
	readOpts()
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGHUP)
	go func() {
		for {
			switch <-c {
			case syscall.SIGHUP:
				readOpts()
			}
		}
	}()
	http.HandleFunc("/", server)
	http.HandleFunc("/favicon.ico", errorServer)

	fmt.Println("listen: " + *bind)
	_, err := strconv.Atoi(*bind)
	if err != nil {
		// listen unix domain socket
		os.Remove(*bind)
		listnr, err := net.Listen("unix", *bind)
		if err != nil {
			panic(err)
		}
		err = os.Chmod(*bind, 0777)
		if err != nil {
			panic(err)
		}
		err = http.Serve(listnr, nil)
		if err != nil {
			panic(err)
		}
	} else {
		// listen tcp port
		err := http.ListenAndServe(":"+*bind, nil)
		if err != nil {
			panic(err)
		}
	}
}

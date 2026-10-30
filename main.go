package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"syscall"
	"time"
)

func main() {
	port := flag.Int("port", 8787, "监听端口（被占用时自动改用随机端口）")
	openFlag := flag.Bool("open", true, "启动后自动打开管理界面")
	awake := flag.Bool("awake", false, "服务期间阻止电脑休眠（macOS: caffeinate）")
	flag.Parse()

	explicitPort := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "port" {
			explicitPort = true
		}
	})

	ln, actualPort, err := listen(*port, explicitPort)
	if err != nil {
		log.Fatalf("无法监听端口: %v", err)
	}

	adminToken := randomToken(16)
	store := NewStore(adminToken)
	ip := primaryLANIP()
	if ip == "" {
		ip = "127.0.0.1"
	}
	store.BaseURL = fmt.Sprintf("http://%s:%d", ip, actualPort)

	br := newBroker()
	srv := newServer(store, br)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go srv.runTickers(ctx)

	if *awake && runtime.GOOS == "darwin" {
		cmd := exec.Command("caffeinate", "-d", "-i", "-s", "-w", strconv.Itoa(os.Getpid()))
		if err := cmd.Start(); err != nil {
			log.Printf("caffeinate 启动失败: %v", err)
		}
	}

	adminURL := fmt.Sprintf("http://127.0.0.1:%d/#t=%s", actualPort, adminToken)
	printBanner(adminURL, store.BaseURL)
	if *openFlag {
		if err := openBrowser(adminURL); err != nil {
			log.Printf("自动打开浏览器失败: %v", err)
		}
	}

	httpSrv := &http.Server{
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		ConnState:         tuneConnBuffers,
	}
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()

	if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
		log.Fatalf("服务异常退出: %v", err)
	}
	// Serve returns as soon as the listener closes; wait for the graceful
	// drain so in-flight transfers get their 2 s to finish.
	<-shutdownDone
	srv.uploads.Close() // staged drag-and-drop copies
}

// tuneConnBuffers widens the socket buffers of newly accepted connections.
//
// macOS gives every TCP socket a ~128 KiB send buffer by default. A single
// stream cannot outrun sndbuf/RTT, so across a Wi-Fi link with a few
// milliseconds of round trip that caps one download at roughly 15-30 MB/s,
// right where large transfers were landing, no matter how fast the radio is.
// 2 MiB lifts the ceiling far above what the air can carry.
//
// Only tuned on macOS: Linux clamps SO_SNDBUF to net.core.wmem_max and turns
// off its own send-buffer autotuning once the size is set by hand, which
// would make things slower there. Best effort; errors keep the defaults.
func tuneConnBuffers(c net.Conn, st http.ConnState) {
	if st != http.StateNew || runtime.GOOS != "darwin" {
		return
	}
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetWriteBuffer(2 << 20)
		_ = tc.SetReadBuffer(1 << 20)
	}
}

func listen(port int, explicit bool) (net.Listener, int, error) {
	ln, err := net.Listen("tcp4", fmt.Sprintf(":%d", port))
	if err != nil && !explicit {
		ln, err = net.Listen("tcp4", ":0")
	}
	if err != nil {
		return nil, 0, err
	}
	return ln, ln.Addr().(*net.TCPAddr).Port, nil
}

func printBanner(adminURL, lanURL string) {
	fmt.Printf("\n  %s v%s · 局域网文件快传\n", appName, appVersion)
	fmt.Printf("  ────────────────────────────────────────\n")
	fmt.Printf("  ▸ 电脑管理界面   %s\n", adminURL)
	fmt.Printf("  ▸ 手机访问地址   %s\n", lanURL)
	fmt.Printf("  ▸ 在电脑上选择文件并「开始共享」，再用手机扫码\n\n")
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

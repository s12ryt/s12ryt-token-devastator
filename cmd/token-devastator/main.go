// token毀滅者：把用不完的 API token 燒成灰燼的燒毀工具。
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"token-devastator/internal/config"
	"token-devastator/internal/server"
)

func main() {
	configPath := flag.String("config", "config.json", "設定檔路徑")
	addr := flag.String("addr", "", "覆蓋監聽地址（優先於設定檔，僅影響本次執行）")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("載入設定失敗: %v", err)
	}
	listen := cfg.Listen
	if *addr != "" {
		listen = *addr
	}

	srv := server.New(cfg, *configPath)
	httpSrv := &http.Server{
		Addr:              listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("🔥 token毀滅者面板已啟動：http://%s（設定檔：%s）", listen, *configPath)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP 伺服器錯誤: %v", err)
		}
	}()

	// Ctrl+C / SIGTERM 優雅關閉
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Println("正在關閉…")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(ctx); err != nil {
		log.Printf("關閉逾時: %v", err)
	}
	log.Println("已結束")
}

// main 是 AI 模型网关的入口文件。
// 职责：加载配置 → 初始化日志 → 注册路由 → 启动 HTTP 服务 → 优雅关闭。
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go-gateway/internal/config"
	"go-gateway/internal/middleware"
	"go-gateway/internal/server"
)

func main() {
	// 1. 加载配置文件（config.yaml），支持 ${ENV_VAR} 环境变量替换
	cfg, err := config.Load("config.yaml")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// 2. 初始化日志模块，按天自动轮转日志文件
	logger, err := middleware.NewLogger("logs")
	if err != nil {
		log.Fatalf("Failed to create logger: %v", err)
	}
	defer logger.Close()

	// 3. 创建 HTTP 处理器并注册路由
	handler := server.NewHandler(cfg, logger)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// 4. 配置 HTTP 服务器（超时从配置文件读取）
	httpServer := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Server.Port),
		Handler:      mux,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
	}

	// 5. 异步启动服务，主 goroutine 等待退出信号
	go func() {
		log.Printf("Gateway listening on :%d", cfg.Server.Port)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	// 6. 监听 SIGINT / SIGTERM 信号，实现优雅关闭
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down server...")
	// 7. 10 秒超时等待现有请求处理完毕
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		log.Fatalf("Server forced shutdown: %v", err)
	}
	log.Println("Server exited")
}
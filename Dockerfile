# Stage 1: 编译阶段
FROM golang:alpine AS builder

WORKDIR /app

# 先复制依赖文件，利用 Docker 缓存层加速构建
# 设置国内 Go 代理，解决容器内无法访问 proxy.golang.org 的问题
COPY go.mod go.sum ./
RUN go env -w GOPROXY=https://goproxy.cn,direct && go mod download

# 复制源码并编译
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /gateway ./cmd/gateway/

# Stage 2: 运行阶段（scratch 空镜像，最小体积）
FROM scratch

# 从编译阶段复制 CA 证书（HTTPS 请求必需）
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /gateway /gateway

# 配置文件通过 volume 挂载，此处放一个默认的
COPY config.yaml /config.yaml

EXPOSE 8080

ENTRYPOINT ["/gateway"]
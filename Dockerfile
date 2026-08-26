# Go 版本 Dockerfile - 多阶段构建，最终 scratch 镜像

FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder

# 安装 CA 证书和时区数据
RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

# 先复制依赖文件，利用 Docker 缓存
COPY go.mod go.sum ./
RUN go mod download

# 复制源代码并构建（buildx 自动注入 TARGETOS/TARGETARCH，在 BUILDPLATFORM 上交叉编译）
ARG TARGETOS
ARG TARGETARCH
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -ldflags="-s -w" \
    -o /rssgen \
    ./cmd/rssgen

# 最终 scratch 镜像
FROM scratch

# 复制 CA 证书（HTTPS 请求需要）
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# 复制时区数据
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo

# 复制编译好的二进制文件
COPY --from=builder /rssgen /rssgen

# 与 docker-compose 挂载点对齐：配置挂到 /app/config.yml。
# server 模式以相对路径读取 config.yml，故工作目录必须是 /app。
# 不烤入示例配置：未挂载配置时应直接报错退出，而非静默加载占位符。
WORKDIR /app

EXPOSE 8000

ENTRYPOINT ["/rssgen"]

# 多階段建置：靜態編譯後塞進最小映像
FROM golang:1.26 AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" \
    -o /out/token-devastator ./cmd/token-devastator
# distroless 無 shell 不能 RUN mkdir；以空目錄 COPY --chown 建立設定目錄，
# 讓 named volume 初始化時繼承 nonroot 擁有權（否則 root 建立目錄，保存設定會失敗）
RUN mkdir -p /out/cfg

# distroless/static：最小面積＋內建 CA 憑證（打 HTTPS API 必需）；nonroot 身分執行
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/token-devastator /usr/local/bin/token-devastator
COPY --from=build --chown=nonroot:nonroot /out/cfg /etc/token-devastator

ENV TD_CONFIG=/etc/token-devastator/config.json
EXPOSE 24300
VOLUME ["/etc/token-devastator"]

USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/token-devastator"]
CMD ["-config", "/etc/token-devastator/config.json", "-addr", "0.0.0.0:24300"]

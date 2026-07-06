# ---- build stage ----
FROM golang:1.26-alpine AS build
WORKDIR /src

# cache deps first
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# static binary; templates, CSS, JS, logo and migrations are embedded via go:embed
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/valaf ./cmd/valaf

# ---- runtime stage ----
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata \
 && addgroup -S valaf && adduser -S -G valaf valaf
COPY --from=build /out/valaf /usr/local/bin/valaf

USER valaf
EXPOSE 8080
# healthcheck hits the liveness endpoint (worker role ignores it harmlessly)
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
  CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["valaf"]
CMD ["serve"]

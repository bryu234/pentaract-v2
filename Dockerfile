FROM node:24-alpine AS web-builder
WORKDIR /web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.24-alpine AS go-dev
RUN apk add --no-cache git
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .

FROM go-dev AS go-builder
COPY --from=web-builder /web/dist /src/internal/webui/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/pentaract ./cmd/pentaract

FROM alpine:3.22 AS runtime
RUN apk add --no-cache ca-certificates postgresql17-client tzdata wget
COPY --from=go-builder /out/pentaract /usr/local/bin/pentaract
WORKDIR /app
EXPOSE 8080
ENTRYPOINT ["pentaract"]
CMD ["serve"]

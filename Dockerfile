FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG APP=api
# From the requested image platform: cross-compile natively instead of emulating.
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
	go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/${APP}
# Staged here because the runtime image has no shell to mkdir a writable log dir.
RUN mkdir -p /out/logs

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/app /app/app
COPY --from=build --chown=65532:65532 /out/logs /app/logs

# Documentation only: the real port comes from server.port in the config.
EXPOSE 8080

# Exec form, so the binary is PID 1 and receives SIGTERM from `docker stop` directly.
ENTRYPOINT ["/app/app"]

# =========================
# Build stage
# =========================
FROM golang:1.26 AS build

ARG TARGETARCH
ARG TARGETOS
ARG GOARM
ARG VERSION
ARG GIT_COMMIT

WORKDIR /src
COPY . .

# 针对不同架构编译
ENV GOARM=${GOARM:-}
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build \
    -ldflags "-s -w -X main.Version=$VERSION -X main.GitCommit=$GIT_COMMIT" \
    -o /out/fwatchdog

# =========================
# Ship stage
# =========================
FROM scratch AS ship
COPY --from=build /out/fwatchdog /fwatchdog
ENTRYPOINT ["/fwatchdog"]
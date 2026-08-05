FROM golang:alpine AS builder

ENV GO111MODULE=on \
    CGO_ENABLED=0 \
    GOOS=linux

RUN apk add git

WORKDIR /build

# Download dependency
COPY . .
RUN go mod download

# Install plugins declared in configurations/plugins.yaml (no-op if the file is empty).
# For private plugins, pass a token: docker build --build-arg BEELZEBUB_GITHUB_TOKEN=...
ARG BEELZEBUB_GITHUB_TOKEN=""
RUN BEELZEBUB_GITHUB_TOKEN="$BEELZEBUB_GITHUB_TOKEN" go run . plugin install --no-build

# Build
RUN go build -o main .

WORKDIR /dist

RUN cp /build/main .

# Use scratch image as finally tiny container 
FROM scratch

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /dist/main /

ENTRYPOINT ["/main", "run"]

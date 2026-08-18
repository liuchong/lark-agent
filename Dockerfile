FROM golang:1.25-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/lark-agent ./cmd/lark-agent

FROM alpine:3.22
RUN addgroup -S lark-agent && adduser -S -G lark-agent lark-agent
COPY --from=build /out/lark-agent /usr/local/bin/lark-agent
COPY scripts/lark-agent-action /usr/local/bin/lark-agent-action
RUN chmod 0755 /usr/local/bin/lark-agent-action
USER lark-agent
ENTRYPOINT ["/usr/local/bin/lark-agent-action"]

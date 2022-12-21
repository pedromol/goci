FROM golang:alpine as build

COPY main.go /app/
COPY go.mod /app/
COPY go.sum /app/

WORKDIR /app

RUN go mod tidy
RUN go build -ldflags="-w -s" main.go
RUN chmod a+x main

FROM alpine:latest

RUN adduser \
    --disabled-password \
    --gecos "" \
    --home "/nonexistent" \
    --shell "/sbin/nologin" \
    --no-create-home \
    --uid "10001" \
    "unprivileged"

COPY --from=build /app/main /

USER unprivileged:unprivileged

EXPOSE 2223

ENTRYPOINT ["/main"]

ARG BUILDER_IMAGE=golang:1.25.8-alpine
FROM ${BUILDER_IMAGE} AS build

WORKDIR /src

COPY go.mod ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags='-s -w' -o /out/dashboard ./cmd/dashboard

ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian12:nonroot
FROM ${RUNTIME_IMAGE}

ENV DASHBOARD_PORT=8080
EXPOSE 8080

COPY --from=build /out/dashboard /dashboard

ENTRYPOINT ["/dashboard"]

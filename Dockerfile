FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/tos-tag ./cmd/api

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/tos-tag /usr/local/bin/tos-tag
USER nonroot:nonroot
EXPOSE 8090
ENTRYPOINT ["/usr/local/bin/tos-tag"]
